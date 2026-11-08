import AVFoundation
import Foundation
import LeafletCore
import UIKit

/// Recorder is recording as the app's views see it: the recording in
/// progress, the recordings the open pages' blocks show, and the one
/// playing. One for the app, as the core records one at a time; the
/// blocks, which TextKit hosts apart from the windows' views, reach it as
/// Recorder.shared.
@MainActor
@Observable
final class Recorder {
    static let shared = Recorder()

    /// state is the recording in progress, as the core last said; at is
    /// when it said it, which its time counts on from. Its levels and the
    /// words heard, which change many times a second, are apart, levels
    /// and liveText, for only the live block to read: each view that reads
    /// state draws again as it changes.
    private(set) var state = RecordingState.idle
    private(set) var at = Date()
    private(set) var levels: [Double] = []
    private(set) var liveText = ""
    /// rows are the recordings the blocks asked for, by ID; missing, those
    /// not on this device, as one made on another not synced yet.
    private(set) var rows: [String: RecordingRow] = [:]
    private(set) var missing: Set<String> = []
    /// playing is the recording the player plays, or nil; loading, the one
    /// whose audio is coming.
    private(set) var playing: String?
    private(set) var loading: String?
    private(set) var player: AVAudioPlayer?
    /// changes counts the player's plays and pauses, which it doesn't
    /// tell, for the views to read it again.
    private(set) var changes = 0

    /// speech are the speech providers a transcription on each page asked
    /// for can be picked from, and the one marked, by page.
    private(set) var speech: [String: SpeechChoices] = [:]

    @ObservationIgnored private var client: CoreClient?
    /// attaching gives the core the microphone, which a recording needs.
    @ObservationIgnored private var attaching: Task<Void, Never>?
    @ObservationIgnored private var speechAsked: Set<String> = []
    @ObservationIgnored private var toast: ((String) -> Void)?
    @ObservationIgnored private var asked: Set<String> = []
    /// reads numbers the reads of each recording, as a stop and the end of
    /// its transcription each read it: "transcribing" read first doesn't
    /// replace "ready" read after.
    @ObservationIgnored private var reads = Newest()
    /// fetching brings loading's audio and starts it; stopPlaying cancels
    /// it, so a recording asked for whose block has gone never starts, and
    /// fetch tells it from one stopped before it, which may still be
    /// finishing an await it can't be cancelled out of.
    @ObservationIgnored private var fetching: Task<Void, Never>?
    @ObservationIgnored private var fetch = 0
    @ObservationIgnored private let microphone = Microphone()
    /// finishing is the time iOS gives the app in the background while
    /// recordings transcribe, as one stopped from the Live Activity does.
    @ObservationIgnored private var finishing = UIBackgroundTaskIdentifier.invalid
    /// ended hears the player reach the end, which it holds weakly.
    @ObservationIgnored private lazy var ended = PlayerEnded { [weak self] in
        self?.changes += 1
        self?.letAudioGo()
    }

    private init() {}

    /// attach gives the recorder the core, and the core the microphone;
    /// toast tells the user what went wrong.
    func attach(_ client: CoreClient, toast: @escaping (String) -> Void) {
        self.client = client
        self.toast = toast
        attaching = Task {
            if DeviceTranscriber.isAvailable {
                await client.useTranscriber(DeviceTranscriber())
            }
            await client.useMicrophone(microphone)
            if let s = try? await client.recordingState() {
                update(s)
                update(levels: s.levels)
            }
            // A Live Activity an earlier launch left, ended as it
            // recorded, goes: update tells it nothing while none records.
            if !state.recording { RecordingActivity.shared.end() }
        }
    }

    /// ready waits for the core to have the microphone, as an intent run
    /// as the app launches records at once.
    func ready() async { await attaching?.value }

    /// elapsed is how long the recording in progress has run, now.
    func elapsed(at now: Date) -> TimeInterval {
        let base = TimeInterval(state.elapsedMs) / 1000
        return state.active && !state.paused ? base + now.timeIntervalSince(at) : base
    }

    /// update takes the core's state of the recording.
    func update(_ s: RecordingState) {
        if s.liveText != liveText { liveText = s.liveText }
        var s = s
        s.levels = []
        s.liveText = ""
        let old = state
        // As only its time goes on, which elapsed counts on, it stays.
        var same = s
        same.elapsedMs = old.elapsedMs
        guard same != old else { return }
        state = s
        at = Date()
        RecordingActivity.shared.sync(s, at: at)
        holdOn(s)
        if !s.error.isEmpty, s.error != old.error { toast?(s.error) }
        // A recording stopped, or a transcription ended: their rows change.
        let done = Set(old.transcribing + [old.id]).subtracting(s.transcribing + [s.id])
        for id in done where !id.isEmpty { Task { await load(id) } }
    }

