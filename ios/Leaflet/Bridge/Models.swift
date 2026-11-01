import Foundation

// The values the core gives the app and takes from it, as JSON. They
// mirror the types of the mobile package, and docs/protocol.md §1 for a
// page's content; keys are spelled out, since a block's data keys must
// stay as they are.

/// PageRow is a page as the library lists it.
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
    /// createdBy is the user who made the page, or nil.
    var createdBy: String?
    /// defaultTemplateID is the template pages made under it start from.
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

/// FolderRow is a folder as the library lists it.
struct FolderRow: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var parentID: String?
    var name: String
    var sortKey: String
    /// defaultTemplateID is the template pages made in it start from.
    var defaultTemplateID: String?

    enum CodingKeys: String, CodingKey {
        case id, name
        case parentID = "parent_id"
        case sortKey = "sort_key"
        case defaultTemplateID = "default_template_id"
    }
}

/// TemplateRow is a template to start a page from: the user's own, or one
/// that comes with Leaflet, named by its key.
struct TemplateRow: Codable, Hashable, Identifiable, Sendable {
    var key: String
    var title: String
    var hint: String
    /// icon is a Lucide icon's name, as the desktop shows it.
    var icon: String
    /// pageID is the template's page, for the user's own.
    var pageID: String?

    var id: String { key }
    var starter: Bool { pageID == nil }

    enum CodingKeys: String, CodingKey {
        case key, title, hint, icon
        case pageID = "page_id"
    }

    /// symbol is the SF Symbol for the template's icon.
    var symbol: String { Lucide.symbol(icon) }
}

/// PageVersion is a kept version of a page, newest first.
struct PageVersion: Codable, Hashable, Identifiable, Sendable {
    var id: String
    /// when and label say when it was kept, and its name or why.
    var when: String
    var label: String
    var markdown: String
}

/// ExportedPage is a page as Markdown, and the file name to save it as.
struct ExportedPage: Codable, Sendable {
    var name: String
    var markdown: String
}

/// Conflict is a block two devices changed at once, as sync merged it:
/// kept is the start of its text as kept, other the side's that was not,
/// or deleted names the side that deleted it, "here" or "there".
struct Conflict: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var pageID: String
    var pageTitle: String
    var kept: String
    var other: String
    var deleted: String?

    enum CodingKeys: String, CodingKey {
        case id, kept, other, deleted
        case pageID = "page_id"
        case pageTitle = "page_title"
    }

    /// Resolution is how a conflict is settled: as merged, with this
    /// device's version put back, or with the block deleted again.
    enum Resolution: String, Sendable {
        case keep
        case thisDevice = "this_device"
        case delete
    }
}

/// Member is someone a page is shared with.
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

/// TagRow is a tag and how many pages have it.
struct TagRow: Codable, Hashable, Sendable {
    var tag: String
    var count: Int
}

/// LibrarySnapshot is what the sidebar shows: pages, templates among them,
/// folders and tags. Pages of another space are shared with the user.
struct LibrarySnapshot: Codable, Sendable {
    var spaceID: String
    var pages: [PageRow]
    var folders: [FolderRow]
    var tags: [TagRow]

    enum CodingKeys: String, CodingKey {
        case pages, folders, tags
        case spaceID = "space_id"
    }

    static let empty = LibrarySnapshot(spaceID: "", pages: [], folders: [], tags: [])
}

/// NewPage is a page to create.
struct NewPage: Codable, Sendable {
    var parentID: String = ""
    var folderID: String = ""
    var afterID: String = ""
    var title: String = ""
    var tags: [String] = []
    /// link puts a card for the page at the end of its parent, as a
    /// subpage made from the library gets.
    var link = false
    /// start fills the page from the template its parent or folder names
    /// for new pages.
    var start = false

    enum CodingKeys: String, CodingKey {
        case title, tags, link, start
        case parentID = "parent_id"
        case folderID = "folder_id"
        case afterID = "after_id"
    }
}

/// SearchHit is one search result.
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

/// Todo is an open to-do of a page, with a due date or an owner.
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

/// CoreStatus is who uses the app and how sync stands.
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

/// OpenedPage is a page open for the editor.
struct OpenedPage: Codable, Sendable {
    var id: String
    /// editor is the number the core gave the editor that opened it, for
    /// its edits and its closing to name it.
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

/// DocContent is a page's content: the tree of blocks, and the meta that
/// merges it per block (docs/protocol.md §1).
struct DocContent: Codable, Hashable, Sendable {
    var document: DocNode
    var meta: JSONValue?
}

/// DocNode is one block: a type, its data, and the blocks nested in it.
struct DocNode: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var type: String
    var data: [String: JSONValue]?
    var children: [DocNode]?
}
