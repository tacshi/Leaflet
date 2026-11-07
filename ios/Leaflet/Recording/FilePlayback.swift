import AVFoundation
import AVKit
import LeafletCore
import SwiftUI

/// PlayerSettings are how audio plays, kept for next time. Views watch them
/// here: AppStorage misses a change to a key with a dot in it made through
/// UserDefaults.
@MainActor
@Observable
final class PlayerSettings {
    static let shared = PlayerSettings()
    /// speed is how fast audio plays, files' and recordings' alike.
    private(set) var speed: Double
    /// following says transcripts follow the audio.
    private(set) var following: Bool

    private init() {
        let defaults = UserDefaults.standard
        let stored = defaults.double(forKey: PlaySpeed.key)
        speed = PlaySpeed.all.contains(stored) ? stored : 1
        following = defaults.bool(forKey: LineClock.followKey)
    }

    func setSpeed(_ speed: Double) {
        self.speed = speed
        UserDefaults.standard.set(speed, forKey: PlaySpeed.key)
    }

    func toggleFollowing() {
        following.toggle()
        UserDefaults.standard.set(following, forKey: LineClock.followKey)
    }
}

/// PlaySpeed is the speeds audio plays at, a stored one that isn't offered
/// reading as normal, and how far a skip goes.
@MainActor
enum PlaySpeed {
    static let key = "player.speed"
    static let all: [Double] = [0.75, 1, 1.25, 1.5, 1.75, 2]
    static let skip: Double = 15

    /// current is the speed to play at.
    static var current: Float { Float(PlayerSettings.shared.speed) }

    /// label is a speed as its button says it: 1.5×.
    static func label(_ speed: Double) -> String { String(format: "%g×", speed) }

    /// set has audio play at speed, what plays now and what plays next.
    static func set(_ speed: Double) {
        PlayerSettings.shared.setSpeed(speed)
        FilePlayback.shared.speedChanged()
        Recorder.shared.speedChanged()
    }
}

/// SkipButton goes seconds on or, below 0, back.
struct SkipButton: View {
    let seconds: Double
    let run: () -> Void

    var body: some View {
        Button(action: run) {
            Image(systemName: seconds < 0 ? "gobackward.15" : "goforward.15")
                .font(.title3)
                .frame(width: 32, height: 32)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(BlockLook.palette.icon)
        .accessibilityLabel(seconds < 0 ? L.cardSkipBack : L.cardSkipForward)
    }
}

/// SpeedMenu says how fast audio plays, and changes it.
struct SpeedMenu: View {
    var body: some View {
        let settings = PlayerSettings.shared
        Menu {
            Picker(
                L.cardSpeed,
                selection: Binding(get: { settings.speed }, set: { PlaySpeed.set($0) })
            ) {
                ForEach(PlaySpeed.all, id: \.self) { speed in
                    Text(verbatim: PlaySpeed.label(speed)).tag(speed)
                }
            }
            .pickerStyle(.inline)
            .labelsHidden()
        } label: {
            Text(verbatim: PlaySpeed.label(settings.speed))
                .font(.caption.weight(.semibold).monospacedDigit())
                .frame(minWidth: 32, minHeight: 32)
                .contentShape(.rect)
        }
        .foregroundStyle(BlockLook.palette.icon)
        .accessibilityLabel(L.cardSpeed)
    }
}

/// FilePlayback plays the audio and video files of a page, in their blocks:
/// one at a time, as one person listens to one, so one starting here pauses
/// the videos and the recording playing, and they pause it.
@MainActor
@Observable
final class FilePlayback {
    static let shared = FilePlayback()
    /// block is the file block playing, or paused where it was.
    private(set) var block: String?
    /// player plays block's file.
    private(set) var player: AVPlayer?
    /// loading is the block whose file is on its way.
    private(set) var loading: String?
    /// playing says the player plays, paused or at its end otherwise.
    private(set) var playing = false
    /// cover is the picture in the file, if it has one.
    private(set) var cover: UIImage?
    /// failed is a block whose file the player can't read.
    private(set) var failed: Set<String> = []
    @ObservationIgnored private var watching: NSKeyValueObservation?

    private init() {}

