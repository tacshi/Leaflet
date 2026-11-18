import Foundation
import LeafletSpeech
import Speech

@main
struct Helper {
    static func main() async {
        // Leaflet closing the pipes ends the helper through a failed write, not a signal.
        signal(SIGPIPE, SIG_IGN)
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
            try? FileHandle.standardError.write(
                contentsOf: Data(
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
            while let data = readInput() {
                var pcm = carry + data
                carry = pcm.count % 2 == 1 ? Data(pcm.suffix(1)) : Data()
                pcm.removeLast(carry.count)
                if !pcm.isEmpty { input.yield(pcm) }
            }
            input.finish()
        }
        return stream
    }

    /// The audio as it comes, rather than in whole blocks, or nil once it ends or cannot be read.
    private static func readInput() -> Data? {
        var bytes = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let n = read(STDIN_FILENO, &bytes, bytes.count)
            if n > 0 { return Data(bytes[..<n]) }
            if n < 0, errno == EINTR { continue }
            return nil
        }
    }
}

enum Output {
    private static let lock = NSLock()

    static func write(_ answer: Data) {
        var line = answer
        line.append(0x0A)
        lock.lock()
        defer { lock.unlock() }
        do {
            try FileHandle.standardOutput.write(contentsOf: line)
        } catch {
            // No one reads the answers any more.
            exit(1)
        }
    }
}
