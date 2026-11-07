import SwiftUI

/// SceneModel is a window's state: the page open in it and the pages
/// before and after it, which panels show, and the page's editor. The
/// menu bar's commands act on the window in front through it.
@MainActor
@Observable
final class SceneModel {
    /// selection is the page open in the window.
    private(set) var selection: String?
    private var back: [String] = []
    private var forward: [String] = []

    var columns = NavigationSplitViewVisibility.automatic
    var inspectorShown = false
    /// inspectorTab is the panel's tab shown: Agent first, as on the desktop.
    var inspectorTab = InspectorTab.agent
    /// allPages is the Agent tab's switch beside a page: the workspace
    /// agent's chat, of all the user's pages, rather than the page's. With
    /// no page open, the panel is the workspace agent's alone.
    var allPages = false
    /// workspace is the workspace agent's chat, the window's.
    let workspace = WorkspaceAgentModel()
    /// workspaceShown shows the workspace agent's chat as a sheet of the
    /// sidebar's, as on iPhone, where no page is beside it for the panel.
    var workspaceShown = false
    /// focusChat has an agent chat's composer take the keyboard as it
    /// shows, once, as Ask the agent opens it.
    var focusChat = false
    /// stacked says the window's columns stack, as on iPhone: with no page
    /// open, no panel is beside one, and the workspace agent's chat is a
    /// sheet of the sidebar's.
    var stacked = false
    /// sidebarToFold is the width that takes the sidebar, where it shows,
    /// up to the fold of a half folded display, the page in the half
    /// beyond it, or nil.
    var sidebarToFold: CGFloat?
    /// recordOnOpen is a page opening to record in, as New Recording makes
    /// one with no page open: it records once its editor shows.
    @ObservationIgnored var recordOnOpen: String?
    var settingsShown = false
    /// afterPanel is what shows once the panel as a sheet has gone.
    @ObservationIgnored private var afterPanel: AfterPanel?
    /// panelSheets counts the panels showing as a sheet, as on iPhone: one,
    /// or for a moment two as the page changes under it.
    @ObservationIgnored private var panelSheets = 0

    /// AfterPanel is what the window shows over itself, which can't show
    /// over the panel as a sheet.
    private enum AfterPanel { case settings, newFolder }
    var newFolderShown = false
    /// moving is the page the Move to sheet is moving.
    var moving: PageRow?
    /// history is the page whose versions show.
    var history: PageRow?
    /// exported is a page exported as a file, Markdown or PDF, being saved
    /// where the user picks.
    var exported: ExportFile?
    /// page is the open page's model, for its edits to reach the core.
    @ObservationIgnored weak var page: PageModel?
    /// saveTitle keeps the open page's title as typed, which the page sets:
    /// the title is otherwise kept only as its field lets the keyboard go.
    @ObservationIgnored var saveTitle: (() async -> Void)?
    /// target is a block the page opening goes to once it shows, as a
    /// to-do's or a search hit's, and targetAt the time it plays it from,
    /// in seconds, for a link to a moment of a video or a recording; -1 for
    /// none.
    @ObservationIgnored var target: String?
    @ObservationIgnored var targetAt = -1
    /// toasts shows the app's toasts over the window and its sheets.
    @ObservationIgnored let toasts = ToastWindow()

    /// flush gives the core what was typed in the open page, its title
    /// too, and waits until it has it: a copy, an export or the history
    /// then has it too.
    func flush() async {
        if editor?.hasPendingEdit == true { editor?.send() }
        await saveTitle?()
        await page?.flush()
    }

    /// letGo drops the page's editor and model, as the page leaves the
    /// window, unless the next page's took their place already: the Format
    /// menu then acts on no page, and the window doesn't keep it.
    func letGo(_ model: PageModel?) {
        guard let model, page === model else { return }
        editor = nil
        page = nil
        saveTitle = nil
    }

    /// held is the open page's model while no view of it runs: SwiftUI
    /// ends a page view as it moves the page into the navigation stack on
    /// iPhone, where it stays shown. It closes once the window opens
    /// another page, or none.
    @ObservationIgnored private var held: PageModel?

    /// closing says the window's views went: a page view's task ends after
    /// them, and no page opens after, to close its model.
    @ObservationIgnored private var closing = false

    /// release closes a page's model as its view goes: at once when the
    /// window shows another page, or closes, or once it does.
    func release(_ model: PageModel) {
        if model.id == selection, !closing {
            if let held, held !== model { close(held) }
            held = model
            return
        }
        close(model)
    }

    /// closeHeld closes the page model the window holds, as it closes, and
    /// has release close the one whose view is still going.
    func closeHeld() {
        closing = true
        if let held { close(held) }
        held = nil
    }

    /// reopened says the window's views came back.
    func reopened() { closing = false }

    /// editor(of:) is the editor of page id, while the window shows it.
    func editor(of id: String) -> EditorController? { page?.id == id ? editor : nil }

    private func close(_ model: PageModel) {
        letGo(model)
        Task { await model.close() }
    }

