import Foundation

@MainActor
protocol PageHost: AnyObject {
    var client: CoreClient? { get }
    func opened() async -> CoreClient?
    func listen(_ fn: @escaping (CoreEvent) -> Void) -> UUID
    func stopListening(_ id: UUID)
    func show(_ toast: Toast)
}

extension AppModel: PageHost {}

@MainActor
@Observable
final class PageModel {
    let id: String
    private let app: any PageHost
    @ObservationIgnored private(set) var content: DocContent? {
        didSet { if content != nil, !hasContent { hasContent = true } }
    }
    private(set) var hasContent = false
    private(set) var rev: Int64 = 0
    @ObservationIgnored var shown: Int64 = 0
    private(set) var readOnly = true
    private(set) var unreadable = false
    private(set) var tinted: Set<String> = []
    private(set) var failure: String?
    private(set) var external = 0
    private(set) var agents = AgentState.empty
    private(set) var agentsLoaded = false
    var chatDraft = ""
    var commentDraft = ""
    var replies: [String: String] = [:]
    private(set) var threads: [CommentThread] = []
    private(set) var saving = false
    private(set) var members: [Member] = []
    @ObservationIgnored private var token: UUID?
    @ObservationIgnored private var editor: Int64?
    @ObservationIgnored private var editing: Task<Void, Never>?
    @ObservationIgnored private var sending = 0
    @ObservationIgnored private var held: (content: DocContent, rev: Int64, reason: String)?
    @ObservationIgnored private(set) var byAgent = false
    @ObservationIgnored private(set) var notesOf: String?
    @ObservationIgnored private var edited: Int64 = 0
    @ObservationIgnored private(set) var closed = false
    var blank = false
    @ObservationIgnored private var asked = Newest()
    @ObservationIgnored private var agentsReading: Task<Void, Never>?
    @ObservationIgnored private var agentsQueued: Task<Void, Never>?
    private(set) var outline: [OutlineRow] = []
    @ObservationIgnored private var outlined: [DocNode] = []
    @ObservationIgnored private var outlining: Task<Void, Never>?
    @ObservationIgnored private var outlineAgain = false

    init(id: String, app: any PageHost) {
        self.id = id
        self.app = app
    }

    func open() async {
        guard let client = await app.opened(), !closed else { return }
        token = app.listen { [weak self] in self?.handle($0) }
        do {
            let page = try await client.openPage(id)
            if closed {
                await client.closePage(id, editor: page.editor)
                return
            }
            if content == nil {
                content = page.content
                rev = page.rev
                shown = rev
                refreshOutline()
            } else if page.rev >= rev {
                // An update that came first has an editor showing it: this
                // content goes to that editor as one more update.
                show(page.content, at: page.rev)
            }
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

    func close() async {
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

    @ObservationIgnored var sendPending: (() -> Void)?

    func edit(_ content: DocContent) {
        guard !readOnly, let client = app.client, let editor else { return }
        self.content = content
        refreshOutline()
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

    private func sent(_ left: Int64?) {
        sending -= 1
        if let left { edited = max(edited, left) }
        guard sending == 0, let held else { return }
        self.held = nil
        if held.rev >= edited { show(held.content, at: held.rev, reason: held.reason) }
    }

    private func show(_ content: DocContent, at rev: Int64, reason: String = "") {
        self.content = content
        self.rev = rev
        byAgent = reason == "agent"
        notesOf = reason.hasPrefix("notes:") ? String(reason.dropFirst(6)) : nil
        external += 1
        refreshOutline()
    }

    private func refreshOutline() {
        guard outlining == nil else {
            outlineAgain = true
            return
        }
        guard let content else { return }
        let before = outlined
        outlining = Task { [weak self] in
            let made = await Task.detached(priority: .userInitiated) {
                () -> (headings: [DocNode], rows: [OutlineRow])? in
                let headings = content.headings
                return headings == before ? nil : (headings, OutlineRow.rows(headings))
            }.value
            guard let self else { return }
            if let made {
                outlined = made.headings
                outline = made.rows
            }
            outlining = nil
            if outlineAgain {
                outlineAgain = false
                refreshOutline()
            }
        }
    }

    func flush() async { await editing?.value }

    func refreshAgents() async {
        if let queued = agentsQueued {
            await queued.value
            return
        }
        let before = agentsReading
        let read = Task { [weak self] in
            await before?.value
            guard let self else { return }
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

    func handle(_ event: CoreEvent) {
        switch event {
        case .pageUpdated(let pageID, let c, let newRev, let reason, _) where pageID == id:
            guard newRev > (held?.rev ?? rev), newRev >= edited else { return }
            if sending > 0 {
                held = (c, newRev, reason)
            } else {
                show(c, at: newRev, reason: reason)
            }
        case .pageReadOnly(let pageID, let ro) where pageID == id:
            readOnly = ro
        case .pageSaving(let pageID, let on) where pageID == id:
            saving = on
        case .tint(let pageID, let ids) where pageID == id:
            tinted = Set(ids)
        case .agentsChanged(let pageID) where pageID.isEmpty || pageID == id:
            Task { await refreshAgents() }
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

struct Newest {
    private var asked: [String: Int] = [:]
    private var kept: [String: Int] = [:]

    mutating func ask(_ what: String) -> Int {
        let n = (asked[what] ?? 0) + 1
        asked[what] = n
        return n
    }

    mutating func answered(_ n: Int, _ what: String) -> Bool {
        guard n > kept[what] ?? 0 else { return false }
        kept[what] = n
        return true
    }
}

extension DocContent {
    var blocks: [DocNode] { document.children ?? [] }

    var headings: [DocNode] {
        var out: [DocNode] = []
        func walk(_ nodes: [DocNode]) {
            for n in nodes {
                if n.type == "heading" { out.append(n) }
                walk(n.children ?? [])
            }
        }
        walk(blocks)
        return out
    }
}

extension DocNode {
    var text: String {
        (data?["delta"]?.array ?? []).compactMap { $0["insert"]?.string }.joined()
    }
}
