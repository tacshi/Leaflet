import LeafletCore
import SwiftUI

enum InspectorTab: String, CaseIterable, Identifiable {
    case agent, comments, outline
    var id: String { rawValue }

    var title: String {
        switch self {
        case .agent: L.commonAgent
        case .comments: L.panelTabComments
        case .outline: L.panelTabOutline
        }
    }
}

struct PageScreen: View {
    let pageID: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var model: PageModel?
    @State private var title = ""
    @Environment(SceneModel.self) private var scene
    @FocusState private var titleFocused: Bool
    @State private var uploading = false
    @State private var linking: LinkKind?
    @State private var uploadNext = false
    @State private var embedding = false
    @State private var pageTrailing: CGFloat?
    @Environment(\.horizontalSizeClass) private var sizeClass

    var body: some View {
        let row = app.library.page(pageID)
        Group {
            if let model, model.hasContent {
                PageEditorView(
                    model: model,
                    style: EditorStyle(textSize: app.textSize, palette: palette),
                    createSubpage: { [app, pageID] in await app.createPage(parentID: pageID) },
                    onController: { [scene, model] c in
                        scene.editor = c
                        scene.page = model
                        scene.saveTitle = { [app, pageID, title = $title] in
                            await Self.keep(title.wrappedValue, of: pageID, app: app)
                        }
                        if let block = scene.target {
                            let at = scene.targetAt
                            (scene.target, scene.targetAt) = (nil, -1)
                            goTo(block, at: at, in: c)
                        }
                        if scene.recordOnOpen == pageID {
                            scene.recordOnOpen = nil
                            Recorder.shared.start(pageID: pageID, editor: c)
                        }
                        c.onBlank = { [weak model] in model?.blank = $0 }
                        c.onComment = { [weak scene] blockID, quote in
                            scene?.newComment = NewComment(blockID: blockID, quote: quote)
                            scene?.inspectorTab = .comments
                            scene?.inspectorShown = true
                        }
                        c.onAsk = { [weak scene] focus, selection, agent in
                            scene?.asking = AskRequest(
                                focus: focus, selection: selection, agent: agent)
                        }
                        c.onAddAgent = { [weak scene] in scene?.openSettings() }
                        c.onEditBlock = { [weak scene] in scene?.editingBlock = $0 }
                        c.onRenameBlock = { [weak scene] node, name in
                            scene?.blockName = name
                            scene?.renamingBlock = node
                        }
                        c.onEditNotes = { [weak scene] in scene?.editingNotes = $0 }
                        c.onShare = { [weak scene] in scene?.sharing = $0 }
                        c.client = app.client
                        c.onOpenFile = { [weak scene, app] id in
                            Task {
                                do {
                                    guard let path = try await app.client?.localFile(id) else {
                                        return
                                    }
                                    scene?.quickLook = URL(fileURLWithPath: path)
                                } catch {
                                    app.show(Toast(message: error.localizedDescription))
                                }
                            }
                        }
                        c.onReplaceNotes = { [weak scene] in
                            scene?.replacingNotes = $0
                            scene?.replacingWith = $1
                        }
                        c.onEditSpeaker = { [weak scene] node, from, place in
                            scene?.blockName = from ?? ""
                            scene?.speakerEdit = SpeakerEdit(node: node, from: from, place: place)
                        }
                        c.onEditLink = { [weak scene] in scene?.editingLink = $0 }
                        c.onSummarize = { [weak scene] node, prompt in
                            scene?.asking = AskRequest(
                                focus: [node.id], selection: "", agent: nil, prompt: prompt)
                        }
                        c.pageID = pageID
                        c.onUndoRun = { [weak model, app] in
                            guard let model, let run = model.agents.runToUndo else { return }
                            Task {
                                do {
                                    try await app.client?.undoRun(model.id, runID: run.id)
                                } catch {
                                    app.show(Toast(message: error.localizedDescription))
                                }
                                await model.refreshAgents()
                            }
                        }
                        c.onEditing = { [weak scene] in scene?.writing = $0 }
                        c.onHeading = { [weak scene] in scene?.heading = $0 }
                        c.pageLinks = PageLinks(
                            title: { [app] id in app.library.page(id)?.title },
                            open: { [weak scene, app] id in
                                if app.opens(id) { scene?.open(id) }
                            },
                            openAt: { [weak scene, app] id, block, at in
                                if app.opens(id) { scene?.open(id, at: block, moment: at) }
                            })
                        c.plainCopy = { [app] nodes in try? await app.client?.plainCopy(nodes) }
                        c.copyText = { [app] nodes in try? await app.client?.copyText(nodes) }
                        c.menuItems = { [app, pageID] trigger, give in
                            Task {
                                if let items = try? await app.client?.menuItems(
                                    pageID, trigger: trigger)
                                {
                                    give(items)
                                }
                            }
                        }
                        c.agents = { [weak model] in model?.agents.options ?? [] }
                        c.author = { [app] in app.status?.displayName ?? "" }
                        c.onMenuItem = { key, blockID in menuItem(key, at: blockID) }
                    }
                ) {
                    header(row)
                        .environment(\.palette, palette)
                        .environment(app)
                } footer: {
                    if model.blank, let row, row.canEdit {
                        startWith(row)
                            .environment(\.palette, palette)
                            .environment(app)
                    }
                }
                .ignoresSafeArea([.container, .keyboard], edges: .bottom)
            } else if let failure = model?.failure {
                ContentUnavailableView(failure, systemImage: "exclamationmark.triangle")
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background { palette.surface.ignoresSafeArea() }
        .navigationTitle(row?.displayTitle ?? "")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if let row {
                ToolbarItem(placement: .primaryAction) {
                    RecordControl(
                        pageID: pageID, canRecord: row.canEdit && !(model?.readOnly ?? true),
                        editor: scene.editor)
                }
                ToolbarItem(placement: .primaryAction) { actions(row) }
            }
            ToolbarItem(placement: .primaryAction) {
                Button(LI.showInspector, systemImage: "sidebar.trailing") {
                    scene.inspectorShown.toggle()
                }
                .tint(scene.inspectorShown ? nil : Color.primary)
            }
        }
        .onGeometryChange(for: CGFloat.self) {
            $0.frame(in: .global).maxX
        } action: {
            pageTrailing = $0
        }
        .inspector(
            isPresented: Binding(get: { scene.inspectorShown }, set: { scene.inspectorShown = $0 })
        ) {
            Inspector(
                tab: Binding(get: { scene.inspectorTab }, set: { scene.inspectorTab = $0 }),
                model: model,
                isSheet: sizeClass == .compact,
                pageTrailing: pageTrailing
            )
            .ignoresSafeArea(.keyboard, edges: scene.writing ? .bottom : [])
            .inspectorColumnWidth(min: 280, ideal: 340, max: 640)
            .presentationDetents([.medium, .large])
        }
        .fileImporter(
            isPresented: $uploading, allowedContentTypes: [.item], allowsMultipleSelection: true
        ) {
            result in
            if case .success(let urls) = result { Task { await upload(urls) } }
        }
        .sheet(item: Binding(get: { scene.asking }, set: { scene.asking = $0 })) { request in
            if let model { AskSheet(request: request, model: model) }
        }
        .sheet(item: Binding(get: { scene.editingBlock }, set: { scene.editingBlock = $0 })) {
            blockSheet($0)
        }
        .sheet(item: Binding(get: { scene.editingLink }, set: { scene.editingLink = $0 })) {
            node in
            LinkSheet(kind: "video", current: node.data?["url"]?.string ?? "") { [scene] made in
                guard let editor = scene.editor, let now = editor.node(ofBlock: node.id),
                    now.type == "video", made.data?["url"] != now.data?["url"]
                else { return }
                var data = now.data ?? [:]
                data["url"] = made.data?["url"]
                data["title"] = made.data?["title"] ?? .string("")
                VideoPlayback.shared.stop([editor.player(node.id)])
                editor.setData(data, ofBlock: node.id)
            }
        }
        .modifier(FileDialogs(scene: scene))
        .sheet(item: Binding(get: { scene.editingNotes }, set: { scene.editingNotes = $0 })) {
            node in
            NotesSheet(notes: node.data?["notes"]?.string ?? "", file: node.type == "file") {
                [scene] notes in
                guard let editor = scene.editor, let now = editor.node(ofBlock: node.id),
                    now.type == "video" || now.type == "file"
                else { return }
                var data = now.data ?? [:]
                data["notes"] = notes.isEmpty ? nil : .string(notes)
                if data != (now.data ?? [:]) { editor.setData(data, ofBlock: node.id) }
            }
        }
        .alert(
            L.recRenameTitle,
            isPresented: Binding(
                get: { scene.renamingBlock != nil },
                set: { if !$0 { scene.renamingBlock = nil } }),
            presenting: scene.renamingBlock
        ) { node in
            TextField(
                L.recName, text: Binding(get: { scene.blockName }, set: { scene.blockName = $0 }))
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonRename) { rename(node) }
        }
        .sheet(
            item: $linking,
            onDismiss: {
                if uploadNext {
                    uploadNext = false
                    uploading = true
                }
            }
        ) { link in
            LinkSheet(
                kind: link.id, insert: { [scene] in scene.editor?.insertBlock($0) },
                upload: link.id == "image" ? { uploadNext = true } : nil)
        }
        .sheet(isPresented: $embedding) {
            EmbedSheet(author: app.status?.displayName ?? "") { [scene] in
                scene.editor?.insertBlock($0)
            }
        }
        .task(id: pageID) {
            title = row?.title ?? ""
            if let row, row.canEdit, row.title.isEmpty { titleFocused = true }
            let m = PageModel(id: pageID, app: app)
            model = m
            await m.open()
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3600))
            }
            scene.release(m)
        }
        .onDisappear {
            saveTitle()
        }
        .onChange(of: row?.title) { _, new in
            if !titleFocused, let new { title = new }
        }
    }

    private func actions(_ row: PageRow) -> some View {
        Menu(L.pageActions, systemImage: "ellipsis") {
            PageMenu(page: row, rename: { titleFocused = true })
        }
        .tint(Color.primary)
    }

    private func startWith(_ row: PageRow) -> some View {
        StartWith(
            page: row,
            draft: { [scene] in
                scene.inspectorTab = .agent
                scene.allPages = false
                scene.inspectorShown = true
            },
            addSubpage: { [app, scene, pageID] in
                Task {
                    if let id = await app.createPage(parentID: pageID, link: true) {
                        scene.open(id)
                    }
                }
            },
            upload: { uploading = true },
            apply: { [app, scene, pageID] t in
                Task {
                    await scene.flush()
                    if let taken = await app.applyTemplate(t.key, to: pageID) {
                        title = taken
                    }
                }
            },
            edit: { [app, scene] t in
                Task { if let id = await app.editTemplate(t) { scene.open(id) } }
            })
    }

    private func menuItem(_ key: String, at blockID: String) {
        switch key {
        case "file":
            uploading = true
        case "recording":
            Recorder.shared.start(pageID: pageID, editor: scene.editor)
        case "image", "video", "link_preview":
            linking = LinkKind(id: key)
        case "embed":
            embedding = true
        default:
            Task { [app, scene, pageID] in
                await scene.flush()
                if let taken = await app.insertTemplate(key, into: pageID, after: blockID) {
                    title = taken
                }
            }
        }
    }

    private func upload(_ urls: [URL]) async {
        guard let client = app.client else { return }
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                let node = try await client.addFile(pageID, path: url.path)
                if let editor = scene.editor(of: pageID) {
                    editor.insertBlock(node)
                } else {
                    try await client.appendBlock(pageID, node)
                }
            } catch {
                app.show(Toast(message: L.hostErrorAddFile(error: error.localizedDescription)))
            }
        }
    }

    private func rename(_ node: DocNode) {
        let name = scene.blockName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name != node.data?["title"]?.string else { return }
        var data = node.data ?? [:]
        data["title"] = .string(name)
        scene.editor?.setData(data, ofBlock: node.id)
    }

    @ViewBuilder private func blockSheet(_ node: DocNode) -> some View {
        let save: ([String: JSONValue]) -> Void = { [scene] data in
            scene.editor?.setData(data, ofBlock: node.id)
        }
        if node.type == "chart" {
            ChartSheet(node: node, save: save)
        } else if node.type == "table" {
            TableSheet(node: node) { [scene] table in scene.editor?.replaceBlock(table) }
        } else if node.type == "embed" {
            EmbedSheet(author: app.status?.displayName ?? "", editing: node) { [scene] embed in
                scene.editor?.replaceBlock(embed)
            }
        } else {
            SourceSheet(node: node, save: save)
        }
    }

    @ViewBuilder private func header(_ row: PageRow?) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let row, !app.library.path(to: row).isEmpty {
                Breadcrumbs(path: app.library.path(to: row)) { [scene] in scene.open($0) }
            }
            TextField(L.commonUntitled, text: $title, axis: .vertical)
                .font(Fonts.pageTitle(size: app.textSize))
                .foregroundStyle(palette.text)
                .focused($titleFocused)
                .disabled(!(row?.canEdit ?? false))
                .accessibilityLabel(L.pageTitleLabel)
                .onSubmit(submitTitle)
                .onChange(of: title) { _, new in
                    guard new.contains(where: \.isNewline) else { return }
                    title = new.split(whereSeparator: \.isNewline).joined(separator: " ")
                    Task { submitTitle() }
                }
                .accessibilityIdentifier("pageTitle")
                .onChange(of: titleFocused) {
                    scene.writing = titleFocused
                    if !titleFocused { saveTitle() }
                }
            if let row {
                Byline(
                    page: row, saving: model?.saving ?? false,
                    sharedWith: model?.members.filter { $0.userID != app.status?.userID }.count
                        ?? 0)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func submitTitle() {
        titleFocused = false
        Task { @MainActor [scene] in
            await Task.yield()
            scene.editor?.focusStart()
        }
    }

    private func saveTitle() {
        Task { @MainActor in
            await Task.yield()
            await keepTitle()
        }
    }

    private func keepTitle() async {
        await Self.keep(title, of: pageID, app: app)
    }

    private static func keep(_ title: String, of id: String, app: AppModel) async {
        guard let row = app.library.page(id), row.canEdit, title != row.title else { return }
        await app.rename(row, to: title)
    }

    private func goTo(_ blockID: String, at: Int, in editor: EditorController) {
        Task { @MainActor [weak editor] in
            for _ in 0..<40 {
                guard let editor else { return }
                let view = editor.textView
                if view.window != nil, view.bounds.height > 0 {
                    view.layoutIfNeeded()
                    editor.reveal(blockID: blockID, atTop: true)
                    editor.play(blockID: blockID, at: at)
                    return
                }
                try? await Task.sleep(for: .milliseconds(50))
            }
        }
    }
}

