import LeafletCore
import SwiftUI

struct Byline: View {
    let page: PageRow
    var saving = false
    var sharedWith = 0
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var editingTags = false

    var body: some View {
        TimelineView(.everyMinute) { context in
            FlowRow {
                Text(L.pageBylineCreated(name: creator))
                dot
                Text(
                    saving
                        ? L.pageBylineSaving
                        : L.pageBylineEditedAt(
                            when: Self.humanTime(page.updated, now: context.date)))
                if shared {
                    dot
                    Text(L.pageBylineSharedWithYou)
                } else if sharedWith > 0 {
                    dot
                    Text(L.pageBylineSharedWith(count: sharedWith))
                }
                if !page.tags.isEmpty {
                    dot
                    ForEach(page.tags, id: \.self) { TagChip(name: $0) }
                }
                if page.canEdit {
                    Button {
                        editingTags = true
                    } label: {
                        Image(systemName: "tag")
                    }
                    .buttonStyle(.borderless)
                    .hoverEffect()
                    .accessibilityLabel(L.pageAddTag)
                    .popover(isPresented: $editingTags) {
                        TagEditor(page: page)
                            .environment(\.palette, palette)
                            .environment(app)
                            .presentationCompactAdaptation(.popover)
                    }
                }
            }
            .font(.footnote)
            .foregroundStyle(palette.textMuted)
        }
    }

    private var dot: some View { Text("·").foregroundStyle(palette.chartMuted) }

    private var creator: String {
        let me = app.status
        if page.createdBy == nil || page.createdBy == me?.userID, let name = me?.displayName,
            !name.isEmpty
        {
            return name
        }
        return L.pageBylineSomeone
    }

    private var shared: Bool { !app.library.own(page) }

    static func humanTime(_ t: Date, now: Date = .now) -> String {
        func ms(_ d: Date) -> Int64 { Int64((d.timeIntervalSince1970 * 1000).rounded()) }
        return MobileWhen(L.language, ms(t), ms(now))
    }
}

struct FlowRow: Layout {
    var spacing: CGFloat = 6
    private static let slack: CGFloat = 1

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var line: CGFloat = 0
        var widest: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > 0, x + s.width > width + Self.slack {
                y += line + spacing
                x = 0
                line = 0
            }
            x += s.width + spacing
            line = max(line, s.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: width.isFinite ? width : widest, height: y + line)
    }

    func placeSubviews(
        in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()
    ) {
        var x = bounds.minX
        var y = bounds.minY
        var line: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > bounds.minX, x + s.width > bounds.maxX + Self.slack {
                y += line + spacing
                x = bounds.minX
                line = 0
            }
            v.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(s))
            x += s.width + spacing
            line = max(line, s.height)
        }
    }
}

struct TagEditor: View {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var tags: [String] = []
    @State private var draft = ""
    @FocusState private var typing: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(L.pageTagsLabel).font(.headline)
            if !tags.isEmpty {
                FlowRow {
                    ForEach(tags, id: \.self) { tag in
                        TagChip(name: tag) {
                            Button(LI.removeTag(tag), systemImage: "xmark") {
                                save(tags.filter { $0 != tag })
                            }
                            .labelStyle(.iconOnly)
                            .font(.caption2.weight(.bold))
                            .buttonStyle(.borderless)
                            .foregroundStyle(.secondary)
                        }
                        .font(.subheadline.weight(.semibold))
                    }
                }
            }
            TextField(L.pageAddTag, text: $draft)
                .textFieldStyle(.roundedBorder)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.done)
                .focused($typing)
                .onSubmit {
                    addDraft()
                    typing = true
                }
            if !suggestions.isEmpty {
                FlowRow {
                    ForEach(suggestions, id: \.self) { tag in
                        Button {
                            add(tag)
                        } label: {
                            Label(tag, systemImage: "plus")
                                .font(.subheadline)
                                .padding(.horizontal, 9)
                                .padding(.vertical, 4)
                                .overlay(Capsule().stroke(palette.border))
                        }
                        .buttonStyle(.borderless)
                        .foregroundStyle(palette.textSecondary)
                    }
                }
            }
        }
        .padding(16)
        .frame(width: 320, alignment: .leading)
        .onAppear {
            tags = page.tags
            typing = true
        }
        .onDisappear(perform: addDraft)
    }

    private func addDraft() {
        let parts = draft.split(separator: ",")
        draft = ""
        for part in parts { add(String(part)) }
    }

    private var suggestions: [String] {
        let have = Set(tags.map { $0.lowercased() })
        let q = draft.trimmingCharacters(in: .whitespaces).lowercased()
        let others = app.library.tags.map(\.tag).filter { !have.contains($0.lowercased()) }
        guard !q.isEmpty else { return Array(others.prefix(12)) }
        return others.filter { $0.lowercased().hasPrefix(q) }
            + others.filter { !$0.lowercased().hasPrefix(q) && $0.lowercased().contains(q) }
    }

    private func add(_ text: String) {
        let tag = text.trimmingCharacters(in: .whitespaces)
            .drop { $0 == "#" }
            .split(separator: " ").joined(separator: " ")
        draft = ""
        guard !tag.isEmpty, !tags.contains(where: { $0.lowercased() == tag.lowercased() }) else {
            return
        }
        save(tags + [tag])
    }

    private func save(_ next: [String]) {
        let before = tags
        tags = next
        let id = page.id
        Task {
            do {
                try await app.client?.setTags(id, tags: next)
            } catch {
                tags = before
                app.show(Toast(message: L.appErrorSave(error: error.localizedDescription)))
            }
            await app.reload()
        }
    }
}
