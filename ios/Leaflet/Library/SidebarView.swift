import SwiftUI

/// SidebarTab is the sidebar's view: the page tree, the tags, or the
/// to-dos of every page.
enum SidebarTab: String, CaseIterable, Identifiable {
    case pages, tags, todos
    var id: String { rawValue }

    var title: String {
        switch self {
        case .pages: L.sidebarTabPages
        case .tags: L.sidebarTabTags
        case .todos: L.todosTitle
        }
    }

    var symbol: String {
        switch self {
        case .pages: "doc.text"
        case .tags: "tag"
        case .todos: "checklist"
        }
    }
}

/// SidebarView is the library: the pages, the tags or the to-dos, and
/// search over them all.
struct SidebarView: View {
    /// stacked says the sidebar stacks over the page, as on iPhone, rather
    /// than standing beside it.
    var stacked = false
    @Environment(AppModel.self) private var app
    @Environment(\.openWindow) private var openWindow
    @Environment(\.palette) private var palette
    @Environment(SceneModel.self) private var scene
    @FocusState private var searchFocused: Bool
    @SceneStorage("sidebar.tab") private var tab = SidebarTab.pages
    /// collapsed are the tags and the to-do groups folded in the Tags and
    /// To-dos tabs, a tag as "tag:" and its name, a to-do group as "todo:"
    /// and its own, one a line.
    @SceneStorage("sidebar.collapsed") private var collapsed = ""
    @State private var query = ""
    @State private var hits: [SearchHit] = []
    /// searching says the hits shown are not yet the query's.
    @State private var searching = false
    @State private var renaming: Renaming?
    @State private var newName = ""
    /// renamingTag is the tag being renamed, from its menu in the Tags tab.
    @State private var renamingTag: String?
    /// id is this sidebar's, by which it tells the app it shows the to-dos.
    @State private var id = UUID()

    /// showsTodos says the sidebar shows the To-dos tab, which the app then
    /// keeps current (AppModel.showsTodos).
    private var showsTodos: Bool { tab == .todos && query.isEmpty }

    /// Renaming is the page or folder being renamed.
    enum Renaming: Identifiable {
        case page(PageRow)
        case folder(FolderRow)
        var id: String {
            switch self {
            case .page(let p): "page-" + p.id
            case .folder(let f): "folder-" + f.id
            }
        }

        /// title and placeholder word the alert, as the desktop's Rename
        /// folder dialog does a folder's.
        var title: String {
            switch self {
            case .page: L.commonRename
            case .folder: L.dialogFolderRename
            }
        }
        var placeholder: String {
            switch self {
            case .page: L.pageTitleLabel
            case .folder: L.dialogFolderName
            }
        }
    }

