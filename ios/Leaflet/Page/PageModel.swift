import Foundation

/// PageModel is a page open in a window: its content at the core's rev,
/// kept current as the core sends changes from sync and agents, and the
/// edits the editor makes, sent back over the rev they were made on.
@MainActor
@Observable
final class PageModel {
    let id: String
    private let app: AppModel
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
    /// agents is the page's agent state: its chat and runs.
    private(set) var agents = AgentState.empty
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
    /// closed says the window let the page go, opened yet or not.
    @ObservationIgnored private var closed = false
    /// blank says the page is one empty paragraph, which offers ways to
    /// start it.
    var blank = false

    init(id: String, app: AppModel) {
        self.id = id
        self.app = app
    }

    /// open opens the page in the core and listens for its changes.
    func open() async {
        guard let client = app.client, !closed else { return }
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
        editing = Task { [previous = editing, app] in
            await previous?.value
            do {
                try await client.editPage(id, editor: editor, content: content, rev: rev)
            } catch {
                app.show(Toast(message: L.appErrorSave(error: error.localizedDescription)))
            }
        }
    }

    /// flush waits for the core to have the edits given it.
    func flush() async { await editing?.value }

    func refreshAgents() async {
        if let s = try? await app.client?.agentState(id) { agents = s }
    }

    func refreshThreads() async {
        if let t = try? await app.client?.threads(id) { threads = t }
    }

    func refreshMembers() async {
        if let m = try? await app.client?.members(id) { members = m }
    }

    private func handle(_ event: CoreEvent) {
        switch event {
        case .pageUpdated(let pageID, let json, let newRev, _, _) where pageID == id:
            guard newRev > rev,
                let c = try? JSONDecoder().decode(DocContent.self, from: Data(json.utf8))
            else { return }
            content = c
            rev = newRev
            external += 1
        case .pageReadOnly(let pageID, let ro) where pageID == id:
            readOnly = ro
        case .pageSaving(let pageID, let on) where pageID == id:
            saving = on
        case .tint(let pageID, let ids) where pageID == id:
            tinted = Set(ids)
        case .agentsChanged:
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
