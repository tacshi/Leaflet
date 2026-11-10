import LeafletCore
import SwiftUI
import UniformTypeIdentifiers

struct PageMenu: View {
    let page: PageRow
    let rename: () -> Void
    var openInNewWindow: (() -> Void)?
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene

    var body: some View {
        if page.canEdit {
            Button(L.sidebarAddSubpage, systemImage: "plus") {
                Task {
                    if let id = await app.createPage(parentID: page.id, link: true) {
                        scene.open(id)
                    }
                }
            }
            if !page.template {
                DefaultTemplateMenu(
                    label: L.templateDefaultForPage, current: page.defaultTemplateID
                ) {
                    await app.setDefaultTemplate($0, page: page)
                }
            }
            Button(L.sidebarRenameEllipsis, systemImage: "pencil", action: rename)
            if !page.template {
                Button(L.sidebarMoveTo, systemImage: "arrow.right.doc.on.clipboard") {
                    scene.moving = page
                }
            }
        }
        if !page.template {
            Button(L.sidebarDuplicate, systemImage: "plus.square.on.square") {
                Task {
                    await scene.flush()
                    if let id = await app.duplicate(page) { scene.open(id) }
                }
            }
            Button(L.sidebarSaveAsTemplate, systemImage: "rectangle.3.group") {
                Task {
                    await scene.flush()
                    await app.saveAsTemplate(page) { [scene] in scene.open($0) }
                }
            }
        }
        if let openInNewWindow, UIApplication.shared.supportsMultipleScenes {
            Button(LI.openInNewWindow, systemImage: "macwindow.badge.plus", action: openInNewWindow)
        }
        Divider()
        Button(L.sidebarPageHistory, systemImage: "clock.arrow.circlepath") {
            Task {
                await scene.flush()
                scene.history = page
            }
        }
        Menu(L.sidebarExport, systemImage: "square.and.arrow.up") {
            Button(L.sidebarExportMarkdown, systemImage: "doc.plaintext") {
                Task {
                    await scene.flush()
                    do {
                        if let md = try await app.client?.exportMarkdown(page.id) {
                            scene.exported = ExportFile(
                                name: md.name, data: Data(md.markdown.utf8),
                                type: ExportFile.markdown)
                        }
                    } catch {
                        app.show(
                            Toast(message: L.hostErrorExport(error: error.localizedDescription)))
                    }
                }
            }
            Button(L.sidebarExportPDF, systemImage: "doc.richtext") {
                Task {
                    await scene.flush()
                    do {
                        let region = Locale.current.region?.identifier ?? ""
                        guard let printed = try await app.client?.exportPDF(page.id, region: region)
                        else { return }
                        app.show(Toast(message: L.hostExportingPDF(name: printed.name)))
                        scene.exported = ExportFile(
                            name: printed.name, data: try await PagePDF.make(printed), type: .pdf)
                    } catch {
                        app.show(
                            Toast(message: L.hostErrorExport(error: error.localizedDescription)))
                    }
                }
            }
        }
        if app.syncs, !page.template {
            Toggle(
                isOn: Binding(
                    get: { page.keepOffline || app.allPagesOffline },
                    set: { keep in Task { await app.setKeepOffline(page, keep) } })
            ) {
                Label(L.sidebarKeepOnDevice, systemImage: "arrow.down.circle")
            }
            .disabled(app.allPagesOffline)
        }
        if page.canEdit {
            Divider()
            Button(L.commonDelete, systemImage: "trash", role: .destructive) {
                Task { await app.delete(page) }
            }
        }
    }
}

struct DefaultTemplateMenu: View {
    let label: String
    let current: String?
    let choose: (TemplateRow?) async -> Void
    @Environment(AppModel.self) private var app

    var body: some View {
        let chosen = app.defaultTemplate(current)
        Menu(label, systemImage: "rectangle.3.group") {
            Picker(
                label,
                selection: Binding(
                    get: { chosen?.key ?? "" },
                    set: { key in
                        let t = app.templates.first { $0.key == key }
                        Task { await choose(t) }
                    })
            ) {
                Text(L.templateDefaultBlank).tag("")
                Divider()
                ForEach(app.templates) { t in
                    Label(t.title, systemImage: t.symbol).tag(t.key)
                }
            }
            .pickerStyle(.inline)
        }
    }
}

struct ExportFile: FileDocument {
    static let markdown = UTType(filenameExtension: "md") ?? .plainText
    static let readableContentTypes = [markdown, .pdf]
    var name: String
    var data: Data
    var type: UTType

    init(name: String, data: Data, type: UTType) {
        self.name = name
        self.data = data
        self.type = type
    }

    init(configuration: ReadConfiguration) throws {
        name = configuration.file.filename ?? ""
        data = configuration.file.regularFileContents ?? Data()
        type = configuration.contentType
    }

    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: data)
    }
}

