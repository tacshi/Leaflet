import Foundation

/// AgentChat is a chat with an agent, as AgentPanel shows it: a page's, of
/// the page, or the workspace agent's, of all the user's pages.
@MainActor
protocol AgentChat: AnyObject, Observable {
    /// id is the chat's in the core: its page's, or "workspace".
    var id: String { get }
    var agents: AgentState { get }
    /// agentsLoaded says the state has been read, before which the panel
    /// can't say there is no agent to ask.
    var agentsLoaded: Bool { get }
    var chatDraft: String { get set }
    /// emptyText, placeholder and suggestions are what the chat says
    /// before anything was asked.
    var emptyText: String { get }
    var placeholder: String { get }
    var suggestions: [String] { get }
    func refreshAgents() async
    /// send asks the chat's agent text, from the window scene shows.
    func send(_ text: String, client: CoreClient, scene: SceneModel) async
}

extension PageModel: AgentChat {
    var emptyText: String { L.panelChatEmpty }
    var placeholder: String { L.panelAskPlaceholder }
    var suggestions: [String] {
        [L.panelSuggestSummarize, L.panelSuggestStructure, L.panelSuggestGaps, L.panelSuggestChart]
    }

    /// send asks the page's agent, about the blocks the editor's selection
    /// is on.
    func send(_ text: String, client: CoreClient, scene: SceneModel) async {
        let focus = scene.editor?.focusBlockIDs() ?? []
        await client.chat(id, text: text, focus: focus)
    }
}

/// WorkspaceAgentModel is the workspace agent's chat, of all the user's
/// pages rather than of one: the Agent tab's at All pages, and the panel's
/// with no page open. A window keeps one, its state the core's.
@MainActor
@Observable
final class WorkspaceAgentModel: AgentChat {
    /// chatID is the workspace agent's chat in the core (agents.WorkspaceID).
    static let chatID = "workspace"
    let id = chatID
    private(set) var agents = AgentState.empty
    private(set) var agentsLoaded = false
    var chatDraft = ""
    @ObservationIgnored private weak var host: (any PageHost)?
    @ObservationIgnored private var token: UUID?
    /// reading is the read of the state under way, and queued the one
    /// after it, which every change meanwhile shares, as a page's chat
    /// has them: a streaming answer changes it many times a second, and
    /// each read waits on the core's queue that edits go through.
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private var queued: Task<Void, Never>?
    /// account is the user the chat is of: what was typed and not sent
    /// goes once another signs in, or they sign out, as their chat does.
    @ObservationIgnored private var account: String?

    var emptyText: String { L.workspaceEmpty }
    var placeholder: String { L.workspacePlaceholder }
    var suggestions: [String] {
        [
            L.workspaceSuggestFile, L.workspaceSuggestTag, L.workspaceSuggestFind,
            L.workspaceSuggestNew,
        ]
    }

    /// start has the chat follow the core's events, once, and reads it.
    func start(_ host: any PageHost) {
        guard token == nil else { return }
        self.host = host
        token = host.listen { [weak self] in self?.handle($0) }
        Task {
            await refreshAgents()
            await keepAccount()
        }
    }

    /// stop has the chat stop following the core's events, as its window
    /// closes.
    func stop() {
        if let token { host?.stopListening(token) }
        token = nil
    }

    /// refreshAgents reads the state once the core is open, as a page's
    /// chat does: read before, it would say there is no agent to ask. It
    /// returns once a read begun after it was asked has answered.
    func refreshAgents() async {
        if let queued {
            await queued.value
            return
        }
        let before = reading
        let read = Task { [weak self] in
            await before?.value
            guard let self else { return }
            // Under way now: a change from here on asks for another.
            self.queued = nil
            let client = await self.host?.opened()
            if let s = try? await client?.agentState(self.id) { self.agents = s }
            if !self.agentsLoaded { self.agentsLoaded = true }
        }
        queued = read
        reading = read
        await read.value
    }

    /// keepAccount notes the user the chat is of, and lets go of what was
    /// typed for another.
    private func keepAccount() async {
        guard let client = await host?.opened(), let user = try? await client.status().userID else {
            return
        }
        if let account, account != user { chatDraft = "" }
        account = user
    }

    /// send asks the workspace agent, telling it of the page open.
    func send(_ text: String, client: CoreClient, scene: SceneModel) async {
        await client.workspaceChat(text, openPageID: scene.selection ?? "")
    }

    private func handle(_ event: CoreEvent) {
        switch event {
        case .agentsChanged(let pageID) where pageID.isEmpty || pageID == id:
            Task { await refreshAgents() }
        // The agents to pick from, as a page's chat hears them; and the
        // account, which signing in or out changes.
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
