import SwiftUI

enum VersionDiff {
    struct Row: Hashable, Identifiable, Sendable {
        enum Kind: Sendable { case same, added, removed, gap }

        let id: Int
        let kind: Kind
        let text: String
    }

    @concurrent nonisolated static func found(from old: String, to new: String) async -> [Row] {
        rows(from: old, to: new)
    }

    nonisolated static func rows(from old: String, to new: String, context: Int = 2) -> [Row] {
        let before = old.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        let after = new.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        var removed = Set<Int>()
        var inserted = Set<Int>()
        for change in after.difference(from: before) {
            switch change {
            case .remove(let offset, _, _): removed.insert(offset)
            case .insert(let offset, _, _): inserted.insert(offset)
            }
        }
        guard !removed.isEmpty || !inserted.isEmpty else { return [] }
        var lines: [(kind: Row.Kind, text: String)] = []
        var i = 0
        var j = 0
        while i < before.count || j < after.count {
            if removed.contains(i), i < before.count {
                lines.append((.removed, before[i]))
                i += 1
            } else if inserted.contains(j), j < after.count {
                lines.append((.added, after[j]))
                j += 1
            } else {
                lines.append((.same, before[i]))
                i += 1
                j += 1
            }
        }
        var kept = Set<Int>()
        for (n, line) in lines.enumerated() where line.kind != .same {
            kept.formUnion(max(0, n - context)...min(lines.count - 1, n + context))
        }
        var rows: [Row] = []
        var gap = false
        for (n, line) in lines.enumerated() {
            guard kept.contains(n) else {
                gap = true
                continue
            }
            if gap {
                rows.append(Row(id: rows.count, kind: .gap, text: "⋯"))
                gap = false
            }
            rows.append(Row(id: rows.count, kind: line.kind, text: line.text))
        }
        if gap { rows.append(Row(id: rows.count, kind: .gap, text: "⋯")) }
        return rows
    }
}

struct VersionChanges: View {
    let old: String
    let new: String
    @State private var rows: [VersionDiff.Row]?

    var body: some View {
        Group {
            if let rows, rows.isEmpty {
                ContentUnavailableView(
                    L.dialogHistoryNoChanges, systemImage: "checkmark.circle")
            } else if let rows {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(rows) { line(for: $0) }
                    }
                    .padding(.vertical, 12)
                }
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: [old, new]) {
            rows = await VersionDiff.found(from: old, to: new)
        }
    }

    private func line(for row: VersionDiff.Row) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(mark(row.kind)).foregroundStyle(.secondary).accessibilityHidden(true)
            Text(row.text.isEmpty ? " " : row.text)
                .foregroundStyle(row.kind == .same || row.kind == .gap ? .secondary : .primary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .font(.system(.footnote, design: .monospaced))
        .padding(.horizontal, 16)
        .padding(.vertical, 2)
        .background(tint(row.kind))
    }

    private func mark(_ kind: VersionDiff.Row.Kind) -> String {
        switch kind {
        case .added: "+"
        case .removed: "−"
        case .same, .gap: " "
        }
    }

    private func tint(_ kind: VersionDiff.Row.Kind) -> Color {
        switch kind {
        case .added: .green.opacity(0.15)
        case .removed: .red.opacity(0.15)
        case .same, .gap: .clear
        }
    }
}
