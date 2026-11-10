import Foundation
import LeafletCore

struct RecordingState: Codable, Equatable, Sendable {
    var active = false
    var starting = false
    var paused = false
    var pageID = ""
    var blockID = ""
    var id = ""
    var levels: [Double] = []
    var liveText = ""
    var engine = ""
    var error = ""
    var elapsedMs: Int64 = 0
    var transcribing: [String] = []
    var transcribingFiles: [String] = []
    var finishing: [String] = []
    var testing = false
    var heard = ""

    var recording: Bool { active || starting }

    static let idle = RecordingState()

    enum CodingKeys: String, CodingKey {
        case active, starting, paused, id, levels, engine, error, transcribing, finishing,
            testing, heard
        case transcribingFiles = "transcribing_files"
        case pageID = "page_id"
        case blockID = "block_id"
        case liveText = "live_text"
        case elapsedMs = "elapsed_ms"
    }
}

struct Segment: Codable, Hashable, Sendable {
    var startMs: Int
    var endMs: Int
    var text: String
    var speaker: String?

    enum CodingKeys: String, CodingKey {
        case text, speaker
        case startMs = "start_ms"
        case endMs = "end_ms"
    }
}

struct RecordingRow: Codable, Equatable, Sendable {
    var id: String
    var status: String
    var durationMs: Int
    var sttProvider: String
    var error: String
    var transcript: [Segment]
    var plain: String
    var label = ""
    var createdMs: Int64 = 0
    var pageTitle = ""
    var folded = 0

    enum CodingKeys: String, CodingKey {
        case id, status, error, transcript, plain, label, folded
        case durationMs = "duration_ms"
        case sttProvider = "stt_provider"
        case createdMs = "created_ms"
        case pageTitle = "page_title"
    }
}

struct TranscriptFile: Decodable, Sendable {
    var name: String
    var text: String
}

struct Transcript: Decodable, Sendable {
    var blocks: [DocNode]
}

struct SttSettings: Codable, Equatable, Sendable {
    var provider: String
    var modelSize: String
    var runOn: String
    var language: String
    var liveTranscript: Bool
    var speakerLabels: Bool
    var baseURL: String
    var model: String
    var sonioxModel: String
    var languageHints: [String]?

    enum CodingKeys: String, CodingKey {
        case provider, language, model
        case modelSize = "model_size"
        case runOn = "run_on"
        case liveTranscript = "live_transcript"
        case speakerLabels = "speaker_labels"
        case baseURL = "base_url"
        case sonioxModel = "soniox_model"
        case languageHints = "language_hints"
    }
}

struct SpeechSettings: Codable, Sendable {
    var stt: SttSettings
    var endpointKey: String
    var sonioxSaved: Bool
    var mossSaved: Bool
    var onDevice: Bool
    var status: String
    var providers: [SpeechProvider]
    var addable: [SpeechProvider]
    var languages: [Language]
    var keepAudio: String

    struct Language: Codable, Hashable, Sendable {
        var code: String
        var name: String
    }

    enum CodingKeys: String, CodingKey {
        case stt, status, languages, providers, addable
        case keepAudio = "keep_audio"
        case endpointKey = "endpoint_key"
        case sonioxSaved = "soniox_saved"
        case mossSaved = "moss_saved"
        case onDevice = "on_device"
    }
}

struct SpeechChoices: Codable, Equatable, Sendable {
    var choices: [SpeechProvider]
    var current: String
}

struct SpeechProvider: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var place: String?
    var removable: Bool?

    static func name(of id: String) -> String {
        switch id {
        case "on-device": L.recSttOnDevice
        case "soniox": "Soniox"
        case "moss": "MOSS"
        default: L.commonEndpoint
        }
    }
}

extension CoreClient {
    nonisolated(nonsending) func useMicrophone(_ mic: Microphone) async {
        try? await call { $0.use(mic) }
    }

    nonisolated(nonsending) func useTranscriber(_ transcriber: DeviceTranscriber) async {
        try? await call { $0.use(transcriber) }
    }

    nonisolated(nonsending) func recordingState() async throws -> RecordingState {
        try await call(RecordingState.self) { core in
            try Self.answer { e in core.recordingState(e) }
        }
    }

