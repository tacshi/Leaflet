import Foundation

struct PageRow: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var spaceID: String
    var parentID: String?
    var folderID: String?
    var title: String
    var icon: String?
    var sortKey: String
    var updatedAt: Int64
    var keepOffline: Bool
    var template: Bool
    var tags: [String]
    var canEdit: Bool
    var createdBy: String?
    var defaultTemplateID: String?

    enum CodingKeys: String, CodingKey {
        case id, title, icon, template, tags
        case spaceID = "space_id"
        case parentID = "parent_id"
        case folderID = "folder_id"
        case sortKey = "sort_key"
        case updatedAt = "updated_at"
        case keepOffline = "keep_offline"
        case canEdit = "can_edit"
        case createdBy = "created_by"
        case defaultTemplateID = "default_template_id"
    }

    var updated: Date { Date(timeIntervalSince1970: TimeInterval(updatedAt) / 1000) }
}

struct FolderRow: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var parentID: String?
    var name: String
    var sortKey: String
    var defaultTemplateID: String?

    enum CodingKeys: String, CodingKey {
        case id, name
        case parentID = "parent_id"
        case sortKey = "sort_key"
        case defaultTemplateID = "default_template_id"
    }
}

struct FileRow: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var folderID: String?
    var name: String
    var size: Int64

    enum CodingKeys: String, CodingKey {
        case id, name, size
        case folderID = "folder_id"
    }
}

struct TemplateRow: Codable, Hashable, Identifiable, Sendable {
    var key: String
    var title: String
    var hint: String
    var icon: String
    var pageID: String?

    var id: String { key }

    enum CodingKeys: String, CodingKey {
        case key, title, hint, icon
        case pageID = "page_id"
    }

    var symbol: String { Lucide.symbol(icon) }
}

struct PageVersion: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var when: String
    var label: String
    var markdown: String
}

struct ExportedPage: Codable, Sendable {
    var name: String
    var markdown: String
}

struct ExportedPDF: Codable, Sendable {
    var name: String
    var path: String
    var width: Double
    var height: Double
}

struct Conflict: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var pageID: String
    var pageTitle: String
    var kept: String
    var other: String
    var deleted: String?
    var keptHere: Bool

    enum CodingKeys: String, CodingKey {
        case id, kept, other, deleted
        case pageID = "page_id"
        case pageTitle = "page_title"
        case keptHere = "kept_here"
    }

    enum Resolution: String, Sendable {
        case keep
        case thisDevice = "this_device"
        case otherDevice = "other_device"
        case delete
    }
}

struct Member: Codable, Hashable, Sendable {
    var userID: String
    var displayName: String
    var email: String
    var role: String
    var accepted: Bool

    enum CodingKeys: String, CodingKey {
        case email, role, accepted
        case userID = "user_id"
        case displayName = "display_name"
    }
}

struct TagRow: Codable, Hashable, Sendable {
    var tag: String
    var count: Int
    var pages: [String] = []
}

struct LibrarySnapshot: Decodable, Sendable {
    let spaceID: String
    let pages: [PageRow]
    let folders: [FolderRow]
    let tags: [TagRow]
    let files: [FileRow]
    let tree: LibraryTree

    enum CodingKeys: String, CodingKey {
        case pages, folders, tags, files, top, children, shared
        case spaceID = "space_id"
        case inFolder = "in_folder"
    }

    init(
        spaceID: String, pages: [PageRow], folders: [FolderRow], tags: [TagRow],
        files: [FileRow] = [], top: [String] = [], inFolder: [String: [String]] = [:],
        children: [String: [String]] = [:], shared: [String] = []
    ) {
        self.spaceID = spaceID
        self.pages = pages
        self.folders = folders
        self.tags = tags
        self.files = files
        tree = LibraryTree(
            pages: pages, tags: tags, top: top, inFolder: inFolder, children: children,
            shared: shared)
    }

    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(
            spaceID: try c.decode(String.self, forKey: .spaceID),
            pages: try c.decode([PageRow].self, forKey: .pages),
            folders: try c.decode([FolderRow].self, forKey: .folders),
            tags: try c.decode([TagRow].self, forKey: .tags),
            files: try c.decode([FileRow].self, forKey: .files),
            top: try c.decode([String].self, forKey: .top),
            inFolder: try c.decode([String: [String]].self, forKey: .inFolder),
            children: try c.decode([String: [String]].self, forKey: .children),
            shared: try c.decode([String].self, forKey: .shared))
    }

    static let empty = LibrarySnapshot(spaceID: "", pages: [], folders: [], tags: [])
}

struct NewPage: Codable, Sendable {
    var parentID: String = ""
    var folderID: String = ""
    var afterID: String = ""
    var title: String = ""
    var tags: [String] = []
    var link = false
    var start = false

    enum CodingKeys: String, CodingKey {
        case title, tags, link, start
        case parentID = "parent_id"
        case folderID = "folder_id"
        case afterID = "after_id"
    }
}

struct SearchHit: Codable, Hashable, Sendable {
    var kind: String
    var title: String
    var pageID: String
    var blockID: String?
    var snippet: String

    enum CodingKeys: String, CodingKey {
        case kind, title, snippet
        case pageID = "page_id"
        case blockID = "block_id"
    }
}

struct Todo: Codable, Hashable, Sendable {
    var pageID: String
    var pageTitle: String
    var blockID: String
    var text: String
    var due: Int64
    var timed: Bool
    var owner: String?

    enum CodingKeys: String, CodingKey {
        case text, due, timed, owner
        case pageID = "page_id"
        case pageTitle = "page_title"
        case blockID = "block_id"
    }

    var dueDate: Date? {
        due == 0 ? nil : Date(timeIntervalSince1970: TimeInterval(due) / 1000)
    }
}

struct TodoGroup: Codable, Hashable, Sendable {
    var section: String
    var title: String
    var todos: [Todo]

    var overdue: Bool { section == "overdue" }
}

struct CoreStatus: Codable, Sendable {
    var userID: String
    var displayName: String
    var email: String
    var spaceID: String
    var signedIn: Bool
    var pending: Int
    var iCloudOn: Bool
    var canSync: Bool

    enum CodingKeys: String, CodingKey {
        case email, pending
        case userID = "user_id"
        case displayName = "display_name"
        case spaceID = "space_id"
        case signedIn = "signed_in"
        case iCloudOn = "icloud_on"
        case canSync = "can_sync"
    }
}

struct OpenedPage: Codable, Sendable {
    var id: String
    var editor: Int64
    var content: DocContent
    var rev: Int64
    var readOnly: Bool
    var unreadable: Bool

    enum CodingKeys: String, CodingKey {
        case id, editor, content, rev, unreadable
        case readOnly = "read_only"
    }
}

struct DocContent: Codable, Hashable, Sendable {
    var document: DocNode
    var meta: JSONValue?
}

struct DocNode: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var type: String
    var data: [String: JSONValue]?
    var children: [DocNode]?
}
