import Foundation
import LeafletCore

struct AgentState: Codable, Sendable {
    struct Message: Codable, Hashable, Sendable {
        var mine: Bool
        var text: String
        var runID: String?
        var pending: Bool
        var error: Bool
        var agentName: String?
        var streamed: Bool

        enum CodingKeys: String, CodingKey {
            case mine, text, pending, error, streamed
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
        var editPages: [String]?
        var blockIDs: [String]?
        var summary: String
        var stoppable: Bool
        var answer: String?
        var activity: String?
        var settled = false

        enum CodingKeys: String, CodingKey {
            case id, surface, instruction, status, undone, error, edits, summary, stoppable, answer,
                activity, settled
            case agentName = "agent_name"
            case blockIDs = "block_ids"
            case editPages = "edit_pages"
        }

        var running: Bool { status == "running" }

        func page(_ i: Int) -> String? {
            guard let pages = editPages, pages.indices.contains(i), !pages[i].isEmpty else {
                return nil
            }
            return pages[i]
        }
    }

    struct Option: Codable, Hashable, Identifiable, Sendable {
        var providerID: String
        var model: String
        var name: String
        var label: String

        var id: String { providerID + "/" + model }

        enum CodingKeys: String, CodingKey {
            case model, name, label
            case providerID = "provider_id"
        }
    }

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
    var current: Choice?
    var defaultAgent: Choice?
    var past: [Past] = []

    struct Past: Codable, Hashable, Identifiable, Sendable {
        var id: String
        var title: String
    }

    enum CodingKeys: String, CodingKey {
        case messages, runs, running, options, available, current, past
        case defaultAgent = "default_agent"
    }

    static let empty = AgentState(
        messages: [], runs: [], running: false, options: [], available: false)

    var runToUndo: Run? {
        guard !running, !runs.contains(where: \.running) else { return nil }
        return runs.last { !$0.edits.isEmpty && !$0.undone }
    }
}

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
        var deleted: Bool?
        var mayDelete: Bool
        var askedBy: String?

        enum CodingKeys: String, CodingKey {
            case id, body, quote, resolved, deleted
            case blockID = "block_id"
            case authorKind = "author_kind"
            case authorName = "author_name"
            case createdAt = "created_at"
            case mayDelete = "may_delete"
            case askedBy = "asked_by"
        }