struct Breadcrumbs: View {
    let path: [PageRow]
    var open: (String) -> Void

    var body: some View {
        HStack(spacing: 4) {
            ForEach(path) { p in
                Button(p.displayTitle) { open(p.id) }
                    .buttonStyle(.plain)
                    .lineLimit(1)
                    .hoverEffect()
                Image(systemName: "chevron.right").imageScale(.small)
            }
        }
        .font(.footnote)
        .foregroundStyle(.secondary)
    }
}

struct Inspector: View {
    @Binding var tab: InspectorTab
    let model: PageModel?
    var isSheet = false
    var noPage = false
    var pageTrailing: CGFloat?
    @Environment(SceneModel.self) private var scene
    @Environment(AppModel.self) private var app
    @State private var counted = false
    @State private var leading: CGFloat = 0

    private var beside: Bool {
        guard let pageTrailing else { return true }
        return pageTrailing <= leading + 1
    }

    var body: some View {
        @Bindable var scene = scene
        VStack(spacing: 0) {
            if noPage {
                AgentPanel(model: scene.workspace, isSheet: isSheet)
            } else {
                Picker(selection: $tab) {
                    ForEach(InspectorTab.allCases) { Text($0.title).tag($0) }
                } label: {
                    EmptyView()
                }
                .pickerStyle(.segmented)
                .padding()
                .padding(.top, isSheet ? 14 : 0)
                switch tab {
                case .outline: OutlineList(rows: model?.outline ?? [], isSheet: isSheet)
                case .agent:
                    if scene.allPages {
                        AgentPanel(
                            model: scene.workspace, allPages: $scene.allPages, isSheet: isSheet)
                    } else if let model {
                        AgentPanel(model: model, allPages: $scene.allPages, isSheet: isSheet)
                    }
                case .comments: if let model { CommentsPanel(model: model) }
                }
            }
        }
        .onGeometryChange(for: CGFloat.self) {
            $0.frame(in: .global).minX
        } action: {
            leading = $0
        }
        .modifier(PanelTop(column: !isSheet && beside))
        .task { scene.workspace.start(app) }
        .onAppear {
            counted = isSheet
            scene.panelShown(asSheet: counted)
        }
        .onChange(of: isSheet) {
            scene.panelGone(asSheet: counted)
            counted = isSheet
            scene.panelShown(asSheet: counted)
        }
        .onDisappear {
            scene.focusChat = false
            scene.panelGone(asSheet: counted)
        }
    }
}

