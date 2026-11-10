import Foundation
import LeafletCore
import LeafletSpeech
import Speech
import Synchronization

final class DeviceTranscriber: NSObject, MobileTranscriberProtocol, Sendable {
    static var isAvailable: Bool { SpeechTranscriber.isAvailable }

    static func languages() async -> Set<String> {
        Set(
            await SpeechTranscriber.supportedLocales.compactMap {
                $0.language.languageCode?.identifier
            })
    }

    private let streams = Mutex<[String: LiveTranscription]>([:])

    static let stall = Duration.seconds(180)

    func start(_ id: String?, language: String?, sink: MobileTranscriptSink?) throws {
        let live = LiveTranscription(language: language ?? "auto", sink: SinkRef(sink: sink))
        streams.withLock { $0[id ?? ""] = live }
    }

    func send(_ id: String?, pcm: Data?) {
        guard let pcm, let live = streams.withLock({ $0[id ?? ""] }) else { return }
        live.send(pcm)
    }

    func finish(_ id: String?, error: NSErrorPointer) -> String {
        guard let live = streams.withLock({ $0.removeValue(forKey: id ?? "") }) else {
            return Self.text(Answer.done([]))
        }
        do {
            return Self.text(Answer.done(try Self.wait(live.pulse) { try await live.finish() }))
        } catch {
            live.close()
            return Self.text(Answer.failed(error))
        }
    }

    func close(_ id: String?) {
        streams.withLock { $0.removeValue(forKey: id ?? "") }?.close()
    }

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

    static func wait(
        _ pulse: Pulse, stall: Duration = stall,
        _ body: @escaping @Sendable () async throws -> [SpeechSegment]
    ) throws -> [SpeechSegment] {
        let result = Mutex<Result<[SpeechSegment], any Error>?>(nil)
        let done = DispatchSemaphore(value: 0)
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

private final class LiveTranscription: Sendable {
    private let audio: AsyncStream<Data>.Continuation
    private let task: Task<[SpeechSegment], any Error>
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
                input.finish()
                throw error
            }
        }
    }

    func send(_ pcm: Data) { audio.yield(pcm) }

    func finish() async throws -> [SpeechSegment] {
        audio.finish()
        return try await task.value
    }

    func close() {
        task.cancel()
        audio.finish()
    }
}

final class Pulse: Sendable {
    private let last = Mutex(ContinuousClock.now)

    func beat() { last.withLock { $0 = .now } }

    var still: Duration { ContinuousClock.now - last.withLock { $0 } }
}

private struct SinkRef: @unchecked Sendable {
    let sink: MobileTranscriptSink?

    func text(_ text: String) { sink?.text(text) }
}