    var body: some View {
        // Only a page opens: the list tags each folder's row with its ID
        // too, which no page has.
        List(
            selection: Binding(
                get: { scene.selection },
                set: { id in
                    if id == nil || app.library.page(id ?? "") != nil { scene.open(id) }
                })
        ) {
            if !query.isEmpty {
                searchResults
            } else {
                switch tab {
                case .pages: pagesTab
                case .tags: tagsTab
                case .todos: todosTab
                }
            }
        }
        // Stacked, the sidebar's style sets its sections in cards, with
        // room above them: a plain list has the rows reach the edges right
        // under the tabs, as the desktop's sidebar has them.
        .modifier(LibraryListStyle(stacked: stacked))
        .scrollDismissesKeyboard(.interactively)
        // The bar has the desktop's buttons and no title, which they leave
        // no room for: search sits right under it, on iPhone too, with
        // no empty row of large title between.
        .navigationTitle("")
        .navigationBarTitleDisplayMode(.inline)
        .searchable(
            text: $query, placement: .navigationBarDrawer(displayMode: .always),
            prompt: L.appMenuSearch
        )
        .searchFocused($searchFocused)
        .onChange(of: scene.searchRequested) { searchFocused = true }
        .task(id: query) { await search() }
        .onAppear { app.showsTodos(id, showsTodos) }
        .onDisappear { app.showsTodos(id, false) }
        .onChange(of: showsTodos) { app.showsTodos(id, showsTodos) }
        .refreshable { await app.syncNow() }
        .safeAreaInset(edge: .top, spacing: 0) {
            Picker(L.sidebarTabsLabel, selection: $tab) {
                ForEach(SidebarTab.allCases) { Label($0.title, systemImage: $0.symbol).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelStyle(.titleOnly)
            .padding(.horizontal)
            .padding(.bottom, 8)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) { SyncStatusBar() }
        .toolbar {
            // A new page and a new folder, each a button of its own, as
            // the desktop's sidebar has them; plain, as the sidebar's own
            // button.
            ToolbarItemGroup(placement: .primaryAction) {
                Button(L.shellNewPage, systemImage: "square.and.pencil") { newPage() }
                    .tint(Color.primary)
                Button(L.sidebarNewFolder, systemImage: "folder.badge.plus") {
                    scene.newFolder()
                }
                .tint(Color.primary)
            }
        }
        // As the desktop's Rename tag dialog: the name, chosen to type
        // over, and Rename.
        .alert(
            L.dialogTagRename,
            isPresented: Binding(
                get: { renamingTag != nil }, set: { if !$0 { renamingTag = nil } }),
            presenting: renamingTag
        ) { tag in
            TextField(L.dialogTagName, text: $newName)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonRename) {
                let name = newName
                Task {
                    // The tag opens under its new name.
                    if let kept = await app.renameTag(tag, to: name) {
                        expanded("tag:" + kept).wrappedValue = true
                    }
                }
            }
        } message: { _ in
            Text(L.dialogTagRenameSubtitle)
        }
        .alert(
            renaming?.title ?? L.commonRename,
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } }),
            presenting: renaming
        ) { item in
            TextField(item.placeholder, text: $newName)
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonSave) {
                let name = newName
                Task {
                    switch item {
                    case .page(let p): await app.rename(p, to: name)
                    case .folder(let f): await app.renameFolder(f, to: name)
                    }
                }
            }
        }
    }

    private func newPage(parentID: String = "", folderID: String = "") {
        Task {
            if let id = await app.createPage(
                parentID: parentID, folderID: folderID, link: !parentID.isEmpty)
            {
                scene.open(id)
            }
        }
    }

    // MARK: Pages

    @ViewBuilder private var pagesTab: some View {
        let lib = app.library
        Section {
            ForEach(lib.folders) { folder in
                FolderRowView(folder: folder, actions: actions)
            }
            ForEach(lib.topPages) { page in
                PageTree(page: page, actions: actions)
            }
            if lib.topPages.isEmpty && lib.folders.isEmpty {
                Text(L.sidebarNoPages).foregroundStyle(.secondary)
            }
        }
        .withoutTopLine()
        if !lib.sharedPages.isEmpty {
            Section(L.sidebarSharedWithMe) {
                ForEach(lib.sharedPages) { page in
                    PageTree(page: page, actions: actions)
                }
            }
            .withoutTopLine()
        }
    }

    private var actions: RowActions {
        RowActions(
            newSubpage: { newPage(parentID: $0.id) },
            newPageInFolder: { newPage(folderID: $0.id) },
            renamePage: {
                newName = $0.title
                renaming = .page($0)
            },
            renameFolder: {
                newName = $0.name
                renaming = .folder($0)
            },
            openInNewWindow: { openWindow(value: $0) })
    }

    // MARK: Tags

    @ViewBuilder private var tagsTab: some View {
        // Each tag a row of the one section, its pages under it: sections
        // of their own would leave a section's room between the tags.
        Section {
            if app.library.tags.isEmpty {
                Text(L.sidebarNoTags).foregroundStyle(.secondary)
            }
            ForEach(app.library.tags, id: \.tag) { tag in
                let open = expanded("tag:" + tag.tag)
                tagHeader(tag, open: open)
                if open.wrappedValue {
                    ForEach(app.library.pages(tagged: tag.tag)) { page in
                        PageLink(page: page, actions: actions)
                    }
                }
            }
        }
        .withoutTopLine()
    }

    /// tagHeader is a tag's row, heading its pages as the desktop's Tags
    /// tab does: the tag's name in its color, its actions' button,
    /// renaming it or taking it off every page, how many pages have it,
    /// and its own arrow, on a band of the tag's color that stands it
    /// apart from the pages. A tap folds it.
    private func tagHeader(_ tag: TagRow, open: Binding<Bool>) -> some View {
        let fold = { withAnimation(.snappy) { open.wrappedValue.toggle() } }
        let band = Color(TagChip<EmptyView>.color(tag.tag).withAlphaComponent(0.1))
        return HStack {
            Text(tag.tag)
                .font(.callout.weight(.semibold))
                .foregroundStyle(TagChip<EmptyView>.ink(tag.tag, text: palette.text))
                .lineLimit(1)
            Spacer()
            Menu {
                Button(L.tagRenameEllipsis, systemImage: "pencil") {
                    newName = tag.tag
                    renamingTag = tag.tag
                }
                Divider()
                Button(L.tagDelete, systemImage: "trash", role: .destructive) {
                    Task { await app.deleteTag(tag.tag) }
                }
            } label: {
                Image(systemName: "ellipsis")
                    .frame(width: 28, height: 28)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .foregroundStyle(.secondary)
            .accessibilityLabel(L.tagActions)
            Text("\(tag.count)").font(.caption).foregroundStyle(.secondary)
                .accessibilityLabel(L.sidebarTagPages(count: tag.count))
            Button(action: fold) {
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .rotationEffect(.degrees(open.wrappedValue ? 90 : 0))
                    .frame(width: 28, height: 28)
                    .contentShape(.rect)
            }
            .buttonStyle(.borderless)
            .foregroundStyle(.primary)
            .accessibilityLabel(open.wrappedValue ? LI.hideTaggedPages : LI.showTaggedPages)
        }
        .padding(.vertical, 8)
        // The band reaches past the name and the arrow, which stay in line
        // with the pages' rows, and fills the row: one tag's band meets the
        // next.
        .padding(.horizontal, 8)
        .background(band, in: .rect(cornerRadius: 10))
        .padding(.horizontal, -8)
        .contentShape(.rect)
        .onTapGesture(perform: fold)
        .listRowInsets(.vertical, 0)
        .listRowSeparator(.hidden)
        .selectionDisabled()
    }

    /// foldHeader heads a section that folds away, as the sidebar's style
    /// folds its own on iPad, where a plain list does not: its title and
    /// an arrow, which a tap turns.
    private func foldHeader(_ title: String, open: Binding<Bool>) -> some View {
        Button {
            withAnimation(.snappy) { open.wrappedValue.toggle() }
        } label: {
            HStack {
                Text(title)
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .rotationEffect(.degrees(open.wrappedValue ? 90 : 0))
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(.isHeader)
    }

    /// expanded is whether a tag or a to-do group is open, kept with the
    /// window.
    private func expanded(_ key: String) -> Binding<Bool> {
        Binding(
            get: { !collapsed.split(separator: "\n").contains(Substring(key)) },
            set: { open in
                var keys = collapsed.split(separator: "\n").map(String.init).filter { $0 != key }
                if !open { keys.append(key) }
                collapsed = keys.joined(separator: "\n")
            })
    }

    // MARK: To-dos

    @ViewBuilder private var todosTab: some View {
        // The core's sections, in its order, each by date, as the desktop's.
        let groups = app.todos
        if groups.isEmpty {
            ContentUnavailableView(
                L.todosEmptyTitle, systemImage: "checklist", description: Text(L.todosEmptyBody))
        }
        // A group alone has no header, which would only say what the
        // to-dos' dates do, nor a fold to hide them all; but Overdue, which
        // their dates say only in red.
        if groups.count == 1, !groups[0].overdue {
            Section {
                todoRows(groups[0])
            }
            .withoutTopLine()
        } else {
            ForEach(groups, id: \.section) { group in
                let open = expanded("todo:\(group.section)")
                Section {
                    if open.wrappedValue {
                        todoRows(group)
                    }
                } header: {
                    foldHeader(group.title, open: open)
                }
                .withoutTopLine()
            }
        }
    }

    private func todoRows(_ group: TodoGroup) -> some View {
        ForEach(group.todos, id: \.self) { todo in
            TodoRowView(todo: todo, overdue: group.overdue) {
                scene.open(todo.pageID, at: todo.blockID)
            }
        }
    }

    // MARK: Search

    private var searchResults: some View {
        Section {
            if searching && hits.isEmpty {
                ProgressView().frame(maxWidth: .infinity)
            } else if hits.isEmpty {
                ContentUnavailableView.search(text: query)
            }
            searchHits
        }
        .withoutTopLine()
    }

    private var searchHits: some View {
        ForEach(hits, id: \.self) { hit in
            Button {
                scene.open(hit.pageID, at: hit.blockID)
            } label: {
                VStack(alignment: .leading, spacing: 2) {
                    Label(
                        hit.title.isEmpty ? L.commonUntitled : hit.title, systemImage: symbol(hit)
                    )
                    .font(.headline)
                    if !hit.snippet.isEmpty {
                        Text(hit.snippet).font(.subheadline).foregroundStyle(.secondary).lineLimit(
                            2)
                    }
                }
            }
            .buttonStyle(.plain)
        }
    }

    private func symbol(_ hit: SearchHit) -> String {
        switch hit.kind {
        case "template": "doc.on.doc"
        case "recording": "waveform"
        case "comment": "text.bubble"
        default: "doc.text"
        }
    }

    /// search finds the query's hits as it is typed. Each change of the
    /// query cancels the search before: one that answers after it would
    /// show the hits of what was typed before.
    private func search() async {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else {
            hits = []
            searching = false
            return
        }
        searching = true
        // Typing on waits a moment before searching.
        try? await Task.sleep(for: .milliseconds(150))
        guard !Task.isCancelled else { return }
        let found = (try? await app.client?.search(q)) ?? []
        guard !Task.isCancelled else { return }
        hits = found
        searching = false
    }
}

/// LibraryListStyle is the library's list: the sidebar's style beside the
/// page, a plain list stacked over it, as on iPhone, with a little more
/// room at its sides than a plain list has.
private struct LibraryListStyle: ViewModifier {
    let stacked: Bool

    func body(content: Content) -> some View {
        if stacked {
            content.listStyle(.plain)
                .contentMargins(.horizontal, 18, for: .scrollContent)
        } else {
            content.listStyle(.sidebar)
        }
    }
}

extension View {
    /// withoutTopLine leaves out the line a plain list draws over a
    /// section's first row: under the tabs, or a section's title, it parts
    /// nothing.
    fileprivate func withoutTopLine() -> some View {
        listSectionSeparator(.hidden, edges: .top)
    }
}

/// RowActions are what a row's menu does that the sidebar handles.
struct RowActions {
    var newSubpage: (PageRow) -> Void
    var newPageInFolder: (FolderRow) -> Void
    var renamePage: (PageRow) -> Void
    var renameFolder: (FolderRow) -> Void
    var openInNewWindow: (String) -> Void
}

/// PageTree is a page and the pages nested in it, which fold away: each
/// a row of the list, those inside indented by depth.
struct PageTree: View {
    let page: PageRow
    let actions: RowActions
    var depth = 0
    @Environment(AppModel.self) private var app
    @State private var expanded = false

    var body: some View {
        let children = app.library.children(of: page)
        PageLink(
            page: page, actions: actions, depth: depth,
            expanded: children.isEmpty ? nil : $expanded)
        if expanded {
            ForEach(children) { PageTree(page: $0, actions: actions, depth: depth + 1) }
        }
    }
}

/// PageLink is a page's row: its title, which opens it, and its menu.
struct PageLink: View {
    let page: PageRow
    let actions: RowActions
    var depth = 0
    /// expanded shows or hides the page's subpages, nil for one with none.
    var expanded: Binding<Bool>?
    @Environment(AppModel.self) private var app

    /// indent is how far each level of subpages goes in.
    static let indent: CGFloat = 20

    var body: some View {
        HStack(spacing: 8) {
            Label {
                Text(page.displayTitle).lineLimit(1)
            } icon: {
                if let icon = page.icon, !icon.isEmpty {
                    Text(icon)
                } else {
                    Image(systemName: page.template ? "doc.on.doc" : "doc.text")
                }
            }
            Spacer(minLength: 0)
            // Its own chevron rather than a disclosure group's: that one
            // keeps its color on the selected row, where this one, as the
            // title, turns light.
            if let expanded {
                Button {
                    withAnimation(.snappy) { expanded.wrappedValue.toggle() }
                } label: {
                    Image(systemName: "chevron.right")
                        .font(.footnote.weight(.semibold))
                        .rotationEffect(.degrees(expanded.wrappedValue ? 90 : 0))
                        .frame(width: 28, height: 28)
                        .contentShape(.rect)
                }
                .buttonStyle(.borderless)
                .foregroundStyle(.primary)
                .accessibilityLabel(expanded.wrappedValue ? LI.hideSubpages : LI.showSubpages)
            }
        }
        .padding(.leading, CGFloat(depth) * Self.indent)
        .tag(page.id)
        .contextMenu {
            PageMenu(
                page: page, rename: { actions.renamePage(page) },
                openInNewWindow: { actions.openInNewWindow(page.id) })
        } preview: {
            PagePreview(page: page)
        }
        .swipeActions(edge: .trailing) {
            if page.canEdit {
                Button(L.commonDelete, systemImage: "trash", role: .destructive) {
                    Task { await app.delete(page) }
                }
                // Red as deleting is everywhere, over the app's accent.
                .tint(.red)
            }
        }
        .swipeActions(edge: .leading) {
            if page.canEdit {
                Button(L.sidebarAddSubpage, systemImage: "plus") { actions.newSubpage(page) }
                    .tint(.accentColor)
            }
        }
    }
}

/// FolderRowView is a folder and its pages, which fold away.
struct FolderRowView: View {
    let folder: FolderRow
    let actions: RowActions
    @Environment(AppModel.self) private var app
    @State private var expanded = true

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            ForEach(app.library.pages(in: folder)) { PageTree(page: $0, actions: actions) }
        } label: {
            Label(folder.name, systemImage: "folder")
                // A tap opens or closes the folder, which is no page to
                // open: the list leaves the page open selected.
                .selectionDisabled()
                .contentShape(.rect)
                .onTapGesture { withAnimation { expanded.toggle() } }
                .contextMenu {
                    Button(L.sidebarNewPageInFolder, systemImage: "plus") {
                        actions.newPageInFolder(folder)
                    }
                    DefaultTemplateMenu(
                        label: L.templateDefaultForFolder, current: folder.defaultTemplateID
                    ) {
                        await app.setDefaultTemplate($0, folder: folder)
                    }
                    Button(L.sidebarRenameEllipsis, systemImage: "pencil") {
                        actions.renameFolder(folder)
                    }
                    Divider()
                    Button(L.sidebarDeleteFolder, systemImage: "trash", role: .destructive) {
                        Task { await app.deleteFolder(folder) }
                    }
                }
        }
    }
}