private struct PanelTop: ViewModifier {
    let column: Bool

    func body(content: Content) -> some View {
        if #available(iOS 27.1, *), column {
            content.modifier(UnderNoBar())
        } else {
            content
        }
    }
}

@available(iOS 27.1, *)
private struct UnderNoBar: ViewModifier {
    @Environment(\.toolbarVerticalEdge) private var edge

    func body(content: Content) -> some View {
        content
            .padding(.top, edge == nil ? 0 : 17)
            .ignoresSafeArea(.container, edges: edge == nil ? [] : .top)
    }
}

struct OutlineRow: Identifiable, Decodable, Hashable, Sendable {
    let id: String
    let text: String
    let level: Int
    let depth: Int
    let parent: String?
    let hasChildren: Bool

    enum CodingKeys: String, CodingKey {
        case id, text, level, depth, parent
        case hasChildren = "has_children"
    }

    static func rows(_ headings: [DocNode]) -> [OutlineRow] {
        guard !headings.isEmpty, let data = try? JSONEncoder().encode(headings) else { return [] }
        var error: NSError?
        let raw = MobileOutline(String(decoding: data, as: UTF8.self), &error)
        return (try? JSONDecoder().decode([OutlineRow].self, from: Data(raw.utf8))) ?? []
    }
}

