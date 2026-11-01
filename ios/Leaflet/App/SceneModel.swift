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
    var newFolderShown = false
    /// moving is the page the Move to sheet is moving.
    var moving: PageRow?
    /// history is the page whose versions show.
    var history: PageRow?
    /// exported is a page's Markdown being saved to a file.
    var exported: ExportedPage?
    /// page is the open page's model, for its edits to reach the core.
    @ObservationIgnored weak var page: PageModel?

    /// flush gives the core what was typed in the open page, and waits
    /// until it has it: a copy, an export or the history then has it too.
    func flush() async {
        if editor?.hasPendingEdit == true { editor?.send() }
        await page?.flush()
    }
    /// searchRequested asks the sidebar to show and focus its search.
    var searchRequested = 0
    /// editor is the open page's editor, for the Format menu.
    var editor: EditorController?
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
        selection = id
    }

    var canGoBack: Bool { !back.isEmpty }
    var canGoForward: Bool { !forward.isEmpty }

    func goBack() {
        guard let previous = back.popLast() else { return }
        if let current = selection { forward.append(current) }
        selection = previous
    }

    func goForward() {
        guard let next = forward.popLast() else { return }
        if let current = selection { back.append(current) }
        selection = next
    }

    /// keep drops the pages not among ids from the history, as they are
    /// deleted.
    func keep(_ ids: Set<String>) {
        back.removeAll { !ids.contains($0) }
        forward.removeAll { !ids.contains($0) }
        if let id = selection, !ids.contains(id) { selection = back.popLast() }
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
