import AVFoundation
import LeafletCore
import Speech
import Synchronization

/// DeviceTranscriber is iOS's own speech recognition as the core's
/// on-device engine: SpeechTranscriber, which runs on the device, offline,
/// once its language's model is downloaded. The core calls it from its own
/// threads; what waits for the recognizer waits on them, never on the
/// main thread.
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

    func finish(_ id: String?, error: NSErrorPointer) -> String {
        guard let live = streams.withLock({ $0.removeValue(forKey: id ?? "") }) else {
            return "[]"
        }
        let json = Self.answer(error, live.pulse) { try await live.finish() }
        // Given up as stuck, it goes on no more.
        if json.isEmpty { live.close() }
        return json
    }

    func close(_ id: String?) {
        streams.withLock { $0.removeValue(forKey: id ?? "") }?.close()
    }

    func file(_ path: String?, language: String?, error: NSErrorPointer) -> String {
        let url = URL(fileURLWithPath: path ?? "")
        let language = language ?? "auto"
        let pulse = Pulse()
        return Self.answer(error, pulse) {
            let transcriber = try await Self.transcriber(for: language, pulse: pulse)
            let audio = try AVAudioFile(forReading: url)
            let analyzer = SpeechAnalyzer(modules: [transcriber])
            let collect = Task { try await Self.collect(transcriber, sink: nil, pulse: pulse) }
            try await analyzer.start(inputAudioFile: audio, finishAfterFile: true)
            return try await collect.value
        }
    }

    /// answer runs body and waits for it, on a thread of the core's that
    /// may, returning its transcript as JSON, or "" with why it failed in
    /// error, as gomobile's methods do.
    private static func answer(
        _ error: NSErrorPointer, _ pulse: Pulse,
        _ body: @escaping @Sendable () async throws -> [Segment]
    ) -> String {
        do {
            return try wait(pulse, body)
        } catch let e {
            error?.pointee = e as NSError
            return ""
        }
    }

    /// wait waits for body, which beats pulse as it goes on, and gives it
    /// up once it has not for stall, saying so.
    static func wait(
        _ pulse: Pulse, stall: Duration = stall,
        _ body: @escaping @Sendable () async throws -> [Segment]
    ) throws -> String {
        let result = Mutex<Result<[Segment], any Error>?>(nil)
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
                throw TranscriptionError(message: L.recErrOnDeviceStuck)
            }
        }
        let segments = try result.withLock { $0! }.get()
        return String(decoding: try JSONEncoder().encode(segments), as: UTF8.self)
    }

    /// transcriber is a transcriber of language ("auto" for the device's),
    /// its model downloaded first if this is its first time, which beats
    /// pulse as it goes.
    fileprivate static func transcriber(for language: String, pulse: Pulse) async throws
        -> SpeechTranscriber
    {
        let locale = try await locale(for: language)
        let transcriber = SpeechTranscriber(
            locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults],
            attributeOptions: [.audioTimeRange])
        if !(await AssetInventory.reservedLocales).contains(locale) {
            _ = try? await AssetInventory.reserve(locale: locale)
        }
        if let request = try await AssetInventory.assetInstallationRequest(
            supporting: [transcriber])
        {
            let watching = request.progress.observe(\.completedUnitCount) { _, _ in pulse.beat() }
            defer { watching.invalidate() }
            try await request.downloadAndInstall()
        }
        return transcriber
    }

    /// locale is the language's locale the device can transcribe: for
    /// "auto", the device's, or the first of the user's languages it can.
    private static func locale(for language: String) async throws -> Locale {
        let wanted =
            language == "auto" || language.isEmpty
            ? [Locale.current] + Locale.preferredLanguages.map(Locale.init(identifier:))
            : [Locale(identifier: language)]
        for l in wanted {
            if let supported = await SpeechTranscriber.supportedLocale(equivalentTo: l) {
                return supported
            }
        }
        // Named in the language the app shows, as the message around it.
        let name =
            Locale(identifier: L.language).localizedString(forIdentifier: wanted[0].identifier)
            ?? wanted[0].identifier
        throw TranscriptionError(message: L.recErrOnDeviceLanguage(language: name))
    }

    /// collect gathers a transcriber's final results as the transcript, a
    /// segment each, telling sink the words so far as they come, and
    /// beating pulse.
    fileprivate static func collect(
        _ transcriber: SpeechTranscriber, sink: SinkRef?, pulse: Pulse
    ) async throws -> [Segment] {
        var segments: [Segment] = []
        var final = ""
        for try await result in transcriber.results {
            pulse.beat()
            let text = String(result.text.characters)
            if result.isFinal {
                let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
                if !trimmed.isEmpty {
                    segments.append(
                        Segment(
                            startMs: Int(result.range.start.seconds * 1000),
                            endMs: Int(result.range.end.seconds * 1000), text: trimmed))
                }
                final += text
                sink?.text(final)
            } else {
                sink?.text(final + text)
            }
        }
        return segments
    }
}