struct OutlineList: View {
    let rows: [OutlineRow]
    var isSheet = false
    @State private var closed: Set<String> = []

    var body: some View {
        if rows.isEmpty {
            ContentUnavailableView(
                L.panelTabOutline, systemImage: "list.bullet.indent",
                description: Text(L.outlineEmpty))
        } else {
            OutlineRows(all: rows, shown: visible(rows), isSheet: isSheet, closed: $closed)
        }
    }

    private func visible(_ all: [OutlineRow]) -> [OutlineRow] {
        var hidden = Set<String>()
        return all.filter { row in
            guard let parent = row.parent, hidden.contains(parent) || closed.contains(parent) else {
                return true
            }
            hidden.insert(row.id)
            return false
        }
    }
}

private struct OutlineRows: View {
    let all: [OutlineRow]
    let shown: [OutlineRow]
    let isSheet: Bool
    @Binding var closed: Set<String>
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette

    var body: some View {
        let marked = mark(scene.heading)
        ScrollViewReader { proxy in
            let folds = all.contains(where: \.hasChildren)
            List(shown) { row in
                line(row, current: row.id == marked, folds: folds)
            }
            .listStyle(.plain)
            .onChange(of: marked) {
                guard let marked else { return }
                withAnimation { proxy.scrollTo(marked) }
            }
            .onAppear {
                if let marked { proxy.scrollTo(marked, anchor: .center) }
            }
        }
    }