    /// play starts file fileID of block from at seconds in, or from where
    /// it was; playing with no time asked, it pauses it. A file that isn't
    /// here comes first.
    func play(block id: String, file fileID: String, at: Int? = nil, client: CoreClient?) {
        if block == id, let player {
            if let at { player.seek(to: CMTime(seconds: Double(at), preferredTimescale: 600)) }
            if playing && at == nil {
                player.pause()
            } else {
                resume(player)
            }
            return
        }
        guard let client, loading == nil else { return }
        stop()
        loading = id
        Task {
            defer { loading = nil }
            do {
                let url = URL(fileURLWithPath: try await client.localFile(fileID))
                let asset = AVURLAsset(url: url)
                guard try await asset.load(.isPlayable) else {
                    failed.insert(id)
                    return
                }
                let picture = await Self.cover(of: asset)
                let session = AVAudioSession.sharedInstance()
                try session.setCategory(.playback, mode: .spokenAudio)
                try session.setActive(true)
                let item = AVPlayerItem(asset: asset)
                let made = AVPlayer(playerItem: item)
                made.defaultRate = PlaySpeed.current
                if let at {
                    await made.seek(to: CMTime(seconds: Double(at), preferredTimescale: 600))
                }
                watching = made.observe(\.timeControlStatus) { [weak self] player, _ in
                    let on = player.timeControlStatus != .paused
                    Task { @MainActor in self?.playing = on }
                }
                player = made
                cover = picture
                block = id
                resume(made)
            } catch {
                failed.insert(id)
            }
        }
    }

    /// cover is the picture asset holds: an album's or an episode's.
    private static func cover(of asset: AVURLAsset) async -> UIImage? {
        guard let items = try? await asset.load(.commonMetadata),
            let art = AVMetadataItem.metadataItems(
                from: items, filteredByIdentifier: .commonIdentifierArtwork
            ).first,
            let data = try? await art.load(.dataValue)
        else { return nil }
        return UIImage(data: data)
    }

    private func resume(_ player: AVPlayer) {
        VideoPlayback.shared.pauseAll()
        Recorder.shared.pausePlaying()
        player.play()
    }

    /// toggle plays the file there is, or pauses it.
    func toggle() {
        guard let player else { return }
        if playing { player.pause() } else { resume(player) }
    }

    /// pause pauses the file playing, as something else starts.
    func pause() { player?.pause() }

    /// seek takes the file to at seconds in, playing or not as it was.
    func seek(to at: Double) {
        player?.seek(to: CMTime(seconds: at, preferredTimescale: 600))
    }

    /// skip moves the file by seconds, within it.
    func skip(by seconds: Double) {
        guard let player else { return }
        let now = player.currentTime().seconds
        guard now.isFinite else { return }
        let length = player.currentItem?.duration.seconds ?? .infinity
        seek(to: min(max(now + seconds, 0), length.isFinite ? length : .infinity))
    }

    /// speedChanged plays the file at the speed now asked for.
    func speedChanged() {
        player?.defaultRate = PlaySpeed.current
        if playing { player?.rate = PlaySpeed.current }
    }

    /// stop lets the file playing go, as its page closes.
    func stop() {
        player?.pause()
        watching = nil
        player = nil
        cover = nil
        block = nil
        playing = false
    }

    /// stop lets the file playing go if it is of blocks.
    func stop(_ blocks: Set<String>) {
        if let block, blocks.contains(block) { stop() }
        failed.subtract(blocks)
    }
}

/// NowPlayingBar is a small player over the top of the page, for audio
/// whose own has scrolled away: a transcript is read below it, and a block
/// may still show when its player no longer does. A video has none, as it
/// has picture in picture, but shows in the system's media controls from
/// here as audio does.
struct NowPlayingBar: View {
    /// editor is the page's, whose audio it plays.
    weak var editor: EditorController?
    /// Playing is what the bar is for: a file's block, or a recording's.
    private enum Playing: Equatable {
        case file(String)
        case recording(String)
    }

    @State private var block: String?
    @State private var title = ""
    @State private var away = false
    @State private var at: Double = 0
    @State private var length: Double = 0
    @State private var dragging = false
    @State private var on = false
    private var following: Bool { PlayerSettings.shared.following }
    /// horizontalSizeClass leaves a narrow screen the controls alone.
    @Environment(\.horizontalSizeClass) private var width

