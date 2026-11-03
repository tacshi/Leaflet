import Foundation
import LeafletCore
import LeafletSpeech
import Speech
import Synchronization

/// DeviceTranscriber is iOS's own speech recognition as the core's
/// on-device engine: SpeechTranscriber, which runs on the device, offline,
/// once its language's model is downloaded (LeafletSpeech, which the Mac's
/// helper is built with too). The core calls it from its own threads; what
/// waits for the recognizer waits on them, never on the main thread. It
/// answers as the Mac's helper does (Answer), so that the core says why it
/// could not transcribe in the user's language, from its code.
final class DeviceTranscriber: NSObject, MobileTranscriberProtocol, Sendable {
    /// isAvailable says the device can transcribe by itself.
    static var isAvailable: Bool { SpeechTranscriber.isAvailable }

    /// languages are the languages the device can transcribe, by their
    /// ISO 639 codes, as the core names them.
    static func languages() async -> Set<String> {
        Set(
            await SpeechTranscriber.supportedLocales.compactMap {
                $0.language.languageCode?.identifier
            })
    }

    private let streams = Mutex<[String: LiveTranscription]>([:])

    /// stall is how long a transcription may go without going on, its
    /// model downloading or its words coming, before it is given up: the
    /// core's thread waiting for it would wait for good, and the block say
    /// "Adding timestamps…" until the app quits.
    static let stall = Duration.seconds(180)

    func start(_ id: String?, language: String?, sink: MobileTranscriptSink?) throws {
        let live = LiveTranscription(language: language ?? "auto", sink: SinkRef(sink: sink))
        streams.withLock { $0[id ?? ""] = live }
    }

    func send(_ id: String?, pcm: Data?) {
        guard let pcm, let live = streams.withLock({ $0[id ?? ""] }) else { return }
        live.send(pcm)
    }

    /// finish waits for the words of the audio sent to stream id, and
    /// answers with them, or why it could not transcribe them.
    func finish(_ id: String?, error: NSErrorPointer) -> String {
        guard let live = streams.withLock({ $0.removeValue(forKey: id ?? "") }) else {
            return Self.text(Answer.done([]))
        }
        do {
            return Self.text(Answer.done(try Self.wait(live.pulse) { try await live.finish() }))
        } catch {
            // Failed, or given up as stuck, it goes on no more.
            live.close()
            return Self.text(Answer.failed(error))
        }
    }

    func close(_ id: String?) {
        streams.withLock { $0.removeValue(forKey: id ?? "") }?.close()
    }

    /// file transcribes the WAV file at path, and answers as finish does.
    func file(_ path: String?, language: String?, error: NSErrorPointer) -> String {
        let path = path ?? ""
        let language = language ?? "auto"
        let pulse = Pulse()
        do {
            return Self.text(
                Answer.done(
                    try Self.wait(pulse) {
                        try await Transcription.file(
                            path, language: language, beat: { pulse.beat() })
                    }))
        } catch {
            return Self.text(Answer.failed(error))
        }
    }

    private static func text(_ answer: Data) -> String { String(decoding: answer, as: UTF8.self) }

    /// wait waits for body, on a thread of the core's that may, as body
    /// beats pulse as it goes on, and gives it up once it has not for
    /// stall, failing as stuck.
    static func wait(
        _ pulse: Pulse, stall: Duration = stall,
        _ body: @escaping @Sendable () async throws -> [SpeechSegment]
    ) throws -> [SpeechSegment] {
        let result = Mutex<Result<[SpeechSegment], any Error>?>(nil)
        let done = DispatchSemaphore(value: 0)
        // From now: a live transcription heard nothing while the recording
        // was silent.
        pulse.beat()
        let task = Task.detached {
            do {
                let segments = try await body()
                result.withLock { $0 = .success(segments) }
            } catch {
                result.withLock { $0 = .failure(error) }
            }
            done.signal()
        }
        while done.wait(timeout: .now() + 1) == .timedOut {
            guard pulse.still < stall else {
                task.cancel()
                throw SpeechFailure.stuck
            }
        }
        return try result.withLock { $0! }.get()
    }
}

/// LiveTranscription is one recording transcribed as it records. Audio
/// sent while the model downloads waits for it, in order. Once the
/// transcription fails, as with no model for the language, the audio goes
/// nowhere: kept for nothing, it would fill the memory over an hour.
private final class LiveTranscription: Sendable {
    private let audio: AsyncStream<Data>.Continuation
    private let task: Task<[SpeechSegment], any Error>
    /// pulse beats as the transcription goes on, which finish waits by.
    let pulse = Pulse()

    init(language: String, sink: SinkRef) {
        let (audio, input) = AsyncStream.makeStream(of: Data.self)
        self.audio = input
        let pulse = pulse
        task = Task.detached {
            do {
                return try await Transcription.stream(
                    audio, language: language, text: { sink.text($0) }, beat: { pulse.beat() })
            } catch {
                // The audio waiting goes, and what comes after isn't kept.
                input.finish()
                throw error
            }
        }
    }

    func send(_ pcm: Data) { audio.yield(pcm) }

    /// finish waits for the words of the audio sent.
    func finish() async throws -> [SpeechSegment] {
        audio.finish()
        return try await task.value
    }

    func close() {
        task.cancel()
        audio.finish()
    }
}

/// Pulse is when a transcription last went on, as its model downloads or
/// its words come, from any thread.
final class Pulse: Sendable {
    private let last = Mutex(ContinuousClock.now)

    func beat() { last.withLock { $0 = .now } }

    /// still is how long since it last went on.
    var still: Duration { ContinuousClock.now - last.withLock { $0 } }
}

/// SinkRef passes the words so far to the core, from any thread.
private struct SinkRef: @unchecked Sendable {
    let sink: MobileTranscriptSink?

    func text(_ text: String) { sink?.text(text) }
}