    /// holdOn keeps the app running in the background while recordings
    /// transcribe, for as long as iOS lets it: one stopped from the Live
    /// Activity, with the app away, stops the microphone that kept it
    /// running. One cut short goes on as the app comes back.
    private func holdOn(_ s: RecordingState) {
        let busy = !s.transcribing.isEmpty || !s.finishing.isEmpty
        if busy, finishing == .invalid {
            finishing = UIApplication.shared.beginBackgroundTask(withName: "Transcribe") {
                [weak self] in
                MainActor.assumeIsolated { self?.letGo() }
            }
        } else if !busy {
            letGo()
        }
    }

    private func letGo() {
        guard finishing != .invalid else { return }
        UIApplication.shared.endBackgroundTask(finishing)
        finishing = .invalid
    }

    /// update takes the latest levels of the recording in progress.
    func update(levels: [Double]) {
        if levels != self.levels { self.levels = levels }
    }

    // MARK: Recording

    /// start records into the page, putting its block at the editor's
    /// caret, transcribed by provider, or the one marked for the page when
    /// it is "".
    func start(pageID: String, editor: EditorController?, provider: String = "") {
        guard let client, !state.recording else { return }
        stopPlaying()
        Task {
            do {
                let node = try await client.startRecording(pageID, provider: provider)
                editor?.insertBlock(node)
            } catch {
                toast?(error.localizedDescription)
            }
            await loadChoices(pageID)
        }
    }

    // MARK: Speech providers

    /// choices returns the speech providers a transcription on page can be
    /// picked from, and the one marked, reading them the first time: there
    /// is no default, so with more than one, each transcription asks.
    func choices(for page: String) -> SpeechChoices? {
        if speechAsked.insert(page).inserted { Task { await loadChoices(page) } }
        return speech[page]
    }

    /// reloadChoices reads the choices again, as the settings or the
    /// session change: a key saved or removed changes them.
    func reloadChoices() {
        for page in speechAsked { Task { await loadChoices(page) } }
    }

    private func loadChoices(_ page: String) async {
        guard let client, let c = try? await client.speechChoices(page) else { return }
        if speech[page] != c { speech[page] = c }
    }

    func stop() {
        guard let client else { return }
        Task { await client.stopRecording() }
    }

    func togglePause() {
        guard let client else { return }
        Task { await client.togglePauseRecording() }
    }

    func testMicrophone() {
        guard let client else { return }
        stopPlaying()
        Task {
            do { try await client.testMicrophone() } catch { toast?(error.localizedDescription) }
        }
    }

    // MARK: Recordings

    /// row returns a recording a block shows, reading it the first time,
    /// and again while it is missing.
    func row(_ id: String) -> RecordingRow? {
        if asked.insert(id).inserted || missing.contains(id) {
            Task { await load(id) }
        }
        return rows[id]
    }

    /// reload reads again the recordings the blocks show, as the core
    /// says they changed.
    func reload() {
        for id in asked { Task { await load(id) } }
    }

    private func load(_ id: String) async {
        guard let client else { return }
        let n = reads.ask(id)
        do {
            let row = try await client.recording(id)
            guard reads.answered(n, id) else { return }
            if rows[id] != row { rows[id] = row }
            if missing.contains(id) { missing.remove(id) }
        } catch {
            // Its block says so, as the desktop's does; it is read again as
            // the recordings change, or the block shows again.
            guard reads.answered(n, id) else { return }
            rows[id] = nil
            if !missing.contains(id) { missing.insert(id) }
        }
    }

    /// retranscribe transcribes recording id, on page, again, with
    /// provider, or the one marked for the page when it is "".
    func retranscribe(_ id: String, page: String?, provider: String) {
        guard let client else { return }
        Task {
            do {
                try await client.retranscribe(id, provider: provider)
                await load(id)
            } catch {
                toast?(error.localizedDescription)
            }
            if let page { await loadChoices(page) }
        }
    }

    /// transcribeFile transcribes audio file block of page into its notes,
    /// with provider, or the one marked for the page when it is "".
    func transcribeFile(page: String, block: String, provider: String = "") {
        guard let client else { return }
        Task {
            do {
                try await client.transcribeFile(page: page, block: block, provider: provider)
            } catch {
                toast?(error.localizedDescription)
            }
            await loadChoices(page)
        }
    }

    /// shareAudio shares recording id's audio, named for title, through
    /// editor's page: downloaded first when it is not on this device.
    func shareAudio(_ id: String, title: String, in editor: EditorController?) {
        guard let client else { return }
        Task {
            do {
                let audio = try await client.recordingAudio(id)
                editor?.onShare?(try SharedFile(name: MobileRecordingAudioName(title), of: audio))
            } catch {
                toast?(error.localizedDescription)
            }
        }
    }

