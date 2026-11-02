import Foundation
import LeafletCore

/// A page's templates, copies, versions and files, as the desktop's page
/// menu, History dialog and blank page offer them.
extension CoreClient {
    nonisolated(nonsending) func templates() async throws -> [TemplateRow] {
        try await call([TemplateRow].self) { core in try Self.answer { e in core.templates(e) } }
    }

    /// applyTemplate fills an empty page from a template, and returns the
    /// title an untitled page took, or "".
    nonisolated(nonsending) func applyTemplate(_ pageID: String, key: String) async throws -> String
    {
        try await call(waiting: true) { core in
            try Self.answer { e in core.applyTemplate(pageID, key: key, error: e) }
        }
    }

    /// insertTemplate puts a template's blocks in a page after a block, as
    /// the / menu's templates do, and returns the title an empty, untitled
    /// page took, or "".
    nonisolated(nonsending) func insertTemplate(
        _ pageID: String, after blockID: String, key: String
    ) async throws
        -> String
    {
        try await call(waiting: true) { core in
            try Self.answer { e in
                core.insertTemplate(pageID, blockID: blockID, key: key, error: e)
            }
        }
    }

    /// menuItems returns what the editor's menu for trigger, "/" or "@",
    /// offers in a page, as the core's JSON for the menu to filter.
    nonisolated(nonsending) func menuItems(_ pageID: String, trigger: Character) async throws
        -> String
    {
        try await call { core in
            try Self.answer { e in core.menuItems(pageID, trigger: String(trigger), error: e) }
        }
    }

    /// editTemplate returns the page of a template to change, a starter
    /// becoming the user's own first.
    nonisolated(nonsending) func editTemplate(_ key: String) async throws -> String {
        try await call { core in try Self.answer { e in core.editTemplate(key, error: e) } }
    }

    nonisolated(nonsending) func setStarterDeleted(_ key: String, deleted: Bool) async throws {
        try await call { try $0.setStarterDeleted(key, deleted: deleted) }
    }

    /// setDefaultTemplate chooses the template new pages under a page, or
    /// in a folder, start from ("" for blank ones), and returns the message
    /// that says so.
    nonisolated(nonsending) func setDefaultTemplate(
        pageID: String = "", folderID: String = "", key: String
    )
        async throws -> String
    {
        try await call { core in
            try Self.answer { e in
                core.setDefaultTemplate(pageID, folderID: folderID, key: key, error: e)
            }
        }
    }

    nonisolated(nonsending) func duplicatePage(_ id: String) async throws -> PageRow {
        try await call(PageRow.self, waiting: true) { core in
            try Self.answer { e in core.duplicatePage(id, error: e) }
        }
    }

    nonisolated(nonsending) func saveAsTemplate(_ id: String) async throws -> PageRow {
        try await call(PageRow.self, waiting: true) { core in
            try Self.answer { e in core.save(asTemplate: id, error: e) }
        }
    }

    nonisolated(nonsending) func history(_ pageID: String) async throws -> [PageVersion] {
        try await call([PageVersion].self, waiting: true) { core in
            try Self.answer { e in core.history(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func nameVersion(_ pageID: String, name: String) async throws {
        try await call { try $0.nameVersion(pageID, name: name) }
    }

    nonisolated(nonsending) func restoreVersion(_ pageID: String, id: String) async throws {
        try await call { try $0.restoreVersion(pageID, id_: id) }
    }

    nonisolated(nonsending) func exportMarkdown(_ pageID: String) async throws -> ExportedPage {
        try await call(ExportedPage.self) { core in
            try Self.answer { e in core.exportMarkdown(pageID, error: e) }
        }
    }

    /// plainCopy returns blocks as Copy as Plain Text puts them on the
    /// clipboard, as the desktop's: text alone, a line per block.
    nonisolated(nonsending) func plainCopy(_ nodes: [DocNode]) async throws -> String {
        let json = String(decoding: try JSONEncoder().encode(nodes), as: UTF8.self)
        return try await call { core in
            try Self.answer { e in core.plainCopy(json, error: e) }
        }
    }

    /// addFile keeps a file with a page, and returns the block that shows
    /// it.
    nonisolated(nonsending) func addFile(_ pageID: String, path: String) async throws -> DocNode {
        try await call(DocNode.self) { core in
            try Self.answer { e in core.addFile(pageID, path: path, error: e) }
        }
    }

    /// localFile returns the path on this device of a file kept with a
    /// page, by its ID, downloaded first when it is not here.
    nonisolated(nonsending) func localFile(_ id: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.localFile(id, error: e) }
        }
    }
}
