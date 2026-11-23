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
    /// The day as last listed, kept for its title once nothing is left on it.
    @State private var last: TodoGroup?

    private var group: TodoGroup? { app.todos.first { $0.key == key } }

    /// The day's midnight here, or nil for the to-dos with no date.
    private var day: Date? {
        let parts = key.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return nil }
        return Calendar.current.date(
            from: DateComponents(year: parts[0], month: parts[1], day: parts[2]))
    }

    private var title: String {
        if let title = (group ?? last)?.title { return title }
        guard let day else { return L.todosSectionNoDate }
        return day.formatted(.dateTime.month().day())
    }

    var body: some View {
        let todos = group?.todos ?? []
        List {
            Section {
                HStack(spacing: 12) {
                    if let day { Text(L.todosDayDate(day: day)) }
                    if !todos.isEmpty {
                        Text(L.todosDayLeft(count: todos.count))
                            .foregroundStyle(group?.overdue == true ? Color.red : Color.secondary)
                    }
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .listRowBackground(Color.clear)
            }
            .listSectionSeparator(.hidden)
            if todos.isEmpty {
                ContentUnavailableView(
                    L.todosDayDoneTitle, systemImage: "checkmark.circle",
                    description: Text(L.todosDayDoneBody)
                )
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
            }
            ForEach(Self.byPage(todos), id: \.id) { page in
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
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.large)
        .onAppear { app.showsTodos(id, true) }
        .onDisappear { app.showsTodos(id, false) }
        .onChange(of: group, initial: true) { if let group { last = group } }
    }

    /// A page's to-dos on the day.
    struct PageTodos {
        var id: String
        var title: String
        var todos: [Todo]
    }

    /// Puts a day's to-dos under their pages, each page where its first to-do is and its to-dos in
    /// the day's order.
    static func byPage(_ todos: [Todo]) -> [PageTodos] {
        var out: [PageTodos] = []
        var at: [String: Int] = [:]
        for todo in todos {
            if let i = at[todo.pageID] {
                out[i].todos.append(todo)
            } else {
                at[todo.pageID] = out.count
                out.append(PageTodos(id: todo.pageID, title: todo.pageTitle, todos: [todo]))
            }
        }
        return out
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
