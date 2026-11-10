import AVFoundation
import Foundation
import LeafletCore

@MainActor
@Observable
final class Recorder {
    static let shared = Recorder()

    private(set) var state = RecordingState.idle
    private(set) var at = Date()
    private(set) var levels: [Double] = []
    private(set) var liveText = ""
    private(set) var rows: [String: RecordingRow] = [:]
    private(set) var missing: Set<String> = []
    private(set) var playing: String?
    private(set) var loading: String?
    private(set) var player: AVAudioPlayer?
    private(set) var changes = 0

    private(set) var speech: [String: SpeechChoices] = [:]

    @ObservationIgnored private var client: CoreClient?
    @ObservationIgnored private var attaching: Task<Void, Never>?
    @ObservationIgnored private var speechAsked: Set<String> = []
    @ObservationIgnored private var toast: ((String) -> Void)?
    @ObservationIgnored private var asked: Set<String> = []
    @ObservationIgnored private var reads = Newest()
    @ObservationIgnored private var fetching: Task<Void, Never>?
    @ObservationIgnored private var fetch = 0
    @ObservationIgnored private let microphone = Microphone()
    @ObservationIgnored private let finishing = BackgroundTask()
    @ObservationIgnored private lazy var ended = PlayerEnded { [weak self] in
        self?.changes += 1
        self?.letAudioGo()
    }

    private init() {}

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
            if !state.recording { RecordingActivity.shared.end() }
        }
    }

    func ready() async { await attaching?.value }

    func elapsed(at now: Date) -> TimeInterval {
        let base = TimeInterval(state.elapsedMs) / 1000
        return state.active && !state.paused ? base + now.timeIntervalSince(at) : base
    }

    func update(_ s: RecordingState) {
        if s.liveText != liveText { liveText = s.liveText }
        var s = s
        s.levels = []
        s.liveText = ""
        let old = state
        var same = s
        same.elapsedMs = old.elapsedMs
        guard same != old else { return }
        state = s
        at = Date()
        RecordingActivity.shared.sync(s, at: at)
        holdOn(s)
        if !s.error.isEmpty, s.error != old.error { toast?(s.error) }
        let done = Set(old.transcribing + [old.id]).subtracting(s.transcribing + [s.id])
        for id in done where !id.isEmpty { Task { await load(id) } }
    }

    private func holdOn(_ s: RecordingState) {
        let busy = !s.transcribing.isEmpty || !s.finishing.isEmpty
        if busy, !finishing.running {
            finishing.begin("Transcribe")
        } else if !busy {
            finishing.end()
        }
    }

    func update(levels: [Double]) {
        if levels != self.levels { self.levels = levels }
    }

    func start(pageID: String, editor: EditorController?, provider: String = "") {
        guard let client, !state.recording else { return }
        quiet()
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

    func choices(for page: String) -> SpeechChoices? {
        if speechAsked.insert(page).inserted { Task { await loadChoices(page) } }
        return speech[page]
    }

    func reloadChoices() {
        for page in speechAsked { Task { await loadChoices(page) } }
    }

    private func loadChoices(_ page: String) async {
        guard let client, let c = try? await client.speechChoices(page) else { return }
        if speech[page] != c { speech[page] = c }
    }

    func stop() {
        Task { await stopRecording() }
    }

    func stopRecording() async {
        guard let client else { return }
        if !finishing.running { finishing.begin("Transcribe") }
        await client.stopRecording()
        if let s = try? await client.recordingState() {
            holdOn(s)
        } else {
            finishing.end()
        }
    }

    func togglePause() {
        guard let client else { return }
        Task { await client.togglePauseRecording() }
    }

    func testMicrophone() {
        guard let client else { return }
        quiet()
        Task {
            do { try await client.testMicrophone() } catch { toast?(error.localizedDescription) }
        }
    }

    func row(_ id: String) -> RecordingRow? {
        if asked.insert(id).inserted || missing.contains(id) {
            Task { await load(id) }
        }
        return rows[id]
    }

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
            guard reads.answered(n, id) else { return }
            rows[id] = nil
            if !missing.contains(id) { missing.insert(id) }
        }
    }

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

    static func put(_ transcript: Transcript, after block: String, in editor: EditorController?) {
        editor?.insertBlocks(transcript.blocks, after: block)
    }

    func play(_ id: String, from at: TimeInterval? = nil) {
        if playing == id, let player {
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

    func pausePlaying() {
        guard let player, player.isPlaying else { return }
        player.pause()
        changes += 1
    }

    func seek(_ id: String, to at: TimeInterval) {
        guard playing == id, let player else { return }
        player.currentTime = at
    }

    func skip(_ id: String, by seconds: TimeInterval) {
        guard playing == id, let player else { return }
        player.currentTime = min(max(player.currentTime + seconds, 0), player.duration)
    }

    func speedChanged() { player?.rate = PlaySpeed.current }

    func quiet() {
        stopPlaying()
        FilePlayback.shared.pause()
        VideoPlayback.shared.pauseAll()
    }

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

    private func letAudioGo() {
        try? AVAudioSession.sharedInstance().setActive(
            false, options: .notifyOthersOnDeactivation)
    }
}

private final class PlayerEnded: NSObject, AVAudioPlayerDelegate {
    private let done: @MainActor @Sendable () -> Void

    init(_ done: @escaping @MainActor @Sendable () -> Void) { self.done = done }

    func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        Task { @MainActor [done] in done() }
    }
}