    /// playing is the audio that started last: the one that plays, or the
    /// one left paused.
    private var playing: Playing? {
        let (files, recorder) = (FilePlayback.shared, Recorder.shared)
        _ = recorder.changes
        let recording = recorder.playing.map(Playing.recording)
        let file = files.block.map(Playing.file)
        if files.playing { return file }
        if recorder.player?.isPlaying == true { return recording }
        return file ?? recording
    }

    var body: some View {
        let palette = BlockLook.palette
        let wide = width == .regular
        Group {
            if away, let playing {
                HStack(spacing: 10) {
                    Button {
                        toggle(playing)
                    } label: {
                        Image(systemName: on ? "pause.fill" : "play.fill")
                            .frame(width: 32, height: 32)
                            .contentShape(.rect)
                    }
                    .accessibilityLabel(on ? L.commonPause : L.recPlay)
                    if wide {
                        Button {
                            if let block { editor?.reveal(blockID: block) }
                        } label: {
                            Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                                .frame(maxWidth: 180, alignment: .leading)
                        }
                        .accessibilityLabel(L.cardGoToPlayer(name: title))
                    }
                    SkipButton(seconds: -PlaySpeed.skip) { skip(playing, by: -PlaySpeed.skip) }
                    SkipButton(seconds: PlaySpeed.skip) { skip(playing, by: PlaySpeed.skip) }
                    if wide {
                        Text(verbatim: MobileClock(Int(at))).font(.caption.monospacedDigit())
                    }
                    Slider(
                        value: $at, in: 0...max(length, 1),
                        onEditingChanged: { editing in
                            dragging = editing
                            if !editing { seek(playing) }
                        }
                    )
                    .tint(palette.accent)
                    if wide {
                        Text(verbatim: MobileClock(Int(length))).font(.caption.monospacedDigit())
                    }
                    SpeedMenu()
                    if wide {
                        Button {
                            LineClock.toggleFollow()
                        } label: {
                            Image(systemName: "text.line.first.and.arrowtriangle.forward")
                                .font(.caption)
                                .foregroundStyle(following ? palette.accent : palette.text)
                                .frame(width: 32, height: 32)
                                .contentShape(.rect)
                        }
                        .accessibilityLabel(L.cardFollowAlong)
                    }
                    Button {
                        stop(playing)
                    } label: {
                        Image(systemName: "xmark").font(.caption)
                            .frame(width: 32, height: 32)
                            .contentShape(.rect)
                    }
                    .accessibilityLabel(L.commonClose)
                }
                .buttonStyle(.plain)
                .foregroundStyle(palette.text)
                .padding(.horizontal, 12).padding(.vertical, 6)
                .frame(maxWidth: 760)
                .glassEffect(.regular, in: .capsule)
                .padding(.horizontal, 16).padding(.top, 8)
            }
        }
        .task(id: playing) {
            away = false
            guard let playing else {
                NowPlaying.shared.clear()
                return
            }
            guard let found = find(playing) else { return }
            (block, title) = (found.id, found.title)
            NowPlaying.shared.show(
                block: found.id, title: found.title, cover: Self.cover(playing),
                controls: Self.controls(playing))
            while !Task.isCancelled {
                away = found.audio && !(editor?.showsHead(ofBlock: found.id) ?? true)
                read(playing)
                NowPlaying.shared.sync(at: at, length: length, on: on)
                try? await Task.sleep(for: .milliseconds(250))
            }
        }
        .onDisappear {
            if let block { NowPlaying.shared.clear(block: block) }
        }
    }

    /// cover is what plays's own picture.
    private static func cover(_ playing: Playing) -> UIImage? {
        if case .file = playing { return FilePlayback.shared.cover }
        return nil
    }

    /// controls are what the system's media controls do to what plays.
    private static func controls(_ playing: Playing) -> NowPlaying.Controls {
        switch playing {
        case .file:
            let files = FilePlayback.shared
            return NowPlaying.Controls(
                isPlaying: { files.playing }, toggle: { files.toggle() },
                seek: { files.seek(to: $0) }, skip: { files.skip(by: $0) })
        case .recording(let id):
            let recorder = Recorder.shared
            return NowPlaying.Controls(
                isPlaying: { recorder.player?.isPlaying == true }, toggle: { recorder.play(id) },
                seek: { recorder.seek(id, to: $0) }, skip: { recorder.skip(id, by: $0) })
        }
    }

