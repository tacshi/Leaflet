import Foundation
import LeafletCore

/// AgentState is a page's agent state: its chat, its runs, and the agents
/// to pick from.
struct AgentState: Codable, Sendable {
    struct Message: Codable, Hashable, Sendable {
        var mine: Bool
        var text: String
        var runID: String?
        var pending: Bool
        var error: Bool
        var agentName: String?

        enum CodingKeys: String, CodingKey {
            case mine, text, pending, error
            case runID = "run_id"
            case agentName = "agent_name"
        }
    }

    struct Run: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var surface: String
        var agentName: String
        var instruction: String
        var status: String
        var undone: Bool
        var error: String?
        var edits: [String]
        var blockIDs: [String]?
        var summary: String
        var stoppable: Bool
        /// answer is the model's answer as far as it has come, while the
        /// run works and has not edited the page.
        var answer: String?

        enum CodingKeys: String, CodingKey {
            case id, surface, instruction, status, undone, error, edits, summary, stoppable, answer
            case agentName = "agent_name"
            case blockIDs = "block_ids"
        }

        var running: Bool { status == "running" }
    }

    struct Option: Codable, Hashable, Identifiable, Sendable {
        var providerID: String
        var model: String
        var name: String
        var label: String
        var local: Bool

        var id: String { providerID + "/" + model }

        enum CodingKeys: String, CodingKey {
            case model, name, label, local
            case providerID = "provider_id"
        }
    }

    /// Choice is an agent and the model it runs.
    struct Choice: Codable, Hashable, Sendable {
        var providerID: String
        var model: String

        enum CodingKeys: String, CodingKey {
            case model
            case providerID = "provider_id"
        }
    }

    var messages: [Message]
    var runs: [Run]
    var running: Bool
    var options: [Option]
    var available: Bool
    /// current is the agent the chat asks, picked or the default.
    var current: Choice?
    /// defaultAgent is the agent asked where none is named, as Settings
    /// sets it.
    var defaultAgent: Choice?

    enum CodingKeys: String, CodingKey {
        case messages, runs, running, options, available, current
        case defaultAgent = "default_agent"
    }

    static let empty = AgentState(
        messages: [], runs: [], running: false, options: [], available: false)
}

/// CommentThread is a first comment and its replies.
struct CommentThread: Codable, Hashable, Identifiable, Sendable {
    struct Comment: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var blockID: String
        var authorKind: String
        var authorName: String
        var body: String
        var quote: String?
        var resolved: Bool
        var createdAt: Int64

        enum CodingKeys: String, CodingKey {
            case id, body, quote, resolved
            case blockID = "block_id"
            case authorKind = "author_kind"
            case authorName = "author_name"
            case createdAt = "created_at"
        }

        var created: Date { Date(timeIntervalSince1970: TimeInterval(createdAt) / 1000) }
    }

    var root: Comment
    var replies: [Comment]
    var id: String { root.id }
}

/// ProviderPreset is a kind of agent provider to add.
struct ProviderPreset: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var baseURL: String
    var auth: String
    var group: String
    var local: Bool
    var suggested: [String]?
    var placeholder: String

    enum CodingKeys: String, CodingKey {
        case id, name, auth, group, local, suggested, placeholder
        case baseURL = "base_url"
    }
}

/// AgentProvider is a service agents run on.
struct AgentProvider: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var preset: String
    var name: String
    var baseURL: String
    var authKind: String
    var defaultModel: String
    var isLocal: Bool
    var models: [String]?
    var hasKey: Bool
    var apiKey: String?
    var supportsTools: Bool?
    /// email is who a ChatGPT provider signed in as.
    var email: String?

    /// isAccount says the provider runs on a ChatGPT plan, signed in to.
    var isAccount: Bool { authKind == "account" }

    enum CodingKeys: String, CodingKey {
        case id, preset, name, models, email
        case baseURL = "base_url"
        case authKind = "auth_kind"
        case defaultModel = "default_model"
        case isLocal = "is_local"
        case hasKey = "has_key"
        case apiKey = "api_key"
        case supportsTools = "supports_tools"
    }
}

/// ProviderTest is what trying a provider found: whether its model
/// answers and calls tools, and the models it serves.
struct ProviderTest: Codable, Sendable {
    var ok: Bool
    var supportsTools: Bool
    var message: String
    var models: [String]?

    enum CodingKeys: String, CodingKey {
        case ok, message, models
        case supportsTools = "supports_tools"
    }
}

extension CoreClient {
    private func json<T: Encodable>(_ v: T) -> String {
        String(decoding: (try? JSONEncoder().encode(v)) ?? Data("null".utf8), as: UTF8.self)
    }

    private func value<T: Decodable>(_ type: T.Type, _ raw: String) throws -> T {
        try JSONDecoder().decode(type, from: Data(raw.utf8))
    }

    // MARK: Agents

