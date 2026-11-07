import MediaPlayer
import UIKit

/// NowPlaying shows the audio of a page where the system shows media: Control
/// Center and the lock screen. Those, and the keyboard's and headphones' media
/// keys, then play, pause, skip, seek and change its speed. A video's own
/// player does this for it.
@MainActor
final class NowPlaying {
    /// Controls are what the system can do to the audio.
    struct Controls {
        let isPlaying: () -> Bool
        let toggle: () -> Void
        let seek: (Double) -> Void
        let skip: (Double) -> Void
    }

    /// Shown is what the system was last told, which it goes on from.
    private struct Shown {
        let at: Double
        let length: Double
        let on: Bool
        let rate: Float
        let when: Date
    }

    static let shared = NowPlaying()
    /// lost is how far the audio can be from where the system thinks before
    /// it is told again.
    private static let lost = 1.5

    private var block: String?
    private var title = ""
    private var controls: Controls?
    /// artwork is the picture beside the title.
    private var artwork: MPMediaItemArtwork?
    private var shown: Shown?
    private var listening = false

    private init() {}

    /// show says block's audio, named title, is what plays, beside its cover
    /// if it has one, and the app's icon if not.
    func show(block: String, title: String, cover: UIImage?, controls: Controls) {
        if !listening {
            listening = true
            Self.listen()
        }
        self.block = block
        self.title = title
        self.controls = controls
        artwork = (cover ?? UIImage(named: "NowPlayingCover")).map(Self.artwork)
        shown = nil
    }

    /// sync tells the system where the audio is, when it would be wrong: it
    /// counts on by itself while the audio plays.
    func sync(at: Double, length: Double, on: Bool) {
        guard block != nil else { return }
        let rate = PlaySpeed.current
        if let shown, shown.on == on, shown.length == length, shown.rate == rate {
            let ran = on ? Date().timeIntervalSince(shown.when) * Double(rate) : 0
            if abs(shown.at + ran - at) < Self.lost { return }
        }
        shown = Shown(at: at, length: length, on: on, rate: rate, when: Date())
        var info: [String: Any] = [
            MPMediaItemPropertyTitle: title,
            MPMediaItemPropertyArtist: "Leaflet",
            MPMediaItemPropertyPlaybackDuration: length,
            MPNowPlayingInfoPropertyElapsedPlaybackTime: at,
            MPNowPlayingInfoPropertyPlaybackRate: on ? rate : 0,
            MPNowPlayingInfoPropertyDefaultPlaybackRate: rate,
        ]
        if let artwork { info[MPMediaItemPropertyArtwork] = artwork }
        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
    }

    /// clear says nothing plays, or block's audio no longer does.
    func clear(block id: String? = nil) {
        guard block != nil, id == nil || id == block else { return }
        block = nil
        controls = nil
        artwork = nil
        shown = nil
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
    }

    private func play() {
        if let controls, !controls.isPlaying() { controls.toggle() }
    }

    private func pause() {
        if let controls, controls.isPlaying() { controls.toggle() }
    }

    private func toggle() { controls?.toggle() }
    private func seek(_ at: Double) { controls?.seek(at) }
    private func skip(_ seconds: Double) { controls?.skip(seconds) }

    /// listen hears the system's commands, which may not come on the main
    /// thread.
    private nonisolated static func listen() {
        let center = MPRemoteCommandCenter.shared()
        center.playCommand.addTarget { _ in
            Task { @MainActor in NowPlaying.shared.play() }
            return .success
        }
        center.pauseCommand.addTarget { _ in
            Task { @MainActor in NowPlaying.shared.pause() }
            return .success
        }
        center.togglePlayPauseCommand.addTarget { _ in
            Task { @MainActor in NowPlaying.shared.toggle() }
            return .success
        }
        let skip = 15.0
        center.skipForwardCommand.preferredIntervals = [NSNumber(value: skip)]
        center.skipForwardCommand.addTarget { _ in
            Task { @MainActor in NowPlaying.shared.skip(skip) }
            return .success
        }
        center.skipBackwardCommand.preferredIntervals = [NSNumber(value: skip)]
        center.skipBackwardCommand.addTarget { _ in
            Task { @MainActor in NowPlaying.shared.skip(-skip) }
            return .success
        }
        center.changePlaybackPositionCommand.addTarget { event in
            guard let event = event as? MPChangePlaybackPositionCommandEvent else {
                return .commandFailed
            }
            let at = event.positionTime
            Task { @MainActor in NowPlaying.shared.seek(at) }
            return .success
        }
        center.changePlaybackRateCommand.supportedPlaybackRates =
            [0.75, 1, 1.25, 1.5, 1.75, 2].map { NSNumber(value: $0) }
        center.changePlaybackRateCommand.addTarget { event in
            guard let event = event as? MPChangePlaybackRateCommandEvent else {
                return .commandFailed
            }
            let rate = Double(event.playbackRate)
            Task { @MainActor in PlaySpeed.set(rate) }
            return .success
        }
    }

    /// artwork is image as the system shows it. The system asks for it away
    /// from the main thread, so it isn't made on the main actor.
    private nonisolated static func artwork(_ image: UIImage) -> MPMediaItemArtwork {
        MPMediaItemArtwork(boundsSize: image.size) { _ in image }
    }
}
