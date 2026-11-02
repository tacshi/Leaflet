import SwiftUI

/// Byline is the line under a page's title, as the desktop's: who created
/// it, when it was edited or that it is saving, whether it is shared, with
/// the user or with how many, its tags, and a button to change them.
struct Byline: View {
    let page: PageRow
    /// saving says the page's edits wait to be saved.
    var saving = false
    /// sharedWith counts who the page is shared with.
    var sharedWith = 0
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var editingTags = false

    var body: some View {
        // The time ("just now", "5 min ago") moves on as the page stays open.
        TimelineView(.everyMinute) { context in
            FlowRow {
                // The page keeps who created it, not who edited it last.
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

    /// creator is who the byline names: the user, for their own pages, as
    /// the desktop names them.
    private var creator: String {
        let me = app.status
        if page.createdBy == nil || page.createdBy == me?.userID, let name = me?.displayName,
            !name.isEmpty
        {
            return name
        }
        return L.pageBylineSomeone
    }

    private var shared: Bool {
        !app.library.spaceID.isEmpty && page.spaceID != app.library.spaceID
    }

    /// humanTime says when, as the desktop's byline does: just now, minutes
    /// ago, today or yesterday at a time, or the date.
    static func humanTime(_ t: Date, now: Date = .now, calendar: Calendar = .current) -> String {
        let d = now.timeIntervalSince(t)
        if d < 60 { return L.pageTimeJustNow }
        if d < 3600 { return L.pageTimeMinutesAgo(count: Int(d / 60)) }
        if calendar.isDate(t, inSameDayAs: now) { return L.pageTimeTodayAt(at: t) }
        if let y = calendar.date(byAdding: .day, value: -1, to: now),
            calendar.isDate(t, inSameDayAs: y)
        {
            return L.pageTimeYesterdayAt(at: t)
        }
        if calendar.component(.year, from: t) == calendar.component(.year, from: now) {
            return L.pageTimeDate(date: t)
        }
        return L.pageTimeDateYear(date: t)
    }
}

/// FlowRow lays its views out in a row that wraps, as the desktop's byline
/// does when it is long.
struct FlowRow: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var line: CGFloat = 0
        var widest: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > 0, x + s.width > width {
                y += line + spacing
                x = 0
                line = 0
            }
            x += s.width + spacing
            line = max(line, s.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: min(widest, width), height: y + line)
    }

    func placeSubviews(
        in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()
    ) {
        var x = bounds.minX
        var y = bounds.minY
        var line: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > bounds.minX, x + s.width > bounds.maxX {
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

/// TagEditor changes a page's tags, as the desktop's token field does: each
/// tag a chip to remove, a field to add one by typing it and Return, and
/// the user's other tags offered to add with a tap. Each change is saved as
/// it is made.
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
        // A tag typed and not yet added goes on as the editor closes:
        // closing it is no reason to lose it.
        .onDisappear(perform: addDraft)
    }

    /// addDraft adds what is typed; tags typed or pasted with commas
    /// between them go in each on its own.
    private func addDraft() {
        let parts = draft.split(separator: ",")
        draft = ""
        for part in parts { add(String(part)) }
    }

    /// suggestions are the user's other tags, those starting with what is
    /// typed first, then those containing it.
    private var suggestions: [String] {
        let have = Set(tags.map { $0.lowercased() })
        let q = draft.trimmingCharacters(in: .whitespaces).lowercased()
        let others = app.library.tags.map(\.tag).filter { !have.contains($0.lowercased()) }
        guard !q.isEmpty else { return Array(others.prefix(12)) }
        return others.filter { $0.lowercased().hasPrefix(q) }
            + others.filter { !$0.lowercased().hasPrefix(q) && $0.lowercased().contains(q) }
    }

    private func add(_ text: String) {
        // As the core keeps tags: without a leading "#", spaces closed up,
        // once whatever their case.
        let tag = text.trimmingCharacters(in: .whitespaces)
            .drop { $0 == "#" }
            .split(separator: " ").joined(separator: " ")
        draft = ""
        guard !tag.isEmpty, !tags.contains(where: { $0.lowercased() == tag.lowercased() }) else {
            return
        }
        save(tags + [tag])
    }

    /// save keeps the tags as next has them; failing, they go back to
    /// what they were, and a toast says why.
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