    func agentState(_ pageID: String) async throws -> AgentState {
        try value(
            AgentState.self,
            try await call { core in try Self.answer { e in core.agentState(pageID, error: e) } })
    }

    func chat(_ pageID: String, text: String, focus: [String]) async {
        let f = json(focus)
        try? await call { $0.chat(pageID, text: text, focus: f) }
    }

    /// ask starts a run from the editor, and returns its ID.
    func ask(
        _ pageID: String, surface: String, instruction: String, focus: [String], selection: String,
        agent: AgentState.Option?
    ) async -> String {
        let f = json(focus)
        return
            (try? await call {
                $0.ask(
                    pageID, surface: surface, instruction: instruction, focus: f,
                    selection: selection,
                    providerID: agent?.providerID ?? "", model: agent?.model ?? "")
            }) ?? ""
    }

    func stopAgent(_ pageID: String, runID: String) async {
        try? await call { $0.stopAgent(pageID, runID: runID) }
    }

    func undoRun(_ pageID: String, runID: String) async {
        try? await call { $0.undoRun(pageID, runID: runID) }
    }

    func redoRun(_ pageID: String, runID: String) async {
        try? await call { $0.redoRun(pageID, runID: runID) }
    }

    /// selectAgent picks the agent and model the page's chat asks.
    func selectAgent(_ pageID: String, providerID: String, model: String) async {
        try? await call { $0.selectAgent(pageID, providerID: providerID, model: model) }
    }

    func newConversation(_ pageID: String) async {
        try? await call { $0.newConversation(pageID) }
    }

    // MARK: Comments

    func threads(_ pageID: String) async throws -> [CommentThread] {
        try value(
            [CommentThread].self,
            try await call { core in try Self.answer { e in core.threads(pageID, error: e) } })
    }

    func addComment(
        _ pageID: String, blockID: String, body: String, parentID: String = "", quote: String = ""
    )
        async throws
    {
        try await call {
            try $0.addComment(
                pageID, blockID: blockID, body: body, parentID: parentID, quote: quote)
        }
    }

    func resolveThread(_ rootID: String, resolved: Bool) async throws {
        try await call { try $0.resolveThread(rootID, resolved: resolved) }
    }

    func deleteComment(_ id: String) async throws {
        try await call { try $0.deleteComment(id) }
    }

    // MARK: Providers

    func presets() async throws -> [ProviderPreset] {
        try value(
            [ProviderPreset].self,
            try await call { core in try Self.answer { e in core.presets(e) } })
    }

    func providers() async throws -> [AgentProvider] {
        try value(
            [AgentProvider].self,
            try await call { core in try Self.answer { e in core.providers(e) } })
    }

    func saveProvider(_ provider: AgentProvider) async throws {
        let raw = json(provider)
        try await call { try $0.saveProvider(raw) }
    }

    /// testProvider tries a provider, with the key typed in its apiKey or
    /// the one kept for it.
    func testProvider(_ provider: AgentProvider) async throws -> ProviderTest {
        let raw = json(provider)
        return try value(
            ProviderTest.self,
            try await call { core in try Self.answer { e in core.testProvider(raw, error: e) } })
    }

    func removeProvider(_ id: String) async throws {
        try await call { try $0.removeProvider(id) }
    }

    /// beginChatGPTSignIn begins signing in with ChatGPT for the provider
    /// providerID ("" for a new one), and returns OpenAI's page to open.
    func beginChatGPTSignIn(_ providerID: String) async throws -> URL {
        let raw = try await call { core in
            try Self.answer { e in core.beginChatGPTSign(in: providerID, error: e) }
        }
        guard let url = URL(string: raw) else { throw URLError(.badURL) }
        return url
    }

    /// finishChatGPTSignIn waits for the sign-in begun to come back.
    func finishChatGPTSignIn() async throws -> ChatGPTAccount {
        try value(
            ChatGPTAccount.self,
            try await call { core in try Self.answer { e in core.finishChatGPTSign(in: e) } })
    }

    /// chatGPTModels are the models the plan signed in to serves.
    func chatGPTModels() async throws -> [String] {
        try value(
            [String].self,
            try await call { core in try Self.answer { e in core.chatGPTModels(e) } })
    }

    /// cancelChatGPTSignIn gives up the sign-in being made, as its page
    /// closes; one already made stays, to save.
    func cancelChatGPTSignIn() {
        Task { try? await call { $0.cancelChatGPTSignIn() } }
    }

    /// forgetChatGPTSignIn gives up the sign-in being made, and forgets one
    /// made and not saved, as the form closes.
    func forgetChatGPTSignIn() {
        Task { try? await call { $0.forgetChatGPTSignIn() } }
    }
}

/// ChatGPTAccount is who signed in with ChatGPT, and the models their plan
/// serves.
struct ChatGPTAccount: Codable, Sendable {
    var email: String
    var models: [String]
}
