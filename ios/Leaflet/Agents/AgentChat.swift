import Foundation

@MainActor
protocol AgentChat: AnyObject, Observable {
    var id: String { get }
    var agents: AgentState { get }
    var agentsLoaded: Bool { get }
    var chatDraft: String { get set }
    var emptyText: String { get }
    var placeholder: String { get }
    var suggestions: [String] { get }
    func refreshAgents() async
    func send(_ text: String, client: CoreClient, scene: SceneModel) async
}

extension PageModel: AgentChat {
    var emptyText: String { L.panelChatEmpty }
    var placeholder: String { L.panelAskPlaceholder }
    var suggestions: [String] {
        [L.panelSuggestSummarize, L.panelSuggestStructure, L.panelSuggestGaps, L.panelSuggestChart]
    }

    func send(_ text: String, client: CoreClient, scene: SceneModel) async {
        let focus = scene.editor?.focusBlockIDs() ?? []
        await client.chat(id, text: text, focus: focus)
    }
}

@MainActor
@Observable
final class WorkspaceAgentModel: AgentChat {
    static let chatID = "workspace"
    let id = chatID
    private(set) var agents = AgentState.empty
    private(set) var agentsLoaded = false
    var chatDraft = ""
    @ObservationIgnored private weak var host: (any PageHost)?
    @ObservationIgnored private var token: UUID?
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private var queued: Task<Void, Never>?
    @ObservationIgnored private var account: String?

    var emptyText: String { L.workspaceEmpty }
    var placeholder: String { L.workspacePlaceholder }
    var suggestions: [String] {
        [
            L.workspaceSuggestFile, L.workspaceSuggestTag, L.workspaceSuggestFind,
            L.workspaceSuggestNew,
        ]
    }

    func start(_ host: any PageHost) {
        guard token == nil else { return }
        self.host = host
        token = host.listen { [weak self] in self?.handle($0) }
        Task {
            await refreshAgents()
            await keepAccount()
        }
    }

    func stop() {
        if let token { host?.stopListening(token) }
        token = nil
    }

    func refreshAgents() async {
        if let queued {
            await queued.value
            return
        }
        let before = reading
        let read = Task { [weak self] in
            await before?.value
            guard let self else { return }
            self.queued = nil
            let client = await self.host?.opened()
            if let s = try? await client?.agentState(self.id) { self.agents = s }
            if !self.agentsLoaded { self.agentsLoaded = true }
        }
        queued = read
        reading = read
        await read.value
    }

    private func keepAccount() async {
        guard let client = await host?.opened(), let user = try? await client.status().userID else {
            return
        }
        if let account, account != user { chatDraft = "" }
        account = user
    }

    func send(_ text: String, client: CoreClient, scene: SceneModel) async {
        await client.workspaceChat(text, openPageID: scene.selection ?? "")
    }

    private func handle(_ event: CoreEvent) {
        switch event {
        case .agentsChanged(let pageID) where pageID.isEmpty || pageID == id:
            Task { await refreshAgents() }
        case .changed(let table, _)
        where ["model_providers", "key_values", "session", "*"].contains(table):
            Task {
                await refreshAgents()
                if table == "session" || table == "*" { await keepAccount() }
            }
        default:
            break
        }
    }
}
