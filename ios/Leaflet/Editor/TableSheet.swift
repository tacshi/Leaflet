import LeafletCore
import SwiftUI

/// TableSheet edits a table's cells, as the desktop's table does: each
/// cell's Markdown in a field, the first row the header, Return going down
/// a column, and a row or a column added or taken away at the end.
struct TableSheet: View {
    let node: DocNode
    /// save gives the table with its new cells.
    let save: (DocNode) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var rows: [[String]] = []
    @FocusState private var focused: Cell?
    /// failure is why the cells could not be put in the table, which the
    /// sheet stays open to say, keeping them.
    @State private var failure: String?
    /// original are the cells as the sheet opened with them.
    @State private var original: [[String]]?

    private var dirty: Bool { original.map { $0 != rows } ?? false }

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
            .padding(.horizontal, 10)
            // As wide as the desktop's columns start.
            .frame(width: 160, alignment: .leading)
            .frame(minHeight: 44)
            .background(r == 0 ? palette.panel : palette.surface)
            .border(palette.divider, width: 0.5)
            // A cell holds one paragraph: Return goes to the one below.
            .onSubmit { focused = r + 1 < rows.count ? Cell(row: r + 1, col: c) : nil }
    }

    /// cell is the text of a cell, as long as the table has it: a row or a
    /// column taken away, SwiftUI may still read its fields once, past
    /// the end of rows.
    private func cell(_ r: Int, _ c: Int) -> Binding<String> {
        Binding(
            get: { r < rows.count && c < rows[r].count ? rows[r][c] : "" },
            set: { if r < rows.count && c < rows[r].count { rows[r][c] = $0 } })
    }

    /// controls add and take away the last row or column, as the desktop's
    /// buttons under a table being edited.
    private var controls: some View {
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
                }
                .accessibilityLabel(L.editorTableRemoveColumn)
            }
        }
        .buttonStyle(.bordered)
        .font(.footnote)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity)
        .background(.bar)
    }

    private func load() {
        let raw = (try? JSONEncoder().encode(node)).map { String(decoding: $0, as: UTF8.self) }
        var error: NSError?
        let json = MobileTableRows(raw ?? "{}", &error)
        rows = (try? JSONDecoder().decode([[String]].self, from: Data(json.utf8))) ?? [[""]]
        if rows.isEmpty || cols == 0 { rows = [[""]] }
        original = rows
    }

    /// apply puts the cells in the table as the core does, and saves it.
    private func apply() {
        guard let table = try? JSONEncoder().encode(node),
            let cells = try? JSONEncoder().encode(rows)
        else { return }
        var error: NSError?
        let raw = MobileSetTableRows(
            String(decoding: table, as: UTF8.self), String(decoding: cells, as: UTF8.self), &error)
        guard error == nil,
            let saved = try? JSONDecoder().decode(DocNode.self, from: Data(raw.utf8))
        else {
            failure = L.appErrorSave(error: error?.localizedDescription ?? "")
            return
        }
        save(saved)
        dismiss()
    }
}
