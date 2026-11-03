import LeafletCore
import SwiftUI
import UniformTypeIdentifiers

/// PageMenu is a page's actions, as the desktop's page menu has them, in
/// the sidebar and the page's header.
struct PageMenu: View {
    let page: PageRow
    /// rename starts renaming the page where the menu is.
    let rename: () -> Void
    /// openInNewWindow opens the page in a window of its own, where the
    /// menu offers it.
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
        Button(L.sidebarExportMarkdown, systemImage: "square.and.arrow.up") {
            Task {
                await scene.flush()
                do {
                    scene.exported = try await app.client?.exportMarkdown(page.id)
                } catch {
                    app.show(Toast(message: L.hostErrorExport(error: error.localizedDescription)))
                }
            }
        }
        // As on the desktop, only where pages sync, and never a template;
        // on, and fixed, while every page is kept on the device.
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

/// DefaultTemplateMenu chooses the template new pages in a place start
/// from: a blank page, or one of the templates, as the desktop's submenu.
struct DefaultTemplateMenu: View {
    let label: String
    /// current is the template the place names now, by its page's ID.
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

/// MarkdownFile is a page's Markdown, saved where the user picks.
struct MarkdownFile: FileDocument {
    static let readableContentTypes = [UTType(filenameExtension: "md") ?? .plainText]
    var text: String

    init(_ text: String) { self.text = text }

    init(configuration: ReadConfiguration) throws {
        text = String(decoding: configuration.file.regularFileContents ?? Data(), as: UTF8.self)
    }

    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: Data(text.utf8))
    }
}

/// HistorySheet is a page's kept versions, as the desktop's History
/// dialog: each to read, restore, or keep the page as it is under a name.
struct HistorySheet: View {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var versions: [PageVersion]?
    @State private var chosen: PageVersion.ID?
    @State private var naming = false
    @State private var name = ""
    /// sizeClass is the sheet's: compact, as on iPhone, it shows the list
    /// alone, which a version chosen would push away.
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
                ScrollView {
                    MarkdownPreview(markdown: v.markdown, size: app.textSize)
                        .foregroundStyle(palette.text)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(24)
                }
                .navigationTitle(L.dialogHistoryThisVersion)
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
        chooseFirst()
    }

    /// chooseFirst shows the latest version beside the list, where there
    /// is room for both.
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

    /// restore puts the version back, with Undo in the toast, which
    /// restores the page as it was, kept as a version first: the editor's
    /// own Undo has only the typing and agents' runs.
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

/// MarkdownPreview shows Markdown as the page shows its blocks, read as
/// the core reads it pasted (MobileMarkdownBlocks): headings, lists,
/// to-dos, quotes, code and tables, with their inline formats. History
/// shows a version of a page so, and the Agent panel an answer, a fence
/// still open as it streams in running to its end.
struct MarkdownPreview: View {
    let markdown: String
    /// size is the page's text size, which a version shows in, or nil
    /// for the text styles', as a chat's.
    var size: Double?
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(Self.rows(Self.blocks(markdown)).enumerated()), id: \.offset) { _, row in
                view(row)
            }
        }
        .textSelection(.enabled)
    }

    /// Row is a block as it shows: at its depth, and its number in a
    /// numbered list.
    struct Row {
        let node: DocNode
        let depth: Int
        let number: Int
    }

    /// blocks reads markdown as the core does.
    static func blocks(_ markdown: String) -> [DocNode] {
        var error: NSError?
        let raw = MobileMarkdownBlocks(markdown, &error)
        return (try? JSONDecoder().decode([DocNode].self, from: Data(raw.utf8))) ?? []
    }

    /// rows are blocks one after another, those in a block after it, one
    /// deeper; a numbered item goes on from the one before it at its depth,
    /// as on the page.
    static func rows(_ blocks: [DocNode], depth: Int = 0) -> [Row] {
        var out: [Row] = []
        var number = 0
        for n in blocks {
            number = n.type == "numbered_list" ? number + 1 : 0
            out.append(Row(node: n, depth: depth, number: number))
            // A table's children are its cells.
            if n.type != "table" { out += rows(n.children ?? [], depth: depth + 1) }
        }
        return out
    }

    /// text is a block's text with its formats and links, as the page has
    /// them.
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

    /// font is the font of the text of a block of kind, scaled by scale
    /// from the page's size, or the text style's.
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
            table(TableGrid.rows(n))
                .padding(.leading, indent)
        case "divider":
            Divider().padding(.vertical, 4)
        default:
            text.font(font()).padding(.leading, indent)
        }
    }

    /// marker is what goes before a list item, as the page draws it.
    static func marker(_ row: Row) -> String {
        switch row.node.type {
        case "numbered_list": "\(row.number)."
        case "todo_list": row.node.data?["checked"]?.bool == true ? "☑" : "☐"
        case "toggle_list": "▾"
        default: "•"
        }
    }

    /// source is a code block's, a formula's or a diagram's text.
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
