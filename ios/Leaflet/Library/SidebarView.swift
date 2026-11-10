import SwiftUI
import UniformTypeIdentifiers

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

struct SidebarView: View {
    var stacked = false
    @Environment(AppModel.self) private var app
    @Environment(\.openWindow) private var openWindow
    @Environment(\.palette) private var palette
    @Environment(SceneModel.self) private var scene
    @FocusState private var searchFocused: Bool
    @SceneStorage("sidebar.tab") private var tab = SidebarTab.pages
    @SceneStorage("sidebar.collapsed") private var collapsed = ""
    @State private var query = ""
    @State private var hits: [SearchHit] = []
    @State private var searching = false
    @State private var renaming: Renaming?
    @State private var newName = ""
    @State private var renamingTag: String?
    @State private var id = UUID()
    @State private var open: Set<String> = []
    @State private var drops = SidebarDrops()

    private var showsTodos: Bool { tab == .todos && query.isEmpty }

    enum Renaming: Identifiable {
        case page(PageRow)
        case folder(FolderRow)
        var id: String {
            switch self {
            case .page(let p): "page-" + p.id
            case .folder(let f): "folder-" + f.id
            }
        }

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
        .environment(drops)
        .background { SidebarDropHost(drops: drops) }
        .overlay { SidebarDropMark(drops: drops) }
        .modifier(LibraryListStyle(stacked: stacked))
        .scrollDismissesKeyboard(.interactively)
        .navigationTitle("")
        .navigationBarTitleDisplayMode(.inline)
        .searchable(
            text: $query, placement: .navigationBarDrawer(displayMode: .always),
            prompt: L.appMenuSearch
        )
        .searchFocused($searchFocused)
        .onChange(of: scene.searchRequested) { searchFocused = true }
        .task(id: query) { await search() }
        .task { scene.workspace.start(app) }
        .task { drops.app = app }
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
            ToolbarItemGroup(placement: .primaryAction) {
                if stacked {
                    Button(L.shellAskAgent, systemImage: "sparkles") {
                        scene.askWorkspace(sheet: true)
                    }
                    .tint(Color.primary)
                }
                Button(L.shellNewPage, systemImage: "square.and.pencil") { newPage() }
                    .tint(Color.primary)
                Button(L.sidebarNewFolder, systemImage: "folder.badge.plus") {
                    scene.newFolder()
                }
                .tint(Color.primary)
            }
        }
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

