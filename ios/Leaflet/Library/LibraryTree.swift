import Foundation

/// LibraryTree is the library as the sidebar arranges it: folders with
/// their pages, the pages nested in pages, and the pages shared with the
/// user. It is worked out once, as the library is read: the sidebar looks
/// pages up row by row, which a search of every page each time makes slow
/// in a large library.
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
    let folders: [FolderRow]
    /// tagged are the pages with each tag, the one changed last first.
    let tagged: [String: [PageRow]]

    init(spaceID: String, pages: [PageRow], folders: [FolderRow]) {
        func own(_ p: PageRow) -> Bool { spaceID.isEmpty || p.spaceID == spaceID }
        let byID = Dictionary(pages.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        let ordered = pages.sorted { ($0.sortKey, $0.id) < ($1.sortKey, $1.id) }
        var children: [String: [PageRow]] = [:]
        var inFolder: [String: [PageRow]] = [:]
        var top: [PageRow] = []
        var shared: [PageRow] = []
        for p in ordered {
            if let parent = p.parentID {
                children[parent, default: []].append(p)
            } else if !p.template, let folder = p.folderID {
                inFolder[folder, default: []].append(p)
            } else if !p.template, own(p) {
                top.append(p)
            }
            // A shared page whose parent the user has not is a top one.
            if !own(p), p.parentID.map({ byID[$0] == nil }) ?? true {
                shared.append(p)
            }
        }
        var tagged: [String: [PageRow]] = [:]
        for p in pages.sorted(by: { $0.updatedAt > $1.updatedAt }) where !p.template {
            for tag in p.tags { tagged[tag, default: []].append(p) }
        }
        self.byID = byID
        self.children = children
        self.inFolder = inFolder
        self.top = top
        self.shared = shared
        self.folders = folders.sorted { ($0.sortKey, $0.id) < ($1.sortKey, $1.id) }
        self.tagged = tagged
    }
}

/// The library as the sidebar arranges it, from its tree.
extension LibrarySnapshot {
    /// own reports whether a page is in the user's space, not shared with
    /// them by someone else.
    func own(_ page: PageRow) -> Bool { spaceID.isEmpty || page.spaceID == spaceID }

    var sortedFolders: [FolderRow] { tree.folders }

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
    /// displayTitle is the title, or Untitled for a page without one.
    var displayTitle: String { title.isEmpty ? L.commonUntitled : title }
}