struct HistorySheet: View {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var versions: [PageVersion]?
    @State private var chosen: PageVersion.ID?
    @State private var naming = false
    @State private var name = ""
    @State private var current = ""
    @State private var comparing = false
    @Environment(\.horizontalSizeClass) private var sizeClass

    var body: some View {
        NavigationSplitView {
            Group {
                if let versions, versions.isEmpty {
                    ContentUnavailableView(L.dialogHistoryEmpty, systemImage: "clock")
                } else {
                    List(versions ?? [], selection: $chosen) { v in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(v.when).font(.subheadline.weight(.medium))
                            if !v.label.isEmpty {
                                Text(v.label).font(.caption).foregroundStyle(.secondary)
                                    .lineLimit(2)
                            }
                        }
                    }
                }
            }
            .navigationTitle(L.dialogHistoryTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L.commonClose) { dismiss() }
                }
                if page.canEdit {
                    ToolbarItem(placement: .primaryAction) {
                        Button(L.dialogHistoryNameVersion, systemImage: "bookmark") {
                            name = ""
                            naming = true
                        }
                    }
                }
            }
        } detail: {
            if let v = versions?.first(where: { $0.id == chosen }) {
                Group {
                    if comparing {
                        VersionChanges(old: v.markdown, new: current)
                    } else {
                        ScrollView {
                            MarkdownPreview(markdown: v.markdown, size: app.textSize)
                                .foregroundStyle(palette.text)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(24)
                        }
                    }
                }
                .safeAreaInset(edge: .top, spacing: 0) {
                    Picker(L.dialogHistoryThisVersion, selection: $comparing) {
                        Text(L.dialogHistoryThisVersion).tag(false)
                        Text(L.dialogHistoryChangesSince).tag(true)
                    }
                    .pickerStyle(.segmented)
                    .padding(.horizontal)
                    .padding(.vertical, 8)
                    .background(.bar)
                }
                .navigationTitle(v.when)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    if page.canEdit {
                        ToolbarItem(placement: .primaryAction) {
                            Button(L.dialogHistoryRestore, systemImage: "clock.arrow.circlepath") {
                                Task { await restore(v) }
                            }
                        }
                    }
                }
            }
        }
        .alert(L.dialogHistoryNameVersion, isPresented: $naming) {
            TextField(L.dialogHistoryNamePlaceholder, text: $name)
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonSave) { Task { await keep() } }
        }
        .task { await load() }
        .onChange(of: sizeClass) { chooseFirst() }
    }

    private func load() async {
        versions = (try? await app.client?.history(page.id)) ?? []
        current = (try? await app.client?.currentMarkdown(page.id)) ?? ""
        chooseFirst()
    }

    private func chooseFirst() {
        if chosen == nil, sizeClass == .regular { chosen = versions?.first?.id }
    }

    private func keep() async {
        do {
            try await app.client?.nameVersion(page.id, name: name)
            chosen = nil
            await load()
        } catch {
            app.show(Toast(message: L.dialogHistoryKeepFailed(error: error.localizedDescription)))
        }
    }

    private func restore(_ v: PageVersion) async {
        guard let client = app.client else { return }
        do {
            let kept = try await client.restoreVersion(page.id, id: v.id)
            var toast = Toast(message: L.dialogHistoryRestored)
            if !kept.isEmpty {
                let pageID = page.id
                toast.actionLabel = L.commonUndo
                toast.action = { [app] in
                    do {
                        try await client.restoreVersion(pageID, id: kept)
                    } catch {
                        let why = error.localizedDescription
                        app.show(Toast(message: L.dialogHistoryRestoreFailed(error: why)))
                    }
                }
            }
            app.show(toast)
            dismiss()
        } catch {
            app.show(
                Toast(message: L.dialogHistoryRestoreFailed(error: error.localizedDescription)))
        }
    }
}

struct MarkdownPreview: View {
    let markdown: String
    var size: Double?
    @Environment(\.palette) private var palette
    @State private var last: Read?

