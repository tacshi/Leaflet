import LeafletCore
import SwiftUI

/// InspectorTab is the panel beside a page: the agent, the comments, or
/// the outline, as the desktop's right panel.
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

/// PageScreen is a page open in a window: its title, tags and content,
/// and the panel beside it.
struct PageScreen: View {
    let pageID: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var model: PageModel?
    @State private var title = ""
    @Environment(SceneModel.self) private var scene
    @FocusState private var titleFocused: Bool
    /// uploading picks files to put in the page.
    @State private var uploading = false
    /// linking asks for the link of a picture, a video or a link card to
    /// put at the caret, of its kind; uploadNext picks a picture on the
    /// device instead, once its sheet has gone.
    @State private var linking: LinkKind?
    @State private var uploadNext = false
    /// embedding makes an embed to put at the caret.
    @State private var embedding = false
    /// sizeClass is the page's: the panel's own column is narrow on iPad.
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
                        // The title's binding and not the view: the view
                        // holds the window, which would hold it back.
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
                        // The window holds the editor weakly: the editor's
                        // view keeps it, as long as the page shows. So
                        // onMenuItem may hold this view, and through it the
                        // window, with no cycle; the others take the window
                        // and the page weakly all the same.
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
                        c.onReplaceNotes = { [weak scene] in scene?.replacingNotes = $0 }
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
                            open: { [weak scene] id in scene?.open(id) },
                            openAt: { [weak scene] id, block, at in
                                scene?.open(id, at: block, moment: at)
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
                    // The header is hosted apart from this view, so it is
                    // given what it reads from the environment.
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
                // The page runs on under the keyboard's floating shortcuts
                // bar: the editor keeps its text clear of a keyboard itself.
                .ignoresSafeArea([.container, .keyboard], edges: .bottom)
            } else if let failure = model?.failure {
                ContentUnavailableView(failure, systemImage: "exclamationmark.triangle")
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        // Under the keyboard too, as the editor: the page is given the
        // keyboard's safe area at its foot, which a background style keeps
        // out of, and the hosting view's own background, black in the dark,
        // would show behind the text there.
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
                // Plain as the sidebar's button while the panel is away;
                // the accent says it is open.
                .tint(scene.inspectorShown ? nil : Color.primary)
            }
        }
        .inspector(
            isPresented: Binding(get: { scene.inspectorShown }, set: { scene.inspectorShown = $0 })
        ) {
            Inspector(
                tab: Binding(get: { scene.inspectorTab }, set: { scene.inspectorTab = $0 }),
                model: model,
                isSheet: sizeClass == .compact
            )
            // Writing in the page, the keyboard's bar is no reason for the
            // panel's composer to rise; writing in the panel, it is.
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
                // The block as it is now, with what synced in while the
                // sheet was open; the same link saved changes nothing.
                guard let editor = scene.editor, let now = editor.node(ofBlock: node.id),
                    now.type == "video", made.data?["url"] != now.data?["url"]
                else { return }
                // Another video: its title is learned again, and its player
                // starts again from its picture; its notes stay.
                var data = now.data ?? [:]
                data["url"] = made.data?["url"]
                data["title"] = made.data?["title"] ?? .string("")
                VideoPlayback.shared.stop([node.id])
                editor.setData(data, ofBlock: node.id)
            }
        }
        .modifier(FileDialogs(scene: scene))
        .sheet(item: Binding(get: { scene.editingNotes }, set: { scene.editingNotes = $0 })) {
            node in
            NotesSheet(notes: node.data?["notes"]?.string ?? "", file: node.type == "file") {
                [scene] notes in
                // Into the block as it is now, with what synced in while the
                // sheet was open.
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
            // A new page starts at its title, to name it.
            if let row, row.canEdit, row.title.isEmpty { titleFocused = true }
            let m = PageModel(id: pageID, app: app)
            model = m
            await m.open()
            // The page stays open while the window shows it. SwiftUI ends
            // this task as the view goes, but also, on iPhone, as it moves
            // the page into the navigation stack, where it stays shown: the
            // window closes it once it has moved on.
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

    /// actions are the page's menu, as the desktop's header has it.
    private func actions(_ row: PageRow) -> some View {
        Menu(L.pageActions, systemImage: "ellipsis") {
            PageMenu(page: row, rename: { titleFocused = true })
        }
        .tint(Color.primary)
    }

    /// startWith is a blank page's ways to start it.
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
                    // The title it took shows, though the title has the
                    // caret, as a new page's has: the field keeps it, and
                    // doesn't put back the empty one it had.
                    if let taken = await app.applyTemplate(t.key, to: pageID) {
                        title = taken
                    }
                }
            },
            edit: { [app, scene] t in
                Task { if let id = await app.editTemplate(t) { scene.open(id) } }
            })
    }

    /// menuItem does a "/" item the editor leaves to the page, at block
    /// blockID: a template's blocks, files, a recording, or a block made
    /// from a link or HTML, asked for in a sheet.
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
                // What was typed, the "/" taken out, goes first, for the
                // core to put the blocks in.
                await scene.flush()
                if let taken = await app.insertTemplate(key, into: pageID, after: blockID) {
                    title = taken
                }
            }
        }
    }

    /// upload keeps files with the page and puts them in it at the caret:
    /// pictures as images, others as file cards. Copying one takes a while,
    /// and the window may have moved on: it goes at the end of its page
    /// then.
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

    /// rename names node as typed, unless nothing is: a recording block's
    /// title.
    private func rename(_ node: DocNode) {
        let name = scene.blockName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name != node.data?["title"]?.string else { return }
        var data = node.data ?? [:]
        data["title"] = .string(name)
        scene.editor?.setData(data, ofBlock: node.id)
    }

    /// blockSheet edits a block without text: a chart's data, a table's
    /// cells, or a formula's or a diagram's source.
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

    /// header is what goes above the page's text: where it is, its title
    /// and its tags.
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
                // A title that wraps takes Return as a line break rather
                // than a submit: it is one line all the same. The page
                // takes the keyboard once the keyboard is done with Return.
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
                // The user is among the members, as the page's owner, even
                // on a page only synced: the byline counts the others.
                Byline(
                    page: row, saving: model?.saving ?? false,
                    sharedWith: model?.members.filter { $0.userID != app.status?.userID }.count
                        ?? 0)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// submitTitle keeps the title and goes on into the page, as Return
    /// does on the desktop.
    private func submitTitle() {
        titleFocused = false
        // Once the title has let the keyboard go: SwiftUI takes focus from
        // the title on its next update, which would take it from the page
        // too had the page taken it first.
        Task { @MainActor [scene] in
            await Task.yield()
            scene.editor?.focusStart()
        }
    }

    /// saveTitle keeps the title as the field has it once it lets the
    /// keyboard go: until then the field may hold the last letter typed
    /// back from the title, and the title saved without it would come
    /// back over it.
    private func saveTitle() {
        Task { @MainActor in
            await Task.yield()
            await keepTitle()
        }
    }

    /// keepTitle keeps the title as the field has it now, if it changed:
    /// as the page leaves the screen, or before a copy or an export of it.
    private func keepTitle() async {
        await Self.keep(title, of: pageID, app: app)
    }

    /// keep renames page id to title, if it may and the title changed.
    private static func keep(_ title: String, of id: String, app: AppModel) async {
        guard let row = app.library.page(id), row.canEdit, title != row.title else { return }
        await app.rename(row, to: title)
    }

    /// goTo shows the block the window opened the page at, once the
    /// editor is in the window and laid out: it scrolls to it then, and
    /// plays it from at seconds in, for a link to a moment of it.
    private func goTo(_ blockID: String, at: Int, in editor: EditorController) {
        Task { @MainActor [weak editor] in
            // Its first layout comes as the window draws it, a moment
            // after the editor is made.
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

/// Breadcrumbs are the pages a page is nested in, from the top.
struct Breadcrumbs: View {
    let path: [PageRow]
    /// open opens a page of the path, as the desktop's breadcrumbs do.
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

/// Inspector is the panel beside a page, or, with none open, the
/// workspace agent's chat alone (noPage).
struct Inspector: View {
    @Binding var tab: InspectorTab
    let model: PageModel?
    /// isSheet says it is a sheet over the page, as on iPhone, rather than
    /// a column beside it.
    var isSheet = false
    var noPage = false
    @Environment(SceneModel.self) private var scene
    @Environment(AppModel.self) private var app
    /// counted is what the window was told the panel is, as it showed.
    @State private var counted = false

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
                switch tab {
                case .outline: OutlineList(rows: model?.outline ?? [], isSheet: isSheet)
                case .agent:
                    // The page's agent, or the workspace agent's, of all the
                    // user's pages, as the composer's menu chooses.
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
        .modifier(PanelTop(column: !isSheet))
        .task { scene.workspace.start(app) }
        // Settings, or a new folder, asked for while the panel is a sheet
        // shows once it goes.
        .onAppear {
            counted = isSheet
            scene.panelShown(asSheet: counted)
        }
        .onChange(of: isSheet) {
            // A window made narrow or wide turns the panel into a sheet, or
            // back.
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

/// PanelTop has a panel beside the page take the room its column keeps
/// over it for the bar, which stands by the window's edge on a display
/// with a vertical bar and leaves it empty.
private struct PanelTop: ViewModifier {
    /// column says the panel is a column, and not a sheet.
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
        // In one line with the page's title, as the tabs' own padding
        // leaves them.
        content
            .padding(.top, edge == nil ? 0 : 17)
            .ignoresSafeArea(.container, edges: edge == nil ? [] : .top)
    }
}

/// OutlineRow is a heading in the outline, as the core nests it for the
/// desktop's outline too (MobileOutline): its text and level, how deep it
/// nests, the heading it is in, and whether others are in it. Apart from
/// the view, it is worked out on any thread (PageModel.outline).
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

    /// rows are a page's headings, given in their order, nested.
    static func rows(_ headings: [DocNode]) -> [OutlineRow] {
        guard !headings.isEmpty, let data = try? JSONEncoder().encode(headings) else { return [] }
        var error: NSError?
        let raw = MobileOutline(String(decoding: data, as: UTF8.self), &error)
        return (try? JSONDecoder().decode([OutlineRow].self, from: Data(raw.utf8))) ?? []
    }
}

/// OutlineList is the page's headings, to go to one: those under a heading
/// nested in it and folded with it, as the desktop's outline. rows are the
/// page's model's, worked out as its headings change; the heading the page
/// is at, which changes as it scrolls, is read by the list of rows alone
/// (OutlineRows).
struct OutlineList: View {
    let rows: [OutlineRow]
    var isSheet = false
    /// closed are the headings folded, their own headings hidden.
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

    /// visible are the rows not folded away in a heading above them.
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

/// OutlineRows lists the outline's rows shown, the one of the heading the
/// page is at chosen, kept in view.
private struct OutlineRows: View {
    let all: [OutlineRow]
    let shown: [OutlineRow]
    let isSheet: Bool
    @Binding var closed: Set<String>
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette

    var body: some View {
        let marked = mark(scene.heading)
        // The heading the page is at shows chosen, kept in view; folded
        // away, the heading it is in does.
        ScrollViewReader { proxy in
            // Room for the chevrons only where a heading has some.
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

    /// mark is the row that shows the heading the page is at, id: it, or
    /// the heading it is folded in.
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
                // As a sheet, it is in the way of the page it went to.
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
        // The highlight is the row's own, within the list's margins, which
        // keep to the tabs' edges above: a row's background would run to
        // the screen's edge.
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(
            current ? palette.accentSoft : .clear, in: .rect(cornerRadius: 10)
        )
        .hoverEffect(.highlight)
        .listRowInsets(EdgeInsets(top: 2, leading: 16, bottom: 2, trailing: 16))
        .listRowBackground(Color.clear)
        // Its lines would cross its highlight.
        .listRowSeparator(current ? .hidden : .automatic)
    }
}

/// LinkKind is the kind of block a LinkSheet asks the link of: "image",
/// "video" or "link_preview".
struct LinkKind: Identifiable {
    let id: String
}
