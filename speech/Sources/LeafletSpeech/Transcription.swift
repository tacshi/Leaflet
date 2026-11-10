import AVFoundation
import Foundation
import Speech

@available(macOS 26, iOS 26, *)
public enum Transcription {
    public static func stream(
        _ audio: AsyncStream<Data>, language: String,
        text: @escaping @Sendable (String) -> Void, beat: @escaping @Sendable () -> Void = {}
    ) async throws -> [SpeechSegment] {
        let transcriber = try await transcriber(for: language, beat: beat)
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
        let heard = Task { try await collect(transcriber, text: text, beat: beat) }
        try await analyzer.start(inputSequence: buffers)
        for await pcm in audio {
            if let buffer = buffer(pcm, from: from, with: converter, to: format) {
                analyze.yield(AnalyzerInput(buffer: buffer))
            }
        }
        analyze.finish()
        if Task.isCancelled {
            await analyzer.cancelAndFinishNow()
        } else {
            try await analyzer.finalizeAndFinishThroughEndOfInput()
        }
        return try await heard.value
    }

    public static func file(
        _ path: String, language: String, beat: @escaping @Sendable () -> Void = {}
    ) async throws -> [SpeechSegment] {
        let transcriber = try await transcriber(for: language, beat: beat)
        let audio = try AVAudioFile(forReading: URL(fileURLWithPath: path))
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        let heard = Task { try await collect(transcriber, text: nil, beat: beat) }
        try await analyzer.start(inputAudioFile: audio, finishAfterFile: true)
        return try await heard.value
    }

    static func transcriber(for language: String, beat: @escaping @Sendable () -> Void)
        async throws -> SpeechTranscriber
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
            let watching = request.progress.observe(\.completedUnitCount) { _, _ in beat() }
            defer { watching.invalidate() }
            try await request.downloadAndInstall()
        }
        return transcriber
    }

    static func locale(for language: String) async throws -> Locale {
        let wanted =
            language == "auto" || language.isEmpty
            ? [Locale.current] + Locale.preferredLanguages.map(Locale.init(identifier:))
            : [Locale(identifier: language)]
        for l in wanted {
            if let supported = await SpeechTranscriber.supportedLocale(equivalentTo: l) {
                return supported
            }
        }
        throw SpeechFailure(
            message: "unsupported language: \(wanted[0].identifier)", code: "language",
            language: wanted[0].language.languageCode?.identifier)
    }

    static func collect(
        _ transcriber: SpeechTranscriber, text: (@Sendable (String) -> Void)?,
        beat: @Sendable () -> Void
    ) async throws -> [SpeechSegment] {
        var segments: [SpeechSegment] = []
        var final = ""
        for try await result in transcriber.results {
            beat()
            let words = String(result.text.characters)
            if result.isFinal {
                let trimmed = words.trimmingCharacters(in: .whitespacesAndNewlines)
                if !trimmed.isEmpty {
                    segments.append(
                        SpeechSegment(
                            startMs: Int(result.range.start.seconds * 1000),
                            endMs: Int(result.range.end.seconds * 1000), text: trimmed))
                }
                final += words
                text?(final)
            } else {
                text?(final + words)
            }
        }
        return segments
    }

    static func buffer(
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
