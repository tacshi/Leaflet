import Foundation

/// LibraryTree is the library as the sidebar arranges it, as the core gives
/// it (mobile.Library): folders with their pages, the pages nested in
/// pages, the pages shared with the user, and the pages of each tag. Its
/// pages are looked up once, as the library is read: the sidebar asks for
/// them row by row.
struct LibraryTree: Sendable {
    let byID: [String: PageRow]
    /// children are the pages under each page, by its ID, in order.
    let children: [String: [PageRow]]
    /// inFolder are the top-level pages of each folder, by its ID.
    let inFolder: [String: [PageRow]]
    /// top are the user's pages in no folder and no other page.
    let top: [PageRow]
    /// shared are the top pages of what others shared with the user.
    let shared: [PageRow]
    /// tagged are the pages with each tag, by title.
    let tagged: [String: [PageRow]]

    init(
        pages: [PageRow], tags: [TagRow], top: [String], inFolder: [String: [String]],
        children: [String: [String]], shared: [String]
    ) {
        let byID = Dictionary(pages.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        func rows(_ ids: [String]) -> [PageRow] { ids.compactMap { byID[$0] } }
        self.byID = byID
        self.top = rows(top)
        self.shared = rows(shared)
        self.inFolder = inFolder.mapValues(rows)
        self.children = children.mapValues(rows)
        tagged = Dictionary(
            tags.map { ($0.tag, rows($0.pages)) }, uniquingKeysWith: { first, _ in first })
    }
}

/// The library as the sidebar arranges it, from its tree.
extension LibrarySnapshot {
    /// own reports whether a page is in the user's space, not shared with
    /// them by someone else.
    func own(_ page: PageRow) -> Bool { spaceID.isEmpty || page.spaceID == spaceID }

    /// topPages are the user's pages in no folder and no other page.
    var topPages: [PageRow] { tree.top }

    func pages(in folder: FolderRow) -> [PageRow] { tree.inFolder[folder.id] ?? [] }

    func children(of page: PageRow) -> [PageRow] { tree.children[page.id] ?? [] }

    /// sharedPages are the top pages of what others shared with the user.
    var sharedPages: [PageRow] { tree.shared }

    func pages(tagged tag: String) -> [PageRow] { tree.tagged[tag] ?? [] }

    func page(_ id: String) -> PageRow? { tree.byID[id] }

    /// path is the page's ancestors, from the top, for its breadcrumbs.
    func path(to page: PageRow) -> [PageRow] {
        var out: [PageRow] = []
        var seen: Set<String> = [page.id]
        var at = page.parentID.flatMap(self.page)
        while let p = at, seen.insert(p.id).inserted {
            out.insert(p, at: 0)
            at = p.parentID.flatMap(self.page)
        }
        return out
    }
}

extension PageRow {
    /// displayTitle is the title, or Untitled for a page without one, as
    /// the core names it.
    var displayTitle: String {
        title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? L.commonUntitled : title
    }
}
