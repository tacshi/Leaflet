import Foundation

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

    public static let stuck = SpeechFailure(
        message: "the transcription stopped going on", code: "stuck")
}

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

    public static func words(_ text: String) -> Data { encode(Words(text: text)) }

    public static func done(_ segments: [SpeechSegment]) -> Data {
        encode(Done(segments: segments))
    }

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
        return (try? encoder.encode(value)) ?? Data("{}".utf8)
    }
}