/// LiveTranscription is one recording transcribed as it records. Audio
/// sent while the model downloads waits for it, in order. Once the
/// transcription fails, as with no model for the language, the audio goes
/// nowhere: kept for nothing, it would fill the memory over an hour.
private final class LiveTranscription: Sendable {
    private let audio: AsyncStream<Data>.Continuation
    private let task: Task<[Segment], any Error>
    /// pulse beats as the transcription goes on, which finish waits by.
    let pulse = Pulse()

    init(language: String, sink: SinkRef) {
        let (audio, input) = AsyncStream.makeStream(of: Data.self)
        self.audio = input
        let pulse = pulse
        task = Task.detached {
            do {
                return try await Self.transcribe(
                    audio, language: language, sink: sink, pulse: pulse)
            } catch {
                // The audio waiting goes, and what comes after isn't kept.
                input.finish()
                throw error
            }
        }
    }

    /// transcribe transcribes audio as it comes, until it ends.
    private static func transcribe(
        _ audio: AsyncStream<Data>, language: String, sink: SinkRef, pulse: Pulse
    ) async throws -> [Segment] {
        let transcriber = try await DeviceTranscriber.transcriber(for: language, pulse: pulse)
        guard
            let format = await SpeechAnalyzer.bestAvailableAudioFormat(
                compatibleWith: [transcriber]),
            let from = AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: 16000, channels: 1,
                interleaved: true),
            let converter = AVAudioConverter(from: from, to: format)
        else {
            throw CocoaError(.featureUnsupported)
        }
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        let (buffers, analyze) = AsyncStream.makeStream(of: AnalyzerInput.self)
        let collect = Task {
            try await DeviceTranscriber.collect(transcriber, sink: sink, pulse: pulse)
        }
        try await analyzer.start(inputSequence: buffers)
        for await pcm in audio {
            if let buffer = Self.buffer(pcm, from: from, with: converter, to: format) {
                analyze.yield(AnalyzerInput(buffer: buffer))
            }
        }
        analyze.finish()
        if Task.isCancelled {
            await analyzer.cancelAndFinishNow()
        } else {
            try await analyzer.finalizeAndFinishThroughEndOfInput()
        }
        return try await collect.value
    }

    func send(_ pcm: Data) { audio.yield(pcm) }

    /// finish waits for the words of the audio sent.
    func finish() async throws -> [Segment] {
        audio.finish()
        return try await task.value
    }

    func close() {
        task.cancel()
        audio.finish()
    }

    /// buffer is the core's PCM in the recognizer's format.
    private static func buffer(
        _ pcm: Data, from: AVAudioFormat, with converter: AVAudioConverter, to format: AVAudioFormat
    ) -> AVAudioPCMBuffer? {
        let frames = AVAudioFrameCount(pcm.count / 2)
        guard frames > 0, let input = AVAudioPCMBuffer(pcmFormat: from, frameCapacity: frames)
        else { return nil }
        input.frameLength = frames
        pcm.withUnsafeBytes { raw in
            if let base = raw.baseAddress, let samples = input.int16ChannelData {
                memcpy(samples[0], base, Int(frames) * 2)
            }
        }
        let capacity = AVAudioFrameCount(Double(frames) * format.sampleRate / from.sampleRate) + 64
        guard let output = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else {
            return nil
        }
        var given = false
        var error: NSError?
        converter.convert(to: output, error: &error) { _, status in
            if given {
                status.pointee = .noDataNow
                return nil
            }
            given = true
            status.pointee = .haveData
            return input
        }
        return error == nil && output.frameLength > 0 ? output : nil
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

/// TranscriptionError says why the device could not transcribe, in words
/// for the user.
private struct TranscriptionError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}
