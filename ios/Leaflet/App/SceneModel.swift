import SwiftUI

@MainActor
@Observable
final class SceneModel {
    /// What the detail column shows.
    enum Selection: Hashable, Sendable {
        /// A page, by its ID.
        case page(String)
        /// A day's to-dos, by the day's key: the day as 2026-11-20, or "no-date".
        case day(String)
        /// The Trash.
        case bin

        /// The ID of the page, if a page is shown.
        var pageID: String? {
            if case .page(let id) = self { id } else { nil }
        }

        /// The selection as a window keeps it across launches: a page's ID, "day:" and a day's
        /// key, or "bin".
        var saved: String {
            switch self {
            case .page(let id): id
            case .day(let key): Self.dayPrefix + key
            case .bin: Self.binKey
            }
        }

        /// The selection a window kept across launches.
        init(saved: String) {
            if saved == Self.binKey {
                self = .bin
            } else if saved.hasPrefix(Self.dayPrefix) {
                self = .day(String(saved.dropFirst(Self.dayPrefix.count)))
            } else {
                self = .page(saved)
            }
        }

        private static let dayPrefix = "day:"
        private static let binKey = "bin"
    }

    private(set) var selection: Selection?
    private var back: [Selection] = []
    private var forward: [Selection] = []

    var columns = NavigationSplitViewVisibility.automatic
    var inspectorShown = false
    var inspectorTab = InspectorTab.agent
    var allPages = false
    let workspace = WorkspaceAgentModel()
    var workspaceShown = false
    var focusChat = false
    var stacked = false
    var sidebarToFold: CGFloat?
    @ObservationIgnored var recordOnOpen: String?
    var settingsShown = false
    @ObservationIgnored private var afterPanel: AfterPanel?
    @ObservationIgnored private var panelSheets = 0

    private enum AfterPanel { case settings, newFolder }
    var newFolderShown = false
    /// The folder a new folder goes in, or empty for the top level.
    var newFolderParent = ""
    var moving: PageRow?
    var movingFolder: FolderRow?
    var history: PageRow?
    var exported: ExportFile?
    @ObservationIgnored weak var page: PageModel?
    @ObservationIgnored var saveTitle: (() async -> Void)?
    @ObservationIgnored var target: String?
    @ObservationIgnored var targetAt = -1
    @ObservationIgnored let toasts = ToastWindow()

    func flush() async {
        if editor?.hasPendingEdit == true { editor?.send() }
        await saveTitle?()
        await page?.flush()
    }

    func letGo(_ model: PageModel?) {
        guard let model, page === model else { return }
        editor = nil
        page = nil
        saveTitle = nil
    }

    @ObservationIgnored private var held: PageModel?

    @ObservationIgnored private var closing = false

    func release(_ model: PageModel) {
        if model.id == pageID, !closing {
            if let held, held !== model { close(held) }
            held = model
            return
        }
        close(model)
    }

    func closeHeld() {
        closing = true
        if let held { close(held) }
        held = nil
    }

    func reopened() { closing = false }

    func editor(of id: String) -> EditorController? { page?.id == id ? editor : nil }

    private func close(_ model: PageModel) {
        letGo(model)
        Task { await model.close() }
    }

    var searchRequested = 0
    weak var editor: EditorController?
    var newComment: NewComment?
    var asking: AskRequest?
    var editingBlock: DocNode?
    var renamingBlock: DocNode?
    var blockName = ""
    var sharing: SharedFile?
    var quickLook: URL?
    var speakerEdit: SpeakerEdit?
    var replacingNotes: DocNode?
    var replacingWith = ""
    var editingNotes: DocNode?
    var editingLink: DocNode?
    var heading: String?
    var writing = false

    /// Shows the selection, or nothing with nil, keeping what was shown to go back to.
    func open(_ selection: Selection?) {
        guard selection != self.selection else { return }
        if let current = self.selection { back.append(current) }
        forward.removeAll()
        show(selection)
    }

    /// Shows the page with this ID.
    func open(_ id: String) { open(.page(id)) }

    /// Shows the Trash, or, with it shown, goes back to what was shown before it.
    func toggleBin() {
        guard selection == .bin else { return open(.bin) }
        while back.last == .bin { back.removeLast() }
        if canGoBack {
            goBack()
        } else {
            forward.append(.bin)
            show(nil)
        }
    }

