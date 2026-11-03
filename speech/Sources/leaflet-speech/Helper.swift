// leaflet-speech transcribes recordings with macOS's own speech
// recognition (LeafletSpeech): on the Mac, offline, once its language's
// model is downloaded. The desktop app ships it in its resources and runs
// it as the recorder's on-device engine (internal/macspeech):
//
//	leaflet-speech available
//	    exits 0 when this Mac can transcribe, 1 when it cannot.
//	leaflet-speech stream <language>
//	    transcribes 16-bit mono PCM at 16 kHz from stdin until it ends.
//	leaflet-speech file <language> <path>
//	    transcribes the WAV file at path.
//
// language is an ISO 639-1 code, or "auto" for the Mac's own language.
// What it writes to stdout is JSON, a line each, as the iOS app's engine
// answers the core (Answer): {"text": ...}, the words so far as they
// come, then {"done": true, "segments": [...]}, the transcript, each
// segment with its start_ms and end_ms; or {"error": ...}, with "code":
// "language" and the language's code when the Mac cannot transcribe the
// language.

import Foundation
import LeafletSpeech
import Speech

@main
struct Helper {
    static func main() async {
        let args = Array(CommandLine.arguments.dropFirst())
        guard #available(macOS 26, *) else { exit(1) }
        switch args.first {
        case "available":
            exit(SpeechTranscriber.isAvailable ? 0 : 1)
        case "stream" where args.count == 2:
            await run {
                try await Transcription.stream(
                    standardInput(), language: args[1], text: { Output.write(Answer.words($0)) })
            }
        case "file" where args.count == 3:
            await run { try await Transcription.file(args[2], language: args[1]) }
        default:
            FileHandle.standardError.write(
                Data(
                    "usage: leaflet-speech available | stream <language> | file <language> <path>\n"
                        .utf8))
            exit(2)
        }
    }

    /// run writes the transcript body makes, or why it could not.
    private static func run(_ body: @Sendable () async throws -> [SpeechSegment]) async {
        do {
            Output.write(Answer.done(try await body()))
        } catch {
            Output.write(Answer.failed(error))
            exit(1)
        }
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
}

/// Output writes the helper's answers, a JSON line each.
enum Output {
    private static let lock = NSLock()

    static func write(_ answer: Data) {
        var line = answer
        line.append(0x0A)
        lock.lock()
        FileHandle.standardOutput.write(line)
        lock.unlock()
    }
}