    private func mark(_ id: String?) -> String? {
        guard id != nil else { return nil }
        let visible = Set(shown.map(\.id))
        let parents = Dictionary(all.map { ($0.id, $0.parent) }, uniquingKeysWith: { a, _ in a })
        var at = id
        while let a = at, !visible.contains(a) { at = parents[a] ?? nil }
        return at
    }

    private func line(_ row: OutlineRow, current: Bool, folds: Bool) -> some View {
        HStack(spacing: 2) {
            if row.hasChildren {
                let open = !closed.contains(row.id)
                Button {
                    withAnimation(.snappy) {
                        if open { closed.insert(row.id) } else { closed.remove(row.id) }
                    }
                } label: {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(palette.textMuted)
                        .rotationEffect(.degrees(open ? 90 : 0))
                        .frame(width: 22, height: 30)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(open ? LI.outlineCollapse : LI.outlineExpand)
            } else if folds {
                Color.clear.frame(width: 22, height: 1)
            }
            Button {
                scene.editor?.reveal(blockID: row.id, atTop: true)
                if isSheet { scene.inspectorShown = false }
            } label: {
                Text(row.text.isEmpty ? L.outlineUntitledHeading : row.text)
                    .font(row.level == 1 || current ? .body.weight(.semibold) : .body)
                    .foregroundStyle(current ? palette.accent : palette.text)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(current ? .isSelected : [])
        }
        .padding(.leading, CGFloat(row.depth) * 14)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(
            current ? palette.accentSoft : .clear, in: .rect(cornerRadius: 10)
        )
        .hoverEffect(.highlight)
        .listRowInsets(EdgeInsets(top: 2, leading: 16, bottom: 2, trailing: 16))
        .listRowBackground(Color.clear)
        .listRowSeparator(current ? .hidden : .automatic)
    }
}

struct LinkKind: Identifiable {
    let id: String
}