/// TodoRowView is an open to-do: its text, its page, and when it is due,
/// in red when that has passed.
struct TodoRowView: View {
    let todo: Todo
    let overdue: Bool
    let open: () -> Void

    var body: some View {
        Button(action: open) {
            VStack(alignment: .leading, spacing: 2) {
                Text(todo.text.isEmpty ? L.todosUntitled : todo.text).lineLimit(2)
                HStack(spacing: 6) {
                    Text(todo.pageTitle.isEmpty ? L.commonUntitled : todo.pageTitle)
                    if let due = todo.dueDate {
                        Text(
                            due,
                            format: todo.timed
                                ? .dateTime.month().day().hour().minute() : .dateTime.month().day()
                        )
                        .foregroundStyle(overdue ? Color.red : Color.secondary)
                    }
                }
                .font(.footnote)
                .foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.plain)
    }
}

/// SyncStatusBar says how sync stands, at the foot of the sidebar.
struct SyncStatusBar: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        HStack {
            status
            Spacer()
        }
        .padding(.horizontal)
        .padding(.vertical, 10)
        .background(.bar)
    }

    private var status: some View {
        HStack(spacing: 8) {
            if app.syncing {
                ProgressView().controlSize(.small)
            } else {
                Image(systemName: symbol).foregroundStyle(.secondary)
            }
            Text(text).font(.footnote).foregroundStyle(.secondary).lineLimit(1)
        }
        .accessibilityElement(children: .combine)
    }

    private var signedIn: Bool { app.syncs }

    private var pending: Int { app.status?.pending ?? 0 }

    /// text says how sync stands, as the desktop's account line does:
    /// working offline, or a pass that failed, before what waits to go.
    private var text: String {
        if app.syncing { return L.sidebarSyncing }
        if !signedIn { return L.sidebarOnDevice }
        if app.workOffline {
            return pending > 0 ? L.pageSyncOfflineQueued(count: pending) : L.pageSyncOffline
        }
        if app.syncFailure != nil { return LI.syncFailed }
        if pending > 0 { return L.sidebarToSync(count: pending) }
        return L.sidebarSynced
    }

    private var symbol: String {
        if !signedIn { return "iphone" }
        if app.workOffline { return "icloud.slash" }
        if app.syncFailure != nil { return "exclamationmark.icloud" }
        return "checkmark.icloud"
    }
}

/// PagePreview is a page as a long press on its row previews it.
struct PagePreview: View {
    let page: PageRow

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(page.displayTitle).font(.title2.weight(.semibold))
            Text(page.updated, format: .relative(presentation: .named))
                .font(.footnote).foregroundStyle(.secondary)
            if !page.tags.isEmpty {
                Text(page.tags.map { "#" + $0 }.joined(separator: " "))
                    .font(.footnote).foregroundStyle(.tint)
            }
        }
        .padding()
        .frame(width: 280, alignment: .leading)
    }
}
