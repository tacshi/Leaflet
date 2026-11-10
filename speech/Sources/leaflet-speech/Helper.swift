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

    private static func run(_ body: @Sendable () async throws -> [SpeechSegment]) async {
        do {
            Output.write(Answer.done(try await body()))
        } catch {
            Output.write(Answer.failed(error))
            exit(1)
        }
    }

    private static func standardInput() -> AsyncStream<Data> {
        let (stream, input) = AsyncStream.makeStream(of: Data.self)
        Thread.detachNewThread {
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
