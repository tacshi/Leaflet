import SwiftUI

/// A day's to-dos from every page, under their pages: a tap on a page opens it, and one on a
/// to-do opens its page at it.
struct TodoDayScreen: View {
    /// The day as 2026-11-20, or "no-date".
    let key: String
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @State private var id = UUID()
    /// The day as last listed, or as the core gives it once nothing is left on it, kept for its
    /// title and date.
    @State private var last: TodoGroup?

    private var group: TodoGroup? { app.todos.first { $0.key == key } }

    var body: some View {
        let day = group ?? last
        let pages = group?.pages ?? []
        List {
            Section {
                HStack(spacing: 12) {
                    if let date = day?.date { Text(date) }
                    if let group, group.count > 0 {
                        Text(L.todosDayLeft(count: group.count))
                            .foregroundStyle(group.overdue ? Color.red : Color.secondary)
                    }
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .listRowBackground(Color.clear)
            }
            .listSectionSeparator(.hidden)
            if pages.isEmpty {
                ContentUnavailableView(
                    L.todosDayDoneTitle, systemImage: "checkmark.circle",
                    description: Text(L.todosDayDoneBody)
                )
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
            }
            ForEach(pages, id: \.id) { page in
                Section {
                    ForEach(page.todos, id: \.self) { todo in
                        DayTodoRow(
                            todo: todo, canCheck: app.canCheck(todo),
                            finish: { await app.finish(todo) },
                            open: { scene.open(todo.pageID, at: todo.blockID) })
                    }
                } header: {
                    Button {
                        scene.open(page.id)
                    } label: {
                        Label(
                            page.title.isEmpty ? L.commonUntitled : page.title,
                            systemImage: "doc.text"
                        )
                        .lineLimit(1)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
        .background { palette.surface.ignoresSafeArea() }
        .navigationTitle(day?.title ?? "")
        .navigationBarTitleDisplayMode(.large)
        .onAppear { app.showsTodos(id, true) }
        .onDisappear { app.showsTodos(id, false) }
        .onChange(of: group, initial: true) { if let group { last = group } }
        .task(id: group == nil) {
            guard group == nil, let client = app.client else { return }
            if let day = await app.read("to-do day", { try await client.todoDay(key) }) {
                last = day
            }
        }
    }
}

/// A to-do on its day, with its owner and its time of day if it has them: its box checks it off,
/// and a tap on the rest opens its page at it.
private struct DayTodoRow: View {
    let todo: Todo
    let canCheck: Bool
    let finish: () async -> Bool
    let open: () -> Void
    @State private var checked = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Button {
                checked = true
                Task { if await !finish() { checked = false } }
            } label: {
                Image(systemName: checked ? "checkmark.square.fill" : "square")
                    .foregroundStyle(checked ? Color.accentColor : Color.secondary)
                    .contentTransition(.symbolEffect(.replace))
            }
            .buttonStyle(.borderless)
            .disabled(!canCheck || checked)
            .accessibilityLabel(L.commonDone)
            .accessibilityValue(todo.text.isEmpty ? L.todosUntitled : todo.text)
            label
        }
    }

    private var label: some View {
        Button(action: open) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(todo.text.isEmpty ? L.todosUntitled : todo.text)
                    if let owner = todo.owner, !owner.isEmpty {
                        Text(owner).font(.footnote).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
                if todo.timed, let due = todo.dueDate {
                    Text(due, format: .dateTime.hour().minute())
                        .font(.footnote)
                        .monospacedDigit()
                        .foregroundStyle(todo.overdue == true ? Color.red : Color.secondary)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }
}