    /// shareTranscript shares recording id's transcript as a file of format,
    /// named for title, through editor's page.
    func shareTranscript(
        _ id: String, title: String, format: String, in editor: EditorController?
    ) {
        guard let client else { return }
        Task {
            do {
                let file = try await client.recordingTranscriptFile(
                    id, title: title, format: format)
                editor?.onShare?(try SharedFile(name: file.name, text: file.text))
            } catch {
                toast?(error.localizedDescription)
            }
        }
    }

    /// dismissError closes the notice of what went wrong with recording id.
    func dismissError(_ id: String) {
        guard let client else { return }
        Task {
            do {
                try await client.dismissRecordingError(id)
                await load(id)
            } catch {
                toast?(error.localizedDescription)
            }
        }
    }

    /// insertTranscript has editor, its page's, put recording id's
    /// transcript after block, the recording's block it was asked from, as
    /// one step to undo.
    func insertTranscript(_ id: String, after block: String, into editor: EditorController?) {
        guard let client else { return }
        Task {
            do {
                Self.put(try await client.transcriptBlocks(id), after: block, in: editor)
            } catch {
                toast?(error.localizedDescription)
            }
        }
    }

    /// put puts a transcript in editor's page after block, the recording's
    /// block it was asked from, as the desktop does: the block a recording
    /// was made in may be another, as a copied page's has an ID of its own.
    static func put(_ transcript: Transcript, after block: String, in editor: EditorController?) {
        editor?.insertBlocks(transcript.blocks, after: block)
    }

    // MARK: Playing

    /// play plays a recording from where it was, or from at seconds;
    /// playing, it pauses it.
    func play(_ id: String, from at: TimeInterval? = nil) {
        if playing == id, let player {
            // Played to its end, the player let the session go.
            if at != nil || !player.isPlaying {
                try? AVAudioSession.sharedInstance().setActive(true)
            }
            if let at {
                player.currentTime = at
                VideoPlayback.shared.pauseAll()
                FilePlayback.shared.pause()
                player.play()
            } else if player.isPlaying {
                player.pause()
            } else {
                VideoPlayback.shared.pauseAll()
                FilePlayback.shared.pause()
                player.play()
            }
            changes += 1
            return
        }
        guard let client, loading == nil, !state.recording else { return }
        stopPlaying()
        loading = id
        fetch += 1
        let this = fetch
        fetching = Task {
            defer {
                if fetch == this {
                    loading = nil
                    fetching = nil
                }
            }
            do {
                let url = try await client.recordingAudio(id)
                guard !Task.isCancelled, fetch == this else { return }
                let audio = AVAudioSession.sharedInstance()
                try audio.setCategory(.playback, mode: .spokenAudio)
                try audio.setActive(true)
                let p = try AVAudioPlayer(contentsOf: url)
                p.delegate = ended
                p.enableRate = true
                p.rate = PlaySpeed.current
                p.prepareToPlay()
                if let at { p.currentTime = at }
                VideoPlayback.shared.pauseAll()
                FilePlayback.shared.pause()
                p.play()
                player = p
                playing = id
            } catch {
                guard !Task.isCancelled, fetch == this else { return }
                toast?(L.recCouldNotPlay(error: error.localizedDescription))
            }
        }
    }

    /// pausePlaying pauses the recording playing, as a video starts.
    func pausePlaying() {
        guard let player, player.isPlaying else { return }
        player.pause()
        changes += 1
    }

    func seek(_ id: String, to at: TimeInterval) {
        guard playing == id, let player else { return }
        player.currentTime = at
    }

    /// skip moves the recording playing by seconds, within it.
    func skip(_ id: String, by seconds: TimeInterval) {
        guard playing == id, let player else { return }
        player.currentTime = min(max(player.currentTime + seconds, 0), player.duration)
    }

    /// speedChanged plays the recording at the speed now asked for.
    func speedChanged() { player?.rate = PlaySpeed.current }

    /// stopPlaying stops the recording playing, and one on its way.
    func stopPlaying() {
        fetching?.cancel()
        fetching = nil
        fetch += 1
        loading = nil
        let was = player
        player?.stop()
        player = nil
        playing = nil
        if was != nil { letAudioGo() }
    }

    /// letAudioGo gives up the audio session once playing is done, for the
    /// music or podcast it interrupted to go on.
    private func letAudioGo() {
        try? AVAudioSession.sharedInstance().setActive(
            false, options: .notifyOthersOnDeactivation)
    }
}

/// PlayerEnded tells the recorder a recording played to its end, for its
/// block to show Play again.
private final class PlayerEnded: NSObject, AVAudioPlayerDelegate {
    private let done: @MainActor @Sendable () -> Void

    init(_ done: @escaping @MainActor @Sendable () -> Void) { self.done = done }

    func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        Task { @MainActor [done] in done() }
    }
}
