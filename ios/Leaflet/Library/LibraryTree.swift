import Foundation

/// The library as the sidebar arranges it: folders with their pages, the
/// pages nested in pages, and the pages shared with the user.
extension LibrarySnapshot {
    private func sorted(_ pages: some Sequence<PageRow>) -> [PageRow] {
        pages.sorted { ($0.sortKey, $0.id) < ($1.sortKey, $1.id) }
    }

    /// own reports whether a page is in the user's space, not shared with
    /// them by someone else.
    func own(_ page: PageRow) -> Bool { spaceID.isEmpty || page.spaceID == spaceID }

    var sortedFolders: [FolderRow] {
        folders.sorted { ($0.sortKey, $0.id) < ($1.sortKey, $1.id) }
    }

    /// topPages are the user's pages in no folder and no other page.
    var topPages: [PageRow] {
        sorted(pages.filter { own($0) && !$0.template && $0.parentID == nil && $0.folderID == nil })
    }

    func pages(in folder: FolderRow) -> [PageRow] {
        sorted(pages.filter { !$0.template && $0.parentID == nil && $0.folderID == folder.id })
    }

    func children(of page: PageRow) -> [PageRow] {
        sorted(pages.filter { $0.parentID == page.id })
    }

    /// sharedPages are the top pages of what others shared with the user.
    var sharedPages: [PageRow] {
        let ids = Set(pages.map(\.id))
        return sorted(
            pages.filter { !own($0) && ($0.parentID == nil || !ids.contains($0.parentID!)) })
    }

    func pages(tagged tag: String) -> [PageRow] {
        pages.filter { !$0.template && $0.tags.contains(tag) }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    func page(_ id: String) -> PageRow? { pages.first { $0.id == id } }

    /// path is the page's ancestors, from the top, for its breadcrumbs.
    func path(to page: PageRow) -> [PageRow] {
        var out: [PageRow] = []
        var at = page.parentID.flatMap(self.page)
        while let p = at, !out.contains(where: { $0.id == p.id }) {
            out.insert(p, at: 0)
            at = p.parentID.flatMap(self.page)
        }
        return out
    }
}

extension PageRow {
    /// displayTitle is the title, or Untitled for a page without one.
    var displayTitle: String { title.isEmpty ? L.commonUntitled : title }
}

/// TodoSection is a group of the To-dos tab, as the desktop's.
enum TodoSection: CaseIterable {
    case overdue, today, upcoming, noDate

    var title: String {
        switch self {
        case .overdue: L.todosSectionOverdue
        case .today: L.commonToday
        case .upcoming: L.todosSectionUpcoming
        case .noDate: L.todosSectionNoDate
        }
    }

    static func of(_ todo: Todo, now: Date = .now, calendar: Calendar = .current) -> TodoSection {
        guard let due = todo.dueDate else { return .noDate }
        if calendar.isDate(due, inSameDayAs: now) {
            return todo.timed && due < now ? .overdue : .today
        }
        return due < now ? .overdue : .upcoming
    }
}
