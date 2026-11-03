import Foundation

/// PageHost is what a page's model needs of the app: the core, its events,
/// and a toast. AppModel is the app's; a test gives one of its own.
@MainActor
protocol PageHost: AnyObject {
    var client: CoreClient? { get }
    func opened() async -> CoreClient?
    func listen(_ fn: @escaping (CoreEvent) -> Void) -> UUID
    func stopListening(_ id: UUID)
    func show(_ toast: Toast)
}

extension AppModel: PageHost {}

/// PageModel is a page open in a window: its content at the core's rev,
/// kept current as the core sends changes from sync and agents, and the
/// edits the editor makes, sent back over the rev they were made on.
@MainActor
@Observable
final class PageModel {
    let id: String
    private let app: any PageHost
    private(set) var content: DocContent?
    private(set) var rev: Int64 = 0
    /// shown is the rev of the content the editor shows, which its edits
    /// are made over: rev, once the editor shows what came at it.
    @ObservationIgnored var shown: Int64 = 0
    private(set) var readOnly = true
    private(set) var unreadable = false
    private(set) var tinted: Set<String> = []
    /// failure says why the page could not open.
    private(set) var failure: String?
    /// external counts the changes from outside the editor, for the editor
    /// to show them.
    private(set) var external = 0
    /// agents is the page's agent state: its chat and runs; agentsLoaded
    /// says it has been read, before which the panel can't say there is
    /// no agent to ask.
    private(set) var agents = AgentState.empty
    private(set) var agentsLoaded = false
    /// chatDraft, commentDraft and replies are what was typed and not sent
    /// in the panel: in the chat, a new comment, and the threads' replies,
    /// by thread. The page keeps them as the panel's tabs change.
    var chatDraft = ""
    var commentDraft = ""
    var replies: [String: String] = [:]
    /// threads are the page's comment threads.
    private(set) var threads: [CommentThread] = []
    /// saving says the edits wait to be saved, as the byline says.
    private(set) var saving = false
    /// members are who the page is shared with, which the byline counts.
    private(set) var members: [Member] = []
    @ObservationIgnored private var token: UUID?
    /// editor is the number the core gave this window's editor of the
    /// page.
    @ObservationIgnored private var editor: Int64?
    /// editing is the latest edit on its way to the core, or the page's
    /// closing, after them.
    @ObservationIgnored private var editing: Task<Void, Never>?
    /// sending counts the edits on their way to the core. A change from
    /// outside waits behind them: shown over them, it would take away what
    /// was just typed, until the core's merge of them came.
    @ObservationIgnored private var sending = 0
    /// held is the newest change from outside that came while edits were
    /// on their way, which the editor shows once they are there.
    @ObservationIgnored private var held: (content: DocContent, rev: Int64, agent: Bool)?
    /// byAgent says the change from outside the editor shows is an agent
    /// run's, as the core said it.
    @ObservationIgnored private(set) var byAgent = false
    /// edited is the newest rev an edit left the page at, which has it: a
    /// change from outside at an older one, though it comes after the
    /// edit was taken, is from before it, and would take it away.
    @ObservationIgnored private var edited: Int64 = 0
    /// closed says the window let the page go, opened yet or not.
    @ObservationIgnored private var closed = false
    /// blank says the page is one empty paragraph, which offers ways to
    /// start it.
    var blank = false
    /// asked numbers the reads of the threads and the members, as each is
    /// asked again on every change: an answer older than the one shown is
    /// dropped.
    @ObservationIgnored private var asked = Newest()
    /// agentsReading is the read of the agent state under way, and
    /// agentsQueued the one after it, which every change meanwhile shares:
    /// a streaming answer changes it 20 times a second, and each read
    /// waits on the core's queue that edits go through.
    @ObservationIgnored private var agentsReading: Task<Void, Never>?
    @ObservationIgnored private var agentsQueued: Task<Void, Never>?

    init(id: String, app: any PageHost) {
        self.id = id
        self.app = app
    }

    /// open opens the page in the core and listens for its changes.
    func open() async {
        // A page open as the app launches waits for the core to open.
        guard let client = await app.opened(), !closed else { return }
        token = app.listen { [weak self] in self?.handle($0) }
        do {
            let page = try await client.openPage(id)
            if closed {
                // Let go while it opened: the core's editor goes too, or the
                // page would stay open there with an editor nobody has.
                await client.closePage(id, editor: page.editor)
                return
            }
            // A change heard while it opened is newer than what it opened
            // as.
            if content == nil || page.rev >= rev {
                content = page.content
                rev = page.rev
            }
            shown = rev
            editor = page.editor
            readOnly = page.readOnly
            unreadable = page.unreadable
            await refreshAgents()
            await refreshThreads()
            await refreshMembers()
        } catch {
            failure = error.localizedDescription
        }
    }

    /// close saves what was typed and closes the page in the core, once
    /// the core has the edits on their way to it. A page still opening
    /// closes in the core as it opens.
    func close() async {
        // What was typed in the last moment goes first, before the editor's
        // view is torn down, which may be after the page closes.
        sendPending?()
        closed = true
        if let token { app.stopListening(token) }
        token = nil
        guard let client = app.client, let editor else { return }
        let id = self.id
        editing = Task { [previous = editing] in
            await previous?.value
            await client.closePage(id, editor: editor)
        }
        await flush()
    }