    /// find is the block of what plays, in this page, its name, and whether
    /// it is audio; nil for what plays in another page.
    private func find(_ playing: Playing) -> (id: String, title: String, audio: Bool)? {
        switch playing {
        case .file(let id):
            guard let node = editor?.node(ofBlock: id) else { return nil }
            return (id, node.data?["name"]?.string ?? L.cardFile, FileBlock.kind(node) == "audio")
        case .recording(let id):
            guard let node = editor?.recordingBlock(id) else { return nil }
            let title = node.data?["title"]?.string ?? ""
            return (node.id, title.isEmpty ? L.commonRecording : title, true)
        }
    }

    private func read(_ playing: Playing) {
        switch playing {
        case .file:
            let files = FilePlayback.shared
            on = files.playing
            if !dragging, let time = files.player?.currentTime().seconds, time.isFinite {
                at = time
            }
            if let d = files.player?.currentItem?.duration.seconds, d.isFinite { length = d }
        case .recording:
            guard let player = Recorder.shared.player else { return }
            on = player.isPlaying
            if !dragging { at = player.currentTime }
            length = player.duration
        }
    }

    private func toggle(_ playing: Playing) {
        switch playing {
        case .file: FilePlayback.shared.toggle()
        case .recording(let id): Recorder.shared.play(id)
        }
    }

    private func seek(_ playing: Playing) {
        switch playing {
        case .file: FilePlayback.shared.seek(to: at)
        case .recording(let id): Recorder.shared.seek(id, to: at)
        }
    }

    private func skip(_ playing: Playing, by seconds: Double) {
        switch playing {
        case .file: FilePlayback.shared.skip(by: seconds)
        case .recording(let id): Recorder.shared.skip(id, by: seconds)
        }
    }

    private func stop(_ playing: Playing) {
        switch playing {
        case .file: FilePlayback.shared.stop()
        case .recording: Recorder.shared.stopPlaying()
        }
        away = false
    }
}

/// FileBar is the bar of a file playing: where it has got to, to drag.
struct FileBar: View {
    let player: AVPlayer
    @State private var at: Double = 0
    @State private var length: Double = 0
    @State private var dragging = false

    var body: some View {
        let palette = BlockLook.palette
        HStack(spacing: 8) {
            SkipButton(seconds: -PlaySpeed.skip) { FilePlayback.shared.skip(by: -PlaySpeed.skip) }
            SkipButton(seconds: PlaySpeed.skip) { FilePlayback.shared.skip(by: PlaySpeed.skip) }
            Text(verbatim: MobileClock(Int(at))).monospacedDigit()
            Slider(
                value: $at, in: 0...max(length, 1),
                onEditingChanged: { editing in
                    dragging = editing
                    if !editing {
                        player.seek(to: CMTime(seconds: at, preferredTimescale: 600))
                    }
                }
            )
            .tint(palette.accent)
            Text(verbatim: MobileClock(Int(length))).monospacedDigit()
            SpeedMenu()
        }
        .font(.caption)
        .foregroundStyle(palette.textMuted)
        .task(id: ObjectIdentifier(player)) {
            while !Task.isCancelled {
                if !dragging { at = max(player.currentTime().seconds, 0) }
                if let d = player.currentItem?.duration.seconds, d.isFinite { length = d }
                try? await Task.sleep(for: .milliseconds(250))
            }
        }
    }
}

/// FileVideo shows a video file's picture, with the system's controls. What
/// it plays shows in the media controls from NowPlaying, with its name.
struct FileVideo: UIViewControllerRepresentable {
    let player: AVPlayer

    func makeUIViewController(context: Context) -> AVPlayerViewController {
        let view = AVPlayerViewController()
        view.updatesNowPlayingInfoCenter = false
        view.player = player
        return view
    }

    func updateUIViewController(_ view: AVPlayerViewController, context: Context) {
        view.player = player
    }
}
