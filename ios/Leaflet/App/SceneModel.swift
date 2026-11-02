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
    /// exported is a page's Markdown being saved to a file.
    var exported: ExportedPage?
    /// page is the open page's model, for its edits to reach the core.
    @ObservationIgnored weak var page: PageModel?
    /// saveTitle keeps the open page's title as typed, which the page sets:
    /// the title is otherwise kept only as its field lets the keyboard go.
    @ObservationIgnored var saveTitle: (() async -> Void)?
    /// target is a block the page opening goes to once it shows, as a
    /// to-do's or a search hit's.
    @ObservationIgnored var target: String?
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

    /// release closes a page's model as its view goes: at once when the
    /// window shows another page, or once it does.
    func release(_ model: PageModel) {
        if model.id == selection {
            if let held, held !== model { close(held) }
            held = model
            return
        }
        close(model)
    }

    /// closeHeld closes the page model the window holds, as it closes.
    func closeHeld() {
        if let held { close(held) }
        held = nil
    }

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
    /// page open goes to it at once, another once it shows.
    func open(_ id: String, at blockID: String?) {
        guard let blockID, !blockID.isEmpty else { return open(id) }
        if id == selection, let editor {
            editor.reveal(blockID: blockID, atTop: true)
            return
        }
        open(id)
        target = blockID
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
        selection = id
        target = nil
        newComment = nil
        asking = nil
        editingBlock = nil
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
        if inspectorShown, inspectorTab == tab {
            inspectorShown = false
        } else {
            inspectorTab = tab
            inspectorShown = true
        }
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