    @ViewBuilder private var pagesTab: some View {
        let lib = app.library
        Section {
            ForEach(lib.rootFolders) { folder in
                FolderRowView(folder: folder, actions: actions, open: $open)
            }
            PageRows(pages: lib.topPages, actions: actions, open: $open)
            ForEach(lib.topFiles) { FileRowView(file: $0) }
            if lib.topPages.isEmpty && lib.folders.isEmpty && lib.topFiles.isEmpty {
                Text(L.sidebarNoPages).foregroundStyle(.secondary)
            }
        }
        .withoutTopLine()
        if !lib.sharedPages.isEmpty {
            Section(L.sidebarSharedWithMe) {
                PageRows(pages: lib.sharedPages, actions: actions, open: $open)
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

    @ViewBuilder private var tagsTab: some View {
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
        .padding(.horizontal, 8)
        .background(band, in: .rect(cornerRadius: 10))
        .padding(.horizontal, -8)
        .contentShape(.rect)
        .onTapGesture(perform: fold)
        .listRowInsets(.vertical, 0)
        .listRowSeparator(.hidden)
        .selectionDisabled()
    }

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

    private func expanded(_ key: String) -> Binding<Bool> {
        Binding(
            get: { !collapsed.split(separator: "\n").contains(Substring(key)) },
            set: { open in
                var keys = collapsed.split(separator: "\n").map(String.init).filter { $0 != key }
                if !open { keys.append(key) }
                collapsed = keys.joined(separator: "\n")
            })
    }

    @ViewBuilder private var todosTab: some View {
        let groups = app.todos
        if groups.isEmpty {
            ContentUnavailableView(
                L.todosEmptyTitle, systemImage: "checklist", description: Text(L.todosEmptyBody))
        }
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

    private var searchResults: some View {
        Section {
            if searching && hits.isEmpty {
                ProgressView().frame(maxWidth: .infinity)
            } else if hits.isEmpty {
                ContentUnavailableView.search(text: query)
                if scene.workspace.agents.available {
                    Button(L.dialogSearchAskAgent, systemImage: "sparkles") { askAgent() }
                        .frame(maxWidth: .infinity)
                }
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

    private func askAgent() {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty, let client = app.client else { return }
        query = ""
        searchFocused = false
        if let page = scene.page, scene.selection == page.id, !stacked {
            scene.inspectorTab = .agent
            scene.allPages = false
            scene.focusChat = true
            scene.inspectorShown = true
            if page.agents.running {
                page.chatDraft = q
            } else {
                Task { await client.chat(page.id, text: q, focus: []) }
            }
            return
        }
        scene.askWorkspace(sheet: stacked)
        if scene.workspace.agents.running {
            scene.workspace.chatDraft = q
        } else {
            Task { [scene] in await client.workspaceChat(q, openPageID: scene.selection ?? "") }
        }
    }

    private func search() async {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else {
            hits = []
            searching = false
            return
        }
        searching = true
        try? await Task.sleep(for: .milliseconds(150))
        guard !Task.isCancelled else { return }
        let found = (try? await app.client?.search(q)) ?? []
        guard !Task.isCancelled else { return }
        hits = found
        searching = false
    }
}

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
    fileprivate func withoutTopLine() -> some View {
        listSectionSeparator(.hidden, edges: .top)
    }
}

struct RowActions {
    var newSubpage: (PageRow) -> Void
    var newPageInFolder: (FolderRow) -> Void
    var renamePage: (PageRow) -> Void
    var renameFolder: (FolderRow) -> Void
    var openInNewWindow: (String) -> Void
}

struct PageRows: View {
    let pages: [PageRow]
    let actions: RowActions
    @Binding var open: Set<String>
    @Environment(AppModel.self) private var app

    var body: some View {
        ForEach(app.library.rows(of: pages, open: open)) { row in
            PageLink(
                page: row.page, actions: actions, depth: row.depth,
                expanded: row.folds ? expanded(row.page.id) : nil
            )
            .modifier(PageDrops(page: row.page))
        }
    }

    private func expanded(_ id: String) -> Binding<Bool> {
        Binding(
            get: { open.contains(id) },
            set: { shown in
                if shown { open.insert(id) } else { open.remove(id) }
            })
    }
}

struct PageLink: View {
    let page: PageRow
    let actions: RowActions
    var depth = 0
    var expanded: Binding<Bool>?
    @Environment(AppModel.self) private var app

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

struct FolderRowView: View {
    let folder: FolderRow
    let actions: RowActions
    @Binding var open: Set<String>
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @State private var expanded = true
    @State private var uploading = false

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            ForEach(app.library.subfolders(of: folder)) {
                FolderRowView(folder: $0, actions: actions, open: $open)
            }
            PageRows(pages: app.library.pages(in: folder), actions: actions, open: $open)
            ForEach(app.library.files(in: folder)) { FileRowView(file: $0) }
        } label: {
            Label(folder.name, systemImage: "folder")
                .frame(maxWidth: .infinity, alignment: .leading)
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
                    Button(L.sidebarNewFolderInFolder, systemImage: "folder.badge.plus") {
                        scene.newFolder(in: folder.id)
                    }
                    Button(L.sidebarUploadFile, systemImage: "square.and.arrow.up") {
                        uploading = true
                    }
                    Button(L.sidebarRenameEllipsis, systemImage: "pencil") {
                        actions.renameFolder(folder)
                    }
                    Button(L.sidebarMoveTo, systemImage: "folder") {
                        scene.movingFolder = folder
                    }
                    Divider()
                    Button(L.sidebarDeleteFolder, systemImage: "trash", role: .destructive) {
                        Task { await app.deleteFolder(folder) }
                    }
                }
                .draggable(FolderRow.dragPrefix + folder.id) {
                    Label(folder.name, systemImage: "folder").padding(8)
                }
                .modifier(SidebarDropRow(key: SidebarDrops.folderKey(folder.id)))
                .fileImporter(
                    isPresented: $uploading, allowedContentTypes: [.item],
                    allowsMultipleSelection: true
                ) { result in
                    if case .success(let urls) = result {
                        Task { await app.upload(urls, folderID: folder.id) }
                    }
                }
        }
    }
}

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
