import Foundation

/// SpeechSegment is a line of a transcript, as Leaflet keeps it.
public struct SpeechSegment: Codable, Hashable, Sendable {
    public var startMs: Int
    public var endMs: Int
    public var text: String

    public init(startMs: Int, endMs: Int, text: String) {
        self.startMs = startMs
        self.endMs = endMs
        self.text = text
    }

    enum CodingKeys: String, CodingKey {
        case text
        case startMs = "start_ms"
        case endMs = "end_ms"
    }
}

/// SpeechFailure says why the device could not transcribe. Its code names
/// the reason for the core to say it in the user's language: "language",
/// a language the device can't transcribe, which language names by its
/// ISO 639 code, or "stuck", a transcription that stopped going on and was
/// given up. message is the reason in English, for the logs.
public struct SpeechFailure: LocalizedError, Sendable {
    public let message: String
    public let code: String
    public var language: String?

    public init(message: String, code: String, language: String? = nil) {
        self.message = message
        self.code = code
        self.language = language
    }

    public var errorDescription: String? { message }

    /// stuck is a transcription given up as it stopped going on.
    public static let stuck = SpeechFailure(
        message: "the transcription stopped going on", code: "stuck")
}

/// Answer is what a transcription says to the core, a JSON object each, as
/// it reads them (recording.OnDeviceAnswer): the words so far, the
/// transcript once done, or why it could not transcribe.
public enum Answer {
    struct Words: Encodable { var text: String }

    struct Done: Encodable {
        var done = true
        var segments: [SpeechSegment]
    }

    struct Failure: Encodable {
        var error: String
        var code: String?
        var language: String?
    }

    /// words is the words so far.
    public static func words(_ text: String) -> Data { encode(Words(text: text)) }

    /// done is the transcript.
    public static func done(_ segments: [SpeechSegment]) -> Data {
        encode(Done(segments: segments))
    }

    /// failed is why the transcription failed, with its code when it is a
    /// SpeechFailure.
    public static func failed(_ error: any Error) -> Data {
        let failure = error as? SpeechFailure
        return encode(
            Failure(
                error: error.localizedDescription, code: failure?.code,
                language: failure?.language))
    }

    private static func encode(_ value: some Encodable) -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        // Of strings, numbers and booleans, it always encodes.
        return (try? encoder.encode(value)) ?? Data("{}".utf8)
    }
}
