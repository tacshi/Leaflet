// leaflet-speech transcribes recordings with macOS's own speech
// recognition (SpeechTranscriber): on the Mac, offline, once its
// language's model is downloaded. The desktop app ships it in its
// resources and runs it as the recorder's on-device engine
// (internal/macspeech):
//
//	leaflet-speech available
//	    exits 0 when this Mac can transcribe, 1 when it cannot.
//	leaflet-speech stream <language>
//	    transcribes 16-bit mono PCM at 16 kHz from stdin until it ends.
//	leaflet-speech file <language> <path>
//	    transcribes the WAV file at path.
//
// language is an ISO 639-1 code, or "auto" for the Mac's own language.
// What it writes to stdout is JSON, a line each: {"text": ...}, the words
// so far as they come, then {"done": true, "segments": [...]}, the
// transcript, each segment with its start_ms and end_ms; or {"error": ...},
// with "code": "language" and the language's code when the Mac cannot
// transcribe the language.

import AVFoundation
import Foundation
import Speech

@main
struct LeafletSpeech {
    static func main() async {
        let args = Array(CommandLine.arguments.dropFirst())
        guard #available(macOS 26, *) else { exit(1) }
        switch args.first {
        case "available":
            exit(SpeechTranscriber.isAvailable ? 0 : 1)
        case "stream" where args.count == 2:
            await run { try await Transcription.stream(language: args[1]) }
        case "file" where args.count == 3:
            await run { try await Transcription.file(args[2], language: args[1]) }
        default:
            FileHandle.standardError.write(
                Data("usage: leaflet-speech available | stream <language> | file <language> <path>\n".utf8))
            exit(2)
        }
    }

    /// run writes the transcript body makes, or why it could not.
    private static func run(_ body: @Sendable () async throws -> [Segment]) async {
        do {
            Output.write(Done(segments: try await body()))
        } catch {
            let helper = error as? HelperError
            Output.write(
                Failure(error: error.localizedDescription, code: helper?.code, language: helper?.language))
            exit(1)
        }
    }
}

/// Segment is a line of a transcript, as Leaflet keeps it.
struct Segment: Encodable, Sendable {
    var startMs: Int
    var endMs: Int
    var text: String

    enum CodingKeys: String, CodingKey {
        case text
        case startMs = "start_ms"
        case endMs = "end_ms"
    }
}

struct Words: Encodable { var text: String }
struct Done: Encodable {
    var done = true
    var segments: [Segment]
}
struct Failure: Encodable {
    var error: String
    var code: String?
    var language: String?
}

/// Output writes the helper's answers, a JSON line each.
enum Output {
    private static let lock = NSLock()

    static func write(_ value: some Encodable) {
        guard var data = try? JSONEncoder().encode(value) else { return }
        data.append(0x0A)
        lock.lock()
        FileHandle.standardOutput.write(data)
        lock.unlock()
    }
}

/// HelperError says why the Mac could not transcribe; code names the
/// reason for the app to say it in the user's language.
struct HelperError: LocalizedError {
    let message: String
    let code: String
    var language: String?
    var errorDescription: String? { message }
}

@available(macOS 26, *)
enum Transcription {
    /// stream transcribes what stdin gives it, as it comes.
    static func stream(language: String) async throws -> [Segment] {
        let transcriber = try await transcriber(for: language)
        guard
            let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]),
            let from = AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: 16000, channels: 1, interleaved: true),
            let converter = AVAudioConverter(from: from, to: format)
        else {
            throw CocoaError(.featureUnsupported)
        }
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        let (buffers, analyze) = AsyncStream.makeStream(of: AnalyzerInput.self)
        let collect = Task { try await collect(transcriber, live: true) }
        try await analyzer.start(inputSequence: buffers)
        for await pcm in standardInput() {
            if let buffer = buffer(pcm, from: from, with: converter, to: format) {
                analyze.yield(AnalyzerInput(buffer: buffer))
            }
        }
        analyze.finish()
        try await analyzer.finalizeAndFinishThroughEndOfInput()
        return try await collect.value
    }

    /// file transcribes the WAV file at path.
    static func file(_ path: String, language: String) async throws -> [Segment] {
        let transcriber = try await transcriber(for: language)
        let audio = try AVAudioFile(forReading: URL(fileURLWithPath: path))
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        let collect = Task { try await collect(transcriber, live: false) }
        try await analyzer.start(inputAudioFile: audio, finishAfterFile: true)
        return try await collect.value
    }

    /// standardInput is what stdin gives, as it comes, until it ends.
    private static func standardInput() -> AsyncStream<Data> {
        let (stream, input) = AsyncStream.makeStream(of: Data.self)
        Thread.detachNewThread {
            // A read may end inside a sample, as a pipe splits what is
            // written where it likes: its odd byte waits for the next
            // read, so that every later sample keeps its two bytes.
            var carry = Data()
            while true {
                let data = FileHandle.standardInput.availableData
                if data.isEmpty { break }
                var pcm = carry + data
                carry = pcm.count % 2 == 1 ? Data(pcm.suffix(1)) : Data()
                pcm.removeLast(carry.count)
                if !pcm.isEmpty { input.yield(pcm) }
            }
            input.finish()
        }
        return stream
    }

    /// transcriber is a transcriber of language ("auto" for the Mac's), its
    /// model downloaded first if this is its first time.
    private static func transcriber(for language: String) async throws -> SpeechTranscriber {
        let locale = try await locale(for: language)
        let transcriber = SpeechTranscriber(
            locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults],
            attributeOptions: [.audioTimeRange])
        if !(await AssetInventory.reservedLocales).contains(locale) {
            _ = try? await AssetInventory.reserve(locale: locale)
        }
        if let request = try await AssetInventory.assetInstallationRequest(supporting: [transcriber]) {
            try await request.downloadAndInstall()
        }
        return transcriber
    }

    /// locale is the language's locale the Mac can transcribe: for "auto",
    /// the Mac's, or the first of the user's languages it can.
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
        throw HelperError(
            message: "unsupported language: \(wanted[0].identifier)", code: "language",
            language: wanted[0].language.languageCode?.identifier)
    }

    /// collect gathers the final results as the transcript, a segment each,
    /// writing the words so far as they come when live.
    private static func collect(_ transcriber: SpeechTranscriber, live: Bool) async throws -> [Segment] {
        var segments: [Segment] = []
        var final = ""
        for try await result in transcriber.results {
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
                if live { Output.write(Words(text: final)) }
            } else if live {
                Output.write(Words(text: final + text))
            }
        }
        return segments
    }

    /// buffer is 16 kHz PCM in the recognizer's format.
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