        var created: Date { Date(timeIntervalSince1970: TimeInterval(createdAt) / 1000) }
    }

    var root: Comment
    var replies: [Comment]
    var mayResolve: Bool
    var id: String { root.id }

    enum CodingKeys: String, CodingKey {
        case root, replies
        case mayResolve = "may_resolve"
    }
}

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
    var email: String?

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

    nonisolated(nonsending) func agentState(_ pageID: String) async throws -> AgentState {
        try await call(AgentState.self) { core in
            try Self.answer { e in core.agentState(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func chat(_ pageID: String, text: String, focus: [String]) async {
        let f = json(focus)
        try? await call { $0.chat(pageID, text: text, focus: f) }
    }

    nonisolated(nonsending) func workspaceChat(_ text: String, openPageID: String) async {
        try? await call { $0.workspaceChat(text, openPageID: openPageID) }
    }

    nonisolated(nonsending) func ask(
        _ pageID: String, surface: String, instruction: String, focus: [String], selection: String,
        agent: AgentState.Option?
    ) async throws -> String {
        let f = json(focus)
        return try await call { core in
            try Self.answer { e in
                core.ask(
                    pageID, surface: surface, instruction: instruction, focus: f,
                    selection: selection,
                    providerID: agent?.providerID ?? "", model: agent?.model ?? "", error: e)
            }
        }
    }

    nonisolated(nonsending) func stopAgent(_ pageID: String, runID: String) async {
        try? await call { $0.stopAgent(pageID, runID: runID) }
    }

    nonisolated(nonsending) func undoRun(_ pageID: String, runID: String) async throws {
        try await call { try $0.undoRun(pageID, runID: runID) }
    }

    nonisolated(nonsending) func replyBlocks(_ pageID: String, runID: String) async throws
        -> [DocNode]
    {
        try await call([DocNode].self) { core in
            try Self.answer { e in core.replyBlocks(pageID, runID: runID, error: e) }
        }
    }

    nonisolated(nonsending) func redoRun(_ pageID: String, runID: String) async throws {
        try await call { try $0.redoRun(pageID, runID: runID) }
    }

    nonisolated(nonsending) func selectAgent(_ pageID: String, providerID: String, model: String)
        async
    {
        try? await call { $0.selectAgent(pageID, providerID: providerID, model: model) }
    }

    nonisolated(nonsending) func openConversation(_ pageID: String, id: String) async {
        try? await call { $0.openConversation(pageID, id_: id) }
    }

    nonisolated(nonsending) func newConversation(_ pageID: String) async {
        try? await call { $0.newConversation(pageID) }
    }

    nonisolated(nonsending) func threads(_ pageID: String) async throws -> [CommentThread] {
        try await call([CommentThread].self) { core in
            try Self.answer { e in core.threads(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func addComment(
        _ pageID: String, blockID: String, body: String, parentID: String = "", quote: String = ""
    )
        async throws
    {
        try await call {
            try $0.addComment(
                pageID, blockID: blockID, body: body, parentID: parentID, quote: quote)
        }
    }

    nonisolated(nonsending) func resolveThread(_ rootID: String, resolved: Bool) async throws {
        try await call { try $0.resolveThread(rootID, resolved: resolved) }
    }

    nonisolated(nonsending) func deleteComment(_ id: String) async throws {
        try await call { try $0.deleteComment(id) }
    }

    nonisolated(nonsending) func presets() async throws -> [ProviderPreset] {
        try await call([ProviderPreset].self) { core in try Self.answer { e in core.presets(e) } }
    }

    nonisolated(nonsending) func providers() async throws -> [AgentProvider] {
        try await call([AgentProvider].self) { core in try Self.answer { e in core.providers(e) } }
    }

    nonisolated(nonsending) func saveProvider(_ provider: AgentProvider) async throws {
        let raw = json(provider)
        try await call(waiting: true) { try $0.saveProvider(raw) }
    }

    nonisolated(nonsending) func testProvider(_ provider: AgentProvider) async throws
        -> ProviderTest
    {
        let raw = json(provider)
        return try await call(ProviderTest.self, waiting: true) { core in
            try Self.answer { e in core.testProvider(raw, error: e) }
        }
    }

    nonisolated(nonsending) func removeProvider(_ id: String) async throws {
        try await call(waiting: true) { try $0.removeProvider(id) }
    }

    nonisolated(nonsending) func beginChatGPTSignIn(_ providerID: String) async throws
        -> ChatGPTSignIn
    {
        try await call(ChatGPTSignIn.self, waiting: true) { core in
            try Self.answer { e in core.beginChatGPTSign(in: providerID, error: e) }
        }
    }

    nonisolated(nonsending) func finishChatGPTSignIn() async throws -> ChatGPTAccount {
        try await call(ChatGPTAccount.self, waiting: true) { core in
            try Self.answer { e in core.finishChatGPTSign(in: e) }
        }
    }

    nonisolated(nonsending) func chatGPTModels() async throws -> [String] {
        try await call([String].self, waiting: true) { core in
            try Self.answer { e in core.chatGPTModels(e) }
        }
    }

    func cancelChatGPTSignIn(_ attempt: Int64) {
        Task { try? await call(waiting: true) { $0.cancelChatGPTSign(in: attempt) } }
    }

    func forgetChatGPTSignIn(_ attempt: Int64) {
        Task { try? await call(waiting: true) { $0.forgetChatGPTSign(in: attempt) } }
    }
}

struct ChatGPTSignIn: Decodable, Sendable {
    var url: URL
    var attempt: Int64
}

struct ChatGPTAccount: Codable, Sendable {
    var email: String
    var models: [String]
}
