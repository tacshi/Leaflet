import Foundation
import LeafletCore

/// A page's templates, copies, versions and files, as the desktop's page
/// menu, History dialog and blank page offer them.
extension CoreClient {
    func templates() async throws -> [TemplateRow] {
        try decode(
            [TemplateRow].self,
            try await call { core in try Self.answer { e in core.templates(e) } })
    }

    /// applyTemplate fills an empty page from a template, and returns the
    /// title an untitled page took, or "".
    func applyTemplate(_ pageID: String, key: String) async throws -> String {
        try await call { core in
            try Self.answer { e in core.applyTemplate(pageID, key: key, error: e) }
        }
    }

    /// insertTemplate puts a template's blocks in a page after a block, as
    /// the / menu's templates do, and returns the title an empty, untitled
    /// page took, or "".
    func insertTemplate(_ pageID: String, after blockID: String, key: String) async throws
        -> String
    {
        try await call { core in
            try Self.answer { e in
                core.insertTemplate(pageID, blockID: blockID, key: key, error: e)
            }
        }
    }

    /// menuItems returns what the editor's menu for trigger, "/" or "@",
    /// offers in a page, as the core's JSON for the menu to filter.
    func menuItems(_ pageID: String, trigger: Character) async throws -> String {
        try await call { core in
            try Self.answer { e in core.menuItems(pageID, trigger: String(trigger), error: e) }
        }
    }

    /// editTemplate returns the page of a template to change, a starter
    /// becoming the user's own first.
    func editTemplate(_ key: String) async throws -> String {
        try await call { core in try Self.answer { e in core.editTemplate(key, error: e) } }
    }

    func setStarterDeleted(_ key: String, deleted: Bool) async throws {
        try await call { try $0.setStarterDeleted(key, deleted: deleted) }
    }

    /// setDefaultTemplate chooses the template new pages under a page, or
    /// in a folder, start from ("" for blank ones), and returns the message
    /// that says so.
    func setDefaultTemplate(pageID: String = "", folderID: String = "", key: String)
        async throws -> String
    {
        try await call { core in
            try Self.answer { e in
                core.setDefaultTemplate(pageID, folderID: folderID, key: key, error: e)
            }
        }
    }

    func duplicatePage(_ id: String) async throws -> PageRow {
        try decode(
            PageRow.self,
            try await call { core in try Self.answer { e in core.duplicatePage(id, error: e) } })
    }

    func saveAsTemplate(_ id: String) async throws -> PageRow {
        try decode(
            PageRow.self,
            try await call { core in try Self.answer { e in core.save(asTemplate: id, error: e) } })
    }

    func history(_ pageID: String) async throws -> [PageVersion] {
        try decode(
            [PageVersion].self,
            try await call { core in try Self.answer { e in core.history(pageID, error: e) } })
    }

    func nameVersion(_ pageID: String, name: String) async throws {
        try await call { try $0.nameVersion(pageID, name: name) }
    }

    func restoreVersion(_ pageID: String, id: String) async throws {
        try await call { try $0.restoreVersion(pageID, id_: id) }
    }

    func exportMarkdown(_ pageID: String) async throws -> ExportedPage {
        try decode(
            ExportedPage.self,
            try await call { core in try Self.answer { e in core.exportMarkdown(pageID, error: e) }
            })
    }

    /// addFile keeps a file with a page, and returns the block that shows
    /// it.
    func addFile(_ pageID: String, path: String) async throws -> DocNode {
        try decode(
            DocNode.self,
            try await call { core in
                try Self.answer { e in core.addFile(pageID, path: path, error: e) }
            })
    }

    /// localFile returns the path on this device of a file kept with a
    /// page, by its ID, downloaded first when it is not here.
    func localFile(_ id: String) async throws -> String {
        try await call { core in try Self.answer { e in core.localFile(id, error: e) } }
    }
}
