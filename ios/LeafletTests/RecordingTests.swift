import AVFoundation
import Foundation
import LeafletSpeech
import MediaPlayer
import Testing

@testable import Leaflet

struct RecordingTests {
    @Test func theAppHoldsOnWhileAFileIsTranscribed() {
        #expect(!RecordingState().transcribes)
        #expect(RecordingState(transcribingFiles: ["file"]).transcribes)
        #expect(RecordingState(transcribing: ["recording"]).transcribes)
        #expect(RecordingState(finishing: ["recording"]).transcribes)
    }

    @Test func theAudioSessionGoesOnlyOnceNothingHasIt() {
        #expect(AudioSession.idle(recording: false, file: false, microphone: false))
        #expect(!AudioSession.idle(recording: false, file: true, microphone: false))
        #expect(!AudioSession.idle(recording: true, file: false, microphone: false))
        #expect(!AudioSession.idle(recording: false, file: false, microphone: true))
    }

    @Test func aPausedRecordingsActivityStillGoesStale() {
        let now = Date()
        #expect(RecordingActivity.staleDate(from: now) == now.addingTimeInterval(120))
    }

    @Test func timeTheAppWasAwayIsNoStall() async throws {
        // Every wait of a second reads here as one the app spent suspended: a transcription
        // that goes on through them is not given up, though it never beats.
        let segments = try await Task.detached {
            try DeviceTranscriber.wait(
                Pulse(), stall: .milliseconds(1500), away: .milliseconds(900)
            ) {
                try await Task.sleep(for: .seconds(4))
                return [SpeechSegment(startMs: 0, endMs: 1000, text: "Hello")]
            }
        }.value
        #expect(segments.map(\.text) == ["Hello"])
    }

    @MainActor @Test func aWindowLeavesTheLockScreenToTheOneThatPlays() {
        let shown = NowPlaying.shared
        let center = MPNowPlayingInfoCenter.default()
        let controls = NowPlaying.Controls(
            isPlaying: { false }, toggle: {}, seek: { _ in }, skip: { _ in })
        shown.show(player: "a/talk", title: "Talk", cover: nil, controls: controls)
        shown.sync(player: "a/talk", at: 1, length: 60, on: false)
        shown.sync(player: "b/talk", at: 2, length: 90, on: false)
        shown.clear(player: "b/talk")
        #expect(center.nowPlayingInfo?[MPMediaItemPropertyTitle] as? String == "Talk")
        #expect(center.nowPlayingInfo?[MPMediaItemPropertyPlaybackDuration] as? Double == 60)
        shown.clear(player: "a/talk")
        #expect(center.nowPlayingInfo == nil)
    }

    @Test func aRecordingPausesAsTheHeadphonesGo() {
        let headphones = AVAudioSession.Port.headphones.rawValue
        let bluetooth = AVAudioSession.Port.bluetoothA2DP.rawValue
        let speaker = AVAudioSession.Port.builtInSpeaker.rawValue
        #expect(Recorder.goesOutLoud(.oldDeviceUnavailable, from: [headphones]))
        #expect(Recorder.goesOutLoud(.oldDeviceUnavailable, from: [bluetooth]))
        #expect(!Recorder.goesOutLoud(.oldDeviceUnavailable, from: [speaker]))
        #expect(!Recorder.goesOutLoud(.newDeviceAvailable, from: [speaker]))
        #expect(!Recorder.goesOutLoud(.categoryChange, from: [headphones]))
    }
}