    func open(_ id: String, at blockID: String?, moment: Int = -1) {
        guard let blockID, !blockID.isEmpty else { return open(id) }
        if id == pageID, let editor {
            editor.reveal(blockID: blockID, atTop: true)
            editor.play(blockID: blockID, at: moment)
            return
        }
        open(id)
        (target, targetAt) = (blockID, moment)
    }

    /// The ID of the page shown, if a page is.
    var pageID: String? { selection?.pageID }

    var canGoBack: Bool { !back.isEmpty }
    var canGoForward: Bool { !forward.isEmpty }

    func goBack() {
        guard let previous = back.popLast() else { return }
        if let current = selection { forward.append(current) }
        show(previous)
    }

    func goForward() {
        guard let next = forward.popLast() else { return }
        if let current = selection { back.append(current) }
        show(next)
    }

    private func show(_ selection: Selection?) {
        if let held, held.id != selection?.pageID {
            close(held)
            self.held = nil
        }
        if recordOnOpen != selection?.pageID { recordOnOpen = nil }
        self.selection = selection
        (target, targetAt) = (nil, -1)
        newComment = nil
        asking = nil
        editingBlock = nil
        renamingBlock = nil
        editingNotes = nil
        editingLink = nil
        sharing = nil
        quickLook = nil
        replacingNotes = nil
        speakerEdit = nil
    }

    /// Forgets the pages no longer in ids, going back from one shown. A day's to-dos and the
    /// Trash stay.
    func keep(_ ids: Set<String>) {
        let kept = { (selection: Selection) in selection.pageID.map(ids.contains) ?? true }
        back.removeAll { !kept($0) }
        forward.removeAll { !kept($0) }
        if let selection, !kept(selection) { show(back.popLast()) }
    }

    func showPanel(_ tab: InspectorTab) {
        guard pageID != nil else {
            if tab == .agent, stacked {
                workspaceShown.toggle()
            } else if tab == .agent {
                inspectorShown.toggle()
            }
            return
        }
        if inspectorShown, inspectorTab == tab {
            inspectorShown = false
        } else {
            inspectorTab = tab
            inspectorShown = true
        }
    }

    func askWorkspace(sheet: Bool = false) {
        inspectorTab = .agent
        allPages = true
        focusChat = true
        if sheet {
            workspaceShown = true
        } else {
            inspectorShown = true
        }
    }

    func openFromChat(
        _ id: String, at block: String? = nil, moment: Int = -1, workspace: Bool, sheet: Bool
    ) {
        if workspace { (inspectorTab, allPages) = (.agent, true) }
        if sheet {
            inspectorShown = false
            workspaceShown = false
        }
        open(id, at: block, moment: moment)
    }

    func openSettings() { showOverPanel(.settings) }

    func newFolder(in parentID: String = "") {
        newFolderParent = parentID
        showOverPanel(.newFolder)
    }

    private func showOverPanel(_ what: AfterPanel) {
        guard panelSheets > 0 else { return show(what) }
        afterPanel = what
        inspectorShown = false
        workspaceShown = false
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard let self, self.afterPanel == what else { return }
            self.panelSheets = 0
            self.panelGone(asSheet: false)
        }
    }

    private func show(_ what: AfterPanel) {
        switch what {
        case .settings: settingsShown = true
        case .newFolder: newFolderShown = true
        }
    }

    func panelShown(asSheet: Bool) {
        if asSheet { panelSheets += 1 }
    }

    func panelGone(asSheet: Bool) {
        if asSheet { panelSheets = max(panelSheets - 1, 0) }
        guard panelSheets == 0, let what = afterPanel else { return }
        afterPanel = nil
        show(what)
    }

    func search() {
        setColumns(.all)
        searchRequested += 1
    }

    func toggleSidebar() {
        setColumns(columns == .detailOnly ? .all : .detailOnly)
    }

    private func setColumns(_ visibility: NavigationSplitViewVisibility) {
        withAnimation { columns = visibility }
    }
}

extension FocusedValues {
    @Entry var scene: SceneModel?
}
