import Foundation
import LeafletCore

/// RecordingState is the recording in progress, and the transcriptions
/// running, as the core last said.
struct RecordingState: Codable, Equatable, Sendable {
    var active = false
    var starting = false
    var paused = false
    var pageID = ""
    var blockID = ""
    var id = ""
    /// levels are the latest loudness levels, 0 to 1, one each 50 ms,
    /// newest last.
    var levels: [Double] = []
    var liveText = ""
    /// engine says where it is transcribed, or that it keeps only audio.
    var engine = ""
    var error = ""
    /// elapsedMs is how long it had recorded, without pauses, as the core
    /// said this.
    var elapsedMs: Int64 = 0
    var transcribing: [String] = []
    /// finishing are the recordings transcribed live, now getting their
    /// times.
    var finishing: [String] = []
    /// testing is Settings' test of the microphone running; heard is what
    /// the last one heard, or why it heard nothing.
    var testing = false
    var heard = ""

    /// recording says a recording runs, or is starting.
    var recording: Bool { active || starting }

    static let idle = RecordingState()

    enum CodingKeys: String, CodingKey {
        case active, starting, paused, id, levels, engine, error, transcribing, finishing,
            testing, heard
        case pageID = "page_id"
        case blockID = "block_id"
        case liveText = "live_text"
        case elapsedMs = "elapsed_ms"
    }
}

/// Segment is a line of a transcript, from startMs.
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

/// RecordingRow is a recording block's recording: its audio's state and
/// its transcript.
struct RecordingRow: Codable, Equatable, Sendable {
    var id: String
    /// status is recording, uploading, transcribing, ready or failed.
    var status: String
    var durationMs: Int
    /// sttProvider is what transcribed it, or "" for audio alone.
    var sttProvider: String
    var error: String
    var transcript: [Segment]
    /// plain is the transcript to copy.
    var plain: String

    enum CodingKeys: String, CodingKey {
        case id, status, error, transcript, plain
        case durationMs = "duration_ms"
        case sttProvider = "stt_provider"
    }
}

/// SttSettings are the speech-to-text choice, as the core keeps it.
struct SttSettings: Codable, Equatable, Sendable {
    /// provider is "openai-compatible" or "soniox".
    var provider: String
    var modelSize: String
    var runOn: String
    var language: String
    var liveTranscript: Bool
    var speakerLabels: Bool
    var baseURL: String
    var model: String
    /// providerID is the agent provider a cloud endpoint's key comes from.
    var providerID: String?
    var sonioxModel: String
    var languageHints: [String]?

    enum CodingKeys: String, CodingKey {
        case provider, language, model
        case modelSize = "model_size"
        case runOn = "run_on"
        case liveTranscript = "live_transcript"
        case speakerLabels = "speaker_labels"
        case baseURL = "base_url"
        case providerID = "provider_id"
        case sonioxModel = "soniox_model"
        case languageHints = "language_hints"
    }
}

/// SpeechSettings are Settings › Speech to text.
struct SpeechSettings: Codable, Sendable {
    var stt: SttSettings
    var endpointKey: String
    var sonioxSaved: Bool
    var signedIn: Bool
    /// onDevice says the device can transcribe by itself.
    var onDevice: Bool
    /// localEndpoint says the endpoint is on this device's network.
    var localEndpoint: Bool
    /// status says where transcription runs, or that it is not set up.
    var status: String
    var languages: [Language]

    struct Language: Codable, Hashable, Sendable {
        var code: String
        var name: String
    }

    enum CodingKeys: String, CodingKey {
        case stt, status, languages
        case endpointKey = "endpoint_key"
        case sonioxSaved = "soniox_saved"
        case signedIn = "signed_in"
        case onDevice = "on_device"
        case localEndpoint = "local_endpoint"
    }
}

extension CoreClient {
    // MARK: Recording

    /// useMicrophone gives the core's recorder the device's microphone.
    func useMicrophone(_ mic: Microphone) async {
        try? await call { $0.use(mic) }
    }

    /// useTranscriber gives the core's recorder the device's own speech
    /// engine.
    func useTranscriber(_ transcriber: DeviceTranscriber) async {
        try? await call { $0.use(transcriber) }
    }

    func recordingState() async throws -> RecordingState {
        try decode(
            RecordingState.self,
            try await call { core in try Self.answer { e in core.recordingState(e) } })
    }

    /// startRecording records into an open page, returning the block for
    /// the editor to put at the caret.
    func startRecording(_ pageID: String) async throws -> DocNode {
        try decode(
            DocNode.self,
            try await call { core in try Self.answer { e in core.startRecording(pageID, error: e) }
            })
    }

    func stopRecording() async { try? await call { $0.stopRecording() } }

    func togglePauseRecording() async { try? await call { $0.togglePauseRecording() } }

    func recording(_ id: String) async throws -> RecordingRow {
        try decode(
            RecordingRow.self,
            try await call { core in try Self.answer { e in core.recording(id, error: e) } })
    }

    /// recordingAudio returns a recording's WAV audio on this device,
    /// downloaded first when it is not.
    func recordingAudio(_ id: String) async throws -> URL {
        let path = try await call { core in
            try Self.answer { e in core.recordingAudio(id, error: e) }
        }
        return URL(fileURLWithPath: path)
    }

    func retranscribe(_ id: String) async throws {
        try await call { try $0.retranscribe(id) }
    }

    /// insertTranscript puts a recording's transcript after its block.
    func insertTranscript(_ id: String) async throws {
        try await call { try $0.insertTranscript(id) }
    }

    // MARK: Speech to text

    func speechSettings() async throws -> SpeechSettings {
        try decode(
            SpeechSettings.self,
            try await call { core in try Self.answer { e in core.speechSettings(e) } })
    }

    func saveSpeechSettings(_ stt: SttSettings) async throws {
        let json = String(decoding: try JSONEncoder().encode(stt), as: UTF8.self)
        try await call { try $0.saveSpeechSettings(json) }
    }

    func setEndpointKey(_ key: String) async throws {
        try await call { try $0.setEndpointKey(key) }
    }

    /// saveSonioxKey keeps a Soniox key, returning where it went.
    func saveSonioxKey(_ key: String) async throws -> String {
        try await call { core in try Self.answer { e in core.saveSonioxKey(key, error: e) } }
    }

    /// testEndpoint and testSoniox return words for the engine working.
    func testEndpoint(url: String, model: String, key: String) async throws -> String {
        try await call { core in
            try Self.answer { e in core.testEndpoint(url, model: model, key: key, error: e) }
        }
    }

    func testSoniox(_ key: String) async throws -> String {
        try await call { core in try Self.answer { e in core.testSoniox(key, error: e) } }
    }

    func testMicrophone() async throws {
        try await call { try $0.testMicrophone() }
    }
}
