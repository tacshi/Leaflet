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
    }

    private func load() async {
        versions = (try? await app.client?.history(page.id)) ?? []
        if chosen == nil { chosen = versions?.first?.id }
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
        do {
            try await app.client?.restoreVersion(page.id, id: v.id)
            app.show(Toast(message: L.dialogHistoryRestored))
            dismiss()
        } catch {
            app.show(
                Toast(message: L.dialogHistoryRestoreFailed(error: error.localizedDescription)))
        }
    }
}

/// MarkdownPreview shows a page's Markdown as the page shows its blocks,
/// as the desktop's History dialog does: headings, lists, to-dos, quotes
/// and code, with their inline formats.
struct MarkdownPreview: View {
    let markdown: String
    let size: Double
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(Self.lines(markdown).enumerated()), id: \.offset) { _, line in
                view(line)
            }
        }
        .foregroundStyle(palette.text)
        .textSelection(.enabled)
    }

    enum Line {
        case heading(Int, String)
        case item(String, String, Int)
        case quote(String)
        case code(String)
        case table([[String]])
        case text(String)
    }

    /// lines reads the Markdown a block a line, a fenced code block whole.
    static func lines(_ md: String) -> [Line] {
        var out: [Line] = []
        var code: [String]?
        for raw in md.components(separatedBy: "\n") {
            if raw.hasPrefix("```") {
                if let c = code {
                    out.append(.code(c.joined(separator: "\n")))
                    code = nil
                } else {
                    code = []
                }
                continue
            }
            if code != nil {
                code?.append(raw)
                continue
            }
            let trimmed = raw.drop { $0 == " " }
            let depth = (raw.count - trimmed.count) / 2
            let line = String(trimmed)
            if line.isEmpty || line == "<details>" || line == "</details>" { continue }
            if line.hasPrefix("|") {
                // A table's rows, its separator left out.
                var row = line.trimmingCharacters(in: .whitespaces)
                if row.hasPrefix("|") { row.removeFirst() }
                if row.hasSuffix("|") { row.removeLast() }
                let cells = row.components(separatedBy: "|")
                    .map { $0.trimmingCharacters(in: .whitespaces) }
                if cells.contains(where: { $0.contains("-") }),
                    cells.allSatisfy({ $0.allSatisfy { $0 == "-" || $0 == ":" } })
                {
                    continue
                }
                if case .table(var rows) = out.last {
                    rows.append(cells)
                    out[out.count - 1] = .table(rows)
                } else {
                    out.append(.table([cells]))
                }
                continue
            }
            if line.hasPrefix("<summary>"), line.hasSuffix("</summary>") {
                out.append(.item("▾", String(line.dropFirst(9).dropLast(10)), depth))
                continue
            }
            // A callout's tone, as GitHub's alerts mark it.
            if line.hasPrefix("> [!"), line.hasSuffix("]") { continue }
            if let hashes = line.firstIndex(of: " "), line[..<hashes].allSatisfy({ $0 == "#" }) {
                out.append(
                    .heading(
                        line.distance(from: line.startIndex, to: hashes),
                        String(line[line.index(after: hashes)...])))
            } else if line.hasPrefix("- [ ] ") || line == "- [ ]" {
                out.append(.item("☐", String(line.dropFirst(6)), depth))
            } else if line.hasPrefix("- [x] ") || line == "- [x]" {
                out.append(.item("☑", String(line.dropFirst(6)), depth))
            } else if line.hasPrefix("- ") || line == "-" {
                out.append(.item("•", String(line.dropFirst(2)), depth))
            } else if let dot = line.firstIndex(of: "."), line[..<dot].allSatisfy(\.isNumber),
                !line[..<dot].isEmpty
            {
                out.append(
                    .item(
                        String(line[...dot]),
                        String(line[line.index(after: dot)...]).trimmingCharacters(
                            in: .whitespaces), depth))
            } else if line.hasPrefix("> ") {
                out.append(.quote(String(line.dropFirst(2))))
            } else {
                out.append(.text(line))
            }
        }
        if let c = code { out.append(.code(c.joined(separator: "\n"))) }
        return out
    }

    @ViewBuilder private func view(_ line: Line) -> some View {
        switch line {
        case .heading(let level, let text):
            inline(text)
                .font(
                    Fonts.pageText(size: size * [1.75, 1.4, 1.18, 1.05][min(max(level, 1), 4) - 1])
                )
                .fontWeight(.semibold)
                .padding(.top, 8)
        case .item(let marker, let text, let depth):
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(marker).foregroundStyle(palette.textSecondary)
                inline(text)
            }
            .font(Fonts.pageText(size: size))
            .padding(.leading, CGFloat(depth) * EditorStyle.indent)
        case .quote(let text):
            inline(text)
                .font(Fonts.pageText(size: size)).italic()
                .padding(.leading, 12)
                .overlay(alignment: .leading) {
                    Capsule().fill(palette.accentSoftBorder).frame(width: 3)
                }
        case .code(let text):
            Text(text)
                .font(.system(size: size * 0.85, design: .monospaced))
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(palette.code, in: .rect(cornerRadius: 8))
        case .table(let rows):
            Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
                ForEach(rows.indices, id: \.self) { r in
                    GridRow {
                        ForEach(rows[r].indices, id: \.self) { c in
                            inline(rows[r][c])
                                .font(Fonts.pageText(size: size * 0.88))
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
        case .text(let text):
            inline(text).font(Fonts.pageText(size: size))
        }
    }

    private func inline(_ text: String) -> Text {
        Text(
            (try? AttributedString(
                markdown: text,
                options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
                ?? AttributedString(text))
    }
}