    private struct Read {
        let markdown: String
        let rows: [Row]
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(shown.enumerated()), id: \.offset) { _, row in
                view(row)
            }
        }
        .textSelection(.enabled)
        .task(id: markdown) { await read() }
    }

    private var shown: [Row] {
        if let rows = Self.kept[markdown] { return rows }
        if let last, markdown.hasPrefix(last.markdown) {
            return last.rows
        }
        guard (markdown as NSString).length <= Self.readAtOnce else { return [] }
        let rows = Self.rows(Self.blocks(markdown))
        Self.keep(markdown, rows)
        return rows
    }

    private func read() async {
        let markdown = markdown
        if let rows = Self.kept[markdown] {
            last = Read(markdown: markdown, rows: rows)
            return
        }
        guard let rows = await Self.parse(markdown), !Task.isCancelled else { return }
        Self.keep(markdown, rows)
        last = Read(markdown: markdown, rows: rows)
    }

    @concurrent nonisolated private static func parse(_ markdown: String) async -> [Row]? {
        guard !Task.isCancelled else { return nil }
        return rows(blocks(markdown))
    }

    private static let readAtOnce = 2_000

    @MainActor private static var kept: [String: [Row]] = [:]
    @MainActor private static var keptOrder: [String] = []
    private static let keptMost = 32

    private static func keep(_ markdown: String, _ rows: [Row]) {
        if kept.updateValue(rows, forKey: markdown) == nil { keptOrder.append(markdown) }
        while keptOrder.count > keptMost { kept[keptOrder.removeFirst()] = nil }
    }

    struct Row: Sendable {
        let node: DocNode
        let depth: Int
        let number: Int
        var cells: [[String]] = []
    }

    nonisolated static func blocks(_ markdown: String) -> [DocNode] {
        var error: NSError?
        let raw = MobileMarkdownBlocks(markdown, &error)
        return (try? JSONDecoder().decode([DocNode].self, from: Data(raw.utf8))) ?? []
    }

    nonisolated static func rows(_ blocks: [DocNode], depth: Int = 0) -> [Row] {
        var out: [Row] = []
        var number = 0
        for n in blocks {
            number = n.type == "numbered_list" ? number + 1 : 0
            out.append(
                Row(
                    node: n, depth: depth, number: number,
                    cells: n.type == "table" ? TableGrid.cells(n) : []))
            if n.type != "table" { out += rows(n.children ?? [], depth: depth + 1) }
        }
        return out
    }

    static func text(_ node: DocNode) -> AttributedString {
        var out = AttributedString()
        for op in node.data?["delta"]?.array ?? [] {
            guard let insert = op["insert"]?.string else { continue }
            let a = DocumentMapper.inline(op["attributes"])
            var run = AttributedString(insert)
            var intent: InlinePresentationIntent = []
            if a[.leafletBold] != nil { intent.insert(.stronglyEmphasized) }
            if a[.leafletItalic] != nil { intent.insert(.emphasized) }
            if a[.leafletCode] != nil { intent.insert(.code) }
            if a[.leafletStrike] != nil { intent.insert(.strikethrough) }
            if !intent.isEmpty { run.inlinePresentationIntent = intent }
            if a[.leafletUnderline] != nil { run.underlineStyle = .single }
            if let url = EditorStyle.linkTarget(a) { run.link = url }
            out += run
        }
        return out
    }

    private func font(_ scale: Double = 1, style: Font.TextStyle = .body) -> Font {
        size.map { Fonts.pageText(size: $0 * scale) } ?? .system(style)
    }

    @ViewBuilder private func view(_ row: Row) -> some View {
        let n = row.node
        let text = Text(Self.text(n))
        let indent = CGFloat(row.depth) * (size == nil ? 14 : EditorStyle.indent)
        switch n.type {
        case "heading":
            let level = min(max(Int(n.data?["level"]?.int ?? 1), 1), 4)
            text
                .font(font([1.75, 1.4, 1.18, 1.05][level - 1], style: .headline))
                .fontWeight(.semibold)
                .padding(.top, size == nil ? 2 : 8)
                .padding(.leading, indent)
        case "bulleted_list", "numbered_list", "todo_list", "toggle_list":
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(Self.marker(row)).foregroundStyle(palette.textSecondary).monospacedDigit()
                text
            }
            .font(font())
            .padding(.leading, indent)
        case "quote", "callout":
            text
                .font(font()).italic(n.type == "quote")
                .padding(.leading, 12)
                .overlay(alignment: .leading) {
                    Capsule().fill(palette.accentSoftBorder).frame(width: 3)
                }
                .padding(.leading, indent)
        case "code", "math_equation", "diagram":
            Text(Self.source(n))
                .font(.system(size: (size ?? 17) * 0.85, design: .monospaced))
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(palette.code, in: .rect(cornerRadius: 8))
                .padding(.leading, indent)
        case "table":
            table(row.cells)
                .padding(.leading, indent)
        case "divider":
            Divider().padding(.vertical, 4)
        default:
            text.font(font()).padding(.leading, indent)
        }
    }

    static func marker(_ row: Row) -> String {
        switch row.node.type {
        case "numbered_list": "\(row.number)."
        case "todo_list": row.node.data?["checked"]?.bool == true ? "☑" : "☐"
        case "toggle_list": "▾"
        default: "•"
        }
    }

    static func source(_ node: DocNode) -> String {
        switch node.type {
        case "math_equation", "diagram":
            node.data?[MobileSourceKey(node.type)]?.string ?? node.text
        default: node.text
        }
    }

    private func table(_ rows: [[String]]) -> some View {
        Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
            ForEach(rows.indices, id: \.self) { r in
                GridRow {
                    ForEach(rows[r].indices, id: \.self) { c in
                        Text(rows[r][c])
                            .font(font(0.88, style: .subheadline))
                            .fontWeight(r == 0 ? .semibold : .regular)
                            .padding(.vertical, 8)
                            .padding(.horizontal, 10)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(r == 0 ? palette.panel : .clear)
                            .border(palette.divider, width: 0.5)
                    }
                }
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border))
    }
}
