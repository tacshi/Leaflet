import LeafletCore
import SwiftUI

struct TableSheet: View {
    let node: DocNode
    let save: BlockChange
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var rows: [[String]] = []
    @FocusState private var focused: Cell?
    @State private var failure: String?
    @State private var original: [[String]]?
    @State private var look = TableLook()
    @State private var originalLook = TableLook()
    /// The column of the cell edited last, which the alignment menu aligns.
    @State private var column: Int?

    private var dirty: Bool { (original.map { $0 != rows } ?? false) || look != originalLook }

    struct Cell: Hashable {
        let row: Int
        let col: Int
    }

    private var cols: Int { rows.first?.count ?? 0 }

    var body: some View {
        NavigationStack {
            ScrollView([.horizontal, .vertical]) {
                Grid(horizontalSpacing: 0, verticalSpacing: 0) {
                    ForEach(rows.indices, id: \.self) { r in
                        GridRow {
                            ForEach(rows[r].indices, id: \.self) { c in
                                field(r, c)
                            }
                        }
                    }
                }
                .clipShape(.rect(cornerRadius: 8))
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border, lineWidth: 1))
                .padding()
            }
            .background(palette.ground)
            .safeAreaInset(edge: .bottom) { controls }
            .navigationTitle(L.hostSlashTable)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.commonSave, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear(perform: load)
        .onChange(of: focused) { _, cell in
            if let cell { column = cell.col }
        }
        .alert(
            failure ?? "",
            isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })
        ) {
            Button(L.commonClose) {}
        }
    }

    private func field(_ r: Int, _ c: Int) -> some View {
        TextField("", text: cell(r, c))
            .accessibilityLabel(L.editorTableCell(row: r + 1, column: c + 1))
            .focused($focused, equals: Cell(row: r, col: c))
            .font(Fonts.pageText(size: BlockLook.textSize * 0.88))
            .fontWeight(r == 0 ? .semibold : .regular)
            .foregroundStyle(palette.text)
            .multilineTextAlignment(look.textAlignment(c))
            .padding(.horizontal, 10)
            .frame(width: 160, alignment: .leading)
            .frame(minHeight: 44)
            .background(r == 0 ? palette.panel : palette.surface)
            .border(palette.divider, width: 0.5)
            .onSubmit { focused = r + 1 < rows.count ? Cell(row: r + 1, col: c) : nil }
    }

    private func cell(_ r: Int, _ c: Int) -> Binding<String> {
        Binding(
            get: { r < rows.count && c < rows[r].count ? rows[r][c] : "" },
            set: { if r < rows.count && c < rows[r].count { rows[r][c] = $0 } })
    }

    private var controls: some View {
        ViewThatFits(in: .horizontal) {
            bar(compact: false)
            bar(compact: true)
        }
        .buttonStyle(.bordered)
        .font(.footnote)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity)
        .background(.bar)
    }

    /// The buttons under the cells; compact, the look's show their icons
    /// alone.
    private func bar(compact: Bool) -> some View {
        HStack(spacing: 8) {
            Button(L.editorTableRow, systemImage: "plus") {
                rows.append(Array(repeating: "", count: cols))
            }
            .accessibilityLabel(L.editorTableAddRow)
            Button(L.editorTableColumn, systemImage: "plus") {
                for r in rows.indices { rows[r].append("") }
            }
            .accessibilityLabel(L.editorTableAddColumn)
            if rows.count > 1 {
                Button(L.editorTableRow, systemImage: "minus") {
                    focused = nil
                    rows.removeLast()
                }
                .accessibilityLabel(L.editorTableRemoveRow)
            }
            if cols > 1 {
                Button(L.editorTableColumn, systemImage: "minus") {
                    focused = nil
                    for r in rows.indices { rows[r].removeLast() }
                    look = look.keeping(columns: cols)
                    if let column, column >= cols { self.column = nil }
                }
                .accessibilityLabel(L.editorTableRemoveColumn)
            }
            if compact {
                lookControls.labelStyle(.iconOnly)
            } else {
                lookControls
            }
        }
    }

    /// The menu that aligns a column and the switch to simple lines.
    private var lookControls: some View {
        Group {
            alignment
            Toggle(L.editorTableSimpleLines, systemImage: "line.3.horizontal", isOn: $look.lines)
                .toggleStyle(.button)
        }
    }

    /// The menu that aligns the column of the cell edited last, left,
    /// centered or right; left puts it back to aligning as text does.
    private var alignment: some View {
        let shown = column.map { look.align($0) }.flatMap { $0.isEmpty ? nil : $0 } ?? "left"
        return Menu {
            Picker(
                L.editorTableAlignment,
                selection: Binding(
                    get: { shown },
                    set: { to in
                        if let column { look = look.aligning(column, to == "left" ? "" : to) }
                    })
            ) {
                ForEach(AlignOption.all) { a in
                    Label(a.title, systemImage: a.symbol).tag(a.id)
                }
            }
        } label: {
            Label(
                L.editorTableAlignment,
                systemImage: AlignOption.all.first { $0.id == shown }?.symbol ?? "text.alignleft")
        }
        .disabled(column == nil)
    }

    private struct AlignOption: Identifiable {
        let id: String
        let symbol: String
        var title: String {
            switch id {
            case "center": L.editorTableAlignCenter
            case "right": L.editorTableAlignRight
            default: L.editorTableAlignLeft
            }
        }

        static let all = [
            AlignOption(id: "left", symbol: "text.alignleft"),
            AlignOption(id: "center", symbol: "text.aligncenter"),
            AlignOption(id: "right", symbol: "text.alignright"),
        ]
    }

    private func load() {
        let raw = (try? JSONEncoder().encode(node)).map { String(decoding: $0, as: UTF8.self) }
        var error: NSError?
        let json = MobileTableRows(raw ?? "{}", &error)
        rows = (try? JSONDecoder().decode([[String]].self, from: Data(json.utf8))) ?? [[""]]
        if rows.isEmpty || cols == 0 { rows = [[""]] }
        original = rows
        look = TableLook(node)
        originalLook = look
    }

    private func apply() {
        guard dirty, let original else { return dismiss() }
        do {
            let was = String(decoding: try JSONEncoder().encode(original), as: UTF8.self)
            let cells = String(decoding: try JSONEncoder().encode(rows), as: UTF8.self)
            let changedLook = look != originalLook ? look : nil
            try save { now in try Self.change(now, from: was, to: cells, look: changedLook) }
            dismiss()
        } catch {
            failure = L.appErrorSave(error: error.localizedDescription)
        }
    }

    /// The table as the page has it now, with the cells the sheet changed
    /// from the rows it opened on and the rows and columns it added or took
    /// off, and the look it gave the table, when it changed it: the rest
    /// stays as others may have left it in the meantime.
    static func change(
        _ table: DocNode, from was: String, to rows: String, look: TableLook? = nil
    ) throws -> DocNode {
        let now = String(decoding: try JSONEncoder().encode(table), as: UTF8.self)
        var error: NSError?
        var raw = MobileChangeTableRows(now, was, rows, &error)
        if let error { throw error }
        if let look {
            let aligns = String(decoding: try JSONEncoder().encode(look.aligns), as: UTF8.self)
            raw = MobileSetTableLook(raw, look.lines ? "lines" : "", aligns, &error)
            if let error { throw error }
        }
        return try JSONDecoder().decode(DocNode.self, from: Data(raw.utf8))
    }
}