    /// searchRequested asks the sidebar to show and focus its search.
    var searchRequested = 0
    /// editor is the open page's editor, for the Format menu. Weakly: the
    /// editor's view keeps it, and its closures reach the page's view,
    /// which reaches the window.
    weak var editor: EditorController?
    /// newComment is a comment being started from the editor.
    var newComment: NewComment?
    /// asking is an agent being asked from the editor.
    var asking: AskRequest?
    /// editingBlock is a block being edited in a sheet, as a chart's data.
    var editingBlock: DocNode?
    /// renamingBlock is a block being renamed, as a recording, and
    /// blockName the name being typed for it.
    var renamingBlock: DocNode?
    var blockName = ""
    /// editingNotes is a video block whose notes are being written, and
    /// editingLink one whose link is being changed.
    var editingNotes: DocNode?
    var editingLink: DocNode?
    /// heading is the heading the open page is at, which the outline shows.
    var heading: String?
    /// writing says the page's text or title has the keyboard, which the
    /// panels beside it then stay still for.
    var writing = false

    /// open opens a page, as a choice in the sidebar does, keeping the
    /// page before for Back.
    func open(_ id: String?) {
        guard id != selection else { return }
        if let current = selection { back.append(current) }
        forward.removeAll()
        show(id)
    }

    /// open opens a page at a block, as a to-do or a search hit does: the
    /// page open goes to it at once, another once it shows. A link to a
    /// moment of a video or a recording plays it from moment seconds in.
    func open(_ id: String, at blockID: String?, moment: Int = -1) {
        guard let blockID, !blockID.isEmpty else { return open(id) }
        if id == selection, let editor {
            editor.reveal(blockID: blockID, atTop: true)
            editor.play(blockID: blockID, at: moment)
            return
        }
        open(id)
        (target, targetAt) = (blockID, moment)
    }

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

    /// show opens a page in the window. What was started on the page
    /// before from its editor, a comment or an ask, goes with it: on
    /// another page it would land on a block that isn't there.
    private func show(_ id: String?) {
        if let held, held.id != id {
            close(held)
            self.held = nil
        }
        // A page New Recording made records only as it opens: another
        // shown first, it no longer does.
        if recordOnOpen != id { recordOnOpen = nil }
        selection = id
        (target, targetAt) = (nil, -1)
        newComment = nil
        asking = nil
        editingBlock = nil
        renamingBlock = nil
        editingNotes = nil
        editingLink = nil
    }

    /// keep drops the pages not among ids from the history, as they are
    /// deleted.
    func keep(_ ids: Set<String>) {
        back.removeAll { !ids.contains($0) }
        forward.removeAll { !ids.contains($0) }
        if let id = selection, !ids.contains(id) { show(back.popLast()) }
    }

    /// showPanel shows the panel at tab, or hides it when it already shows
    /// that tab, as ⌘J and ⇧⌘M do on the desktop.
    func showPanel(_ tab: InspectorTab) {
        // With no page open the panel is the workspace agent's chat alone,
        // whatever tab was last shown: the agent's shortcut shows or hides
        // it, and a page's other tabs have nothing to show.
        guard selection != nil else {
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

    /// askWorkspace shows the panel at the workspace agent's chat, its
    /// composer taking the keyboard; as a sheet of the sidebar's, with
    /// sheet, where no page is beside it for the panel.
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

    /// openFromChat opens page id, at block and a moment of it when given,
    /// from a link or an edit in an agent's chat: the workspace agent's,
    /// with workspace, stays beside it, the panel at its Agent tab at All
    /// pages, and a chat as a sheet goes first, as it would cover the page.
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

    /// openSettings shows Settings. With the panel a sheet, as on iPhone,
    /// the panel goes first: Settings, a sheet of the window's, can't show
    /// over it. It shows once the panel has gone.
    func openSettings() { showOverPanel(.settings) }

    /// newFolder asks for a new folder's name, the panel as a sheet gone
    /// first, as for Settings.
    func newFolder() { showOverPanel(.newFolder) }

    private func showOverPanel(_ what: AfterPanel) {
        guard panelSheets > 0 else { return show(what) }
        // A sheet still closing, a heading picked in it, is waited for as
        // well; one whose going was never heard, not for long.
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

    /// panelShown hears that the panel shows, as a sheet or not.
    func panelShown(asSheet: Bool) {
        if asSheet { panelSheets += 1 }
    }

    /// panelGone hears that the panel has gone, and shows what waited for
    /// it once no sheet of it is left.
    func panelGone(asSheet: Bool) {
        if asSheet { panelSheets = max(panelSheets - 1, 0) }
        guard panelSheets == 0, let what = afterPanel else { return }
        afterPanel = nil
        show(what)
    }

    func search() {
        columns = .all
        searchRequested += 1
    }

    func toggleSidebar() {
        columns = columns == .detailOnly ? .all : .detailOnly
    }
}

extension FocusedValues {
    /// scene is the window in front, for the menu bar.
    @Entry var scene: SceneModel?
}