    /// sendPending sends what the editor has typed and not sent yet, if
    /// it has.
    @ObservationIgnored var sendPending: (() -> Void)?

    /// edit gives the core the content as the editor has it now, after
    /// the edits before it, over the rev the editor shows.
    func edit(_ content: DocContent) {
        guard !readOnly, let client = app.client, let editor else { return }
        self.content = content
        let rev = shown
        let id = self.id
        sending += 1
        editing = Task { [previous = editing, app, weak self] in
            await previous?.value
            var left: Int64?
            do {
                left = try await client.editPage(id, editor: editor, content: content, rev: rev)
            } catch {
                app.show(Toast(message: L.appErrorSave(error: error.localizedDescription)))
            }
            self?.sent(left)
        }
    }

    /// sent follows an edit the core has, which left the page at rev left:
    /// once none is on its way, the change from outside held for them
    /// shows, unless it is from before them. The core's merge of them, at
    /// left or after, shows instead as it comes, if it hasn't.
    private func sent(_ left: Int64?) {
        sending -= 1
        if let left { edited = max(edited, left) }
        guard sending == 0, let held else { return }
        self.held = nil
        if held.rev >= edited { show(held.content, at: held.rev, agent: held.agent) }
    }

    /// show has the editor show content, the page at rev; agent says an
    /// agent's run changed it, which the editor's Undo then undoes.
    private func show(_ content: DocContent, at rev: Int64, agent: Bool = false) {
        self.content = content
        self.rev = rev
        byAgent = agent
        external += 1
    }

    /// flush waits for the core to have the edits given it.
    func flush() async { await editing?.value }

    /// refreshAgents reads the agent state again, and returns once a read
    /// begun after it was asked has answered.
    func refreshAgents() async {
        if let queued = agentsQueued {
            await queued.value
            return
        }
        let before = agentsReading
        let read = Task { [weak self] in
            await before?.value
            guard let self else { return }
            // Under way now: a change from here on asks for another.
            self.agentsQueued = nil
            if let s = try? await self.app.client?.agentState(self.id) { self.agents = s }
            if !self.agentsLoaded { self.agentsLoaded = true }
        }
        agentsQueued = read
        agentsReading = read
        await read.value
    }

    func refreshThreads() async {
        let n = asked.ask("threads")
        if let t = try? await app.client?.threads(id), asked.answered(n, "threads") { threads = t }
    }

    func refreshMembers() async {
        let n = asked.ask("members")
        if let m = try? await app.client?.members(id), asked.answered(n, "members") { members = m }
    }

    /// handle follows the core's events for the page, as the app hears
    /// them.
    func handle(_ event: CoreEvent) {
        switch event {
        case .pageUpdated(let pageID, let c, let newRev, let reason, _) where pageID == id:
            guard newRev > (held?.rev ?? rev), newRev >= edited else { return }
            if sending > 0 {
                held = (c, newRev, reason == "agent")
            } else {
                show(c, at: newRev, agent: reason == "agent")
            }
        case .pageReadOnly(let pageID, let ro) where pageID == id:
            readOnly = ro
        case .pageSaving(let pageID, let on) where pageID == id:
            saving = on
        case .tint(let pageID, let ids) where pageID == id:
            tinted = Set(ids)
        case .agentsChanged(let pageID) where pageID.isEmpty || pageID == id:
            Task { await refreshAgents() }
        // The agents to pick from: a provider saved or removed, the
        // default agent, signing in or out, or all of them at once.
        case .changed(let table, _)
        where ["model_providers", "key_values", "session", "*"].contains(table):
            Task { await refreshAgents() }
        case .changed(let table, let pageID)
        where table == "comments" && (pageID.isEmpty || pageID == id):
            Task { await refreshThreads() }
        case .changed(let table, let pageID)
        where table == "page_members" && (pageID.isEmpty || pageID == id):
            Task { await refreshMembers() }
        default:
            break
        }
    }
}

/// Newest numbers the reads of what the core keeps, by what is read, for
/// an answer not to replace a newer one: a read asked as one change comes,
/// and another as the next does, may answer in either order. Each answer
/// newer than the one kept is kept, so reads asked faster than they
/// answer still show as they come.
struct Newest {
    private var asked: [String: Int] = [:]
    private var kept: [String: Int] = [:]

    /// ask numbers a read of what.
    mutating func ask(_ what: String) -> Int {
        let n = (asked[what] ?? 0) + 1
        asked[what] = n
        return n
    }

    /// answered says read n of what answered newer than the answer kept,
    /// to keep instead.
    mutating func answered(_ n: Int, _ what: String) -> Bool {
        guard n > kept[what] ?? 0 else { return false }
        kept[what] = n
        return true
    }
}

extension DocContent {
    /// blocks are the page's top-level blocks.
    var blocks: [DocNode] { document.children ?? [] }

    /// headings are the page's headings, in order, with their levels, for
    /// the outline.
    var headings: [(node: DocNode, level: Int)] {
        var out: [(DocNode, Int)] = []
        func walk(_ nodes: [DocNode]) {
            for n in nodes {
                if n.type == "heading" {
                    out.append((n, Int(n.data?["level"]?.int ?? 1)))
                }
                walk(n.children ?? [])
            }
        }
        walk(blocks)
        return out
    }
}

extension DocNode {
    /// text is the block's text, without its formats.
    var text: String {
        (data?["delta"]?.array ?? []).compactMap { $0["insert"]?.string }.joined()
    }
}