    nonisolated(nonsending) func startRecording(_ pageID: String, provider: String) async throws
        -> DocNode
    {
        try await call(DocNode.self) { core in
            try Self.answer { e in core.startRecording(pageID, provider: provider, error: e) }
        }
    }

    nonisolated(nonsending) func recordNewPage(folderID: String) async throws -> String {
        try await call { core in
            try Self.answer { e in core.recordNewPage(folderID, error: e) }
        }
    }

    nonisolated(nonsending) func speechChoices(_ pageID: String) async throws -> SpeechChoices {
        try await call(SpeechChoices.self) { core in
            try Self.answer { e in core.speechChoices(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func stopRecording() async { try? await call { $0.stopRecording() } }

    nonisolated(nonsending) func togglePauseRecording() async {
        try? await call { $0.togglePauseRecording() }
    }

    nonisolated(nonsending) func recording(_ id: String) async throws -> RecordingRow {
        try await call(RecordingRow.self) { core in
            try Self.answer { e in core.recording(id, error: e) }
        }
    }

    nonisolated(nonsending) func recordingAudio(_ id: String) async throws -> URL {
        let path = try await cancellable { core, token in
            try Self.answer { e in core.recordingAudio(id, token: token, error: e) }
        }
        return URL(fileURLWithPath: path)
    }

    nonisolated(nonsending) func recordingTranscriptFile(
        _ id: String, title: String, format: String
    )
        async throws -> TranscriptFile
    {
        try await call(TranscriptFile.self) { core in
            try Self.answer { e in
                core.recordingTranscriptFile(id, title: title, format: format, error: e)
            }
        }
    }

    nonisolated(nonsending) func transcribeFile(page: String, block: String, provider: String)
        async throws
    {
        try await call { try $0.transcribeFile(page, blockID: block, provider: provider) }
    }

    nonisolated(nonsending) func retranscribe(_ id: String, provider: String) async throws {
        try await call { try $0.retranscribe(id, provider: provider) }
    }

    nonisolated(nonsending) func dismissRecordingError(_ id: String) async throws {
        try await call { try $0.dismissRecordingError(id) }
    }

    nonisolated(nonsending) func transcriptBlocks(_ id: String) async throws -> Transcript {
        try await call(Transcript.self) { core in
            try Self.answer { e in core.transcriptBlocks(id, error: e) }
        }
    }

    nonisolated(nonsending) func speechSettings() async throws -> SpeechSettings {
        try await call(SpeechSettings.self) { core in
            try Self.answer { e in core.speechSettings(e) }
        }
    }

    nonisolated(nonsending) func setKeepAudio(_ keep: String) async throws {
        try await call { try $0.setKeepAudio(keep) }
    }

    nonisolated(nonsending) func saveSpeechSettings(_ stt: SttSettings) async throws {
        let json = String(decoding: try JSONEncoder().encode(stt), as: UTF8.self)
        try await call { try $0.saveSpeechSettings(json) }
    }

    nonisolated(nonsending) func setEndpointKey(url: String, key: String) async throws {
        try await call { try $0.setEndpointKey(url, key: key) }
    }

    nonisolated(nonsending) func saveSonioxKey(_ key: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.saveSonioxKey(key, error: e) }
        }
    }

    nonisolated(nonsending) func saveMossKey(_ key: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.saveMossKey(key, error: e) }
        }
    }

    nonisolated(nonsending) func testEndpoint(url: String, model: String, key: String) async throws
        -> String
    {
        try await call(waiting: true) { core in
            try Self.answer { e in core.testEndpoint(url, model: model, key: key, error: e) }
        }
    }

    nonisolated(nonsending) func testSoniox(_ key: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.testSoniox(key, error: e) }
        }
    }

    nonisolated(nonsending) func testMoss(_ key: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.testMoss(key, error: e) }
        }
    }

    nonisolated(nonsending) func removeSpeechProvider(_ id: String) async throws {
        try await call { try $0.removeSpeechProvider(id) }
    }

    nonisolated(nonsending) func testMicrophone() async throws {
        try await call { try $0.testMicrophone() }
    }
}
