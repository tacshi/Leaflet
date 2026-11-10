import Foundation

struct LibraryTree: Sendable {
    let byID: [String: PageRow]
    let children: [String: [PageRow]]
    let inFolder: [String: [PageRow]]
    let top: [PageRow]
    let shared: [PageRow]
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

/// The folders as the sidebar arranges them: folders in folders, roots at the top level.
struct FolderTree: Sendable {
    let byID: [String: FolderRow]
    let roots: [FolderRow]
    let children: [String: [FolderRow]]
    let parent: [String: String]

    init(folders: [FolderRow], roots: [String], in children: [String: [String]]) {
        let byID = Dictionary(folders.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        func rows(_ ids: [String]) -> [FolderRow] { ids.compactMap { byID[$0] } }
        self.byID = byID
        self.roots = rows(roots)
        self.children = children.mapValues(rows)
        var parent: [String: String] = [:]
        for (id, ins) in children {
            for child in ins { parent[child] = id }
        }
        self.parent = parent
    }
}

extension LibrarySnapshot {
    func own(_ page: PageRow) -> Bool { spaceID.isEmpty || page.spaceID == spaceID }

    var rootFolders: [FolderRow] { folderTree.roots }

    func subfolders(of folder: FolderRow) -> [FolderRow] { folderTree.children[folder.id] ?? [] }

    func folder(_ id: String) -> FolderRow? { folderTree.byID[id] }

    /// Whether the folder id is ancestor or a folder somewhere inside it.
    func folder(_ id: String, isIn ancestor: String) -> Bool {
        var seen: Set<String> = []
        var at: String? = id
        while let cursor = at, seen.insert(cursor).inserted {
            if cursor == ancestor { return true }
            at = folderTree.parent[cursor]
        }
        return false
    }

    /// Every folder in sidebar order, each with how deep it is.
    var folderOrder: [(folder: FolderRow, depth: Int)] {
        var out: [(folder: FolderRow, depth: Int)] = []
        var seen: Set<String> = []
        func add(_ folders: [FolderRow], depth: Int) {
            for folder in folders where seen.insert(folder.id).inserted {
                out.append((folder, depth))
                add(subfolders(of: folder), depth: depth + 1)
            }
        }
        add(rootFolders, depth: 0)
        return out
    }

    var topPages: [PageRow] { tree.top }

    func pages(in folder: FolderRow) -> [PageRow] { tree.inFolder[folder.id] ?? [] }

    func children(of page: PageRow) -> [PageRow] { tree.children[page.id] ?? [] }

    var sharedPages: [PageRow] { tree.shared }

    func pages(tagged tag: String) -> [PageRow] { tree.tagged[tag] ?? [] }

    func page(_ id: String) -> PageRow? { tree.byID[id] }

    func files(in folder: FolderRow) -> [FileRow] { files.filter { $0.folderID == folder.id } }

    var topFiles: [FileRow] {
        let held = Set(folders.map(\.id))
        return files.filter { !held.contains($0.folderID ?? "") }
    }

    func rows(of pages: [PageRow], open: Set<String>) -> [TreeRow] {
        var out: [TreeRow] = []
        var seen: Set<String> = []
        func add(_ pages: [PageRow], depth: Int) {
            for page in pages where seen.insert(page.id).inserted {
                let under = children(of: page)
                out.append(TreeRow(page: page, depth: depth, folds: !under.isEmpty))
                if open.contains(page.id) { add(under, depth: depth + 1) }
            }
        }
        add(pages, depth: 0)
        return out
    }

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

struct TreeRow: Identifiable {
    let page: PageRow
    let depth: Int
    let folds: Bool
    var id: String { page.id }
}

extension PageRow {
    var displayTitle: String {
        title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? L.commonUntitled : title
    }
}
