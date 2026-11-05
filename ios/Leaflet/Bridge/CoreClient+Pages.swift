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

    /// restoreVersion puts a version back as the page's content, and
    /// returns the version it kept of the page as it was, for Undo to
    /// restore, or "" when there was nothing to keep.
    @discardableResult
    nonisolated(nonsending) func restoreVersion(_ pageID: String, id: String) async throws
        -> String
    {
        try await call { core in
            try Self.answer { e in core.restoreVersion(pageID, id_: id, error: e) }
        }
    }

    nonisolated(nonsending) func exportMarkdown(_ pageID: String) async throws -> ExportedPage {
        try await call(ExportedPage.self) { core in
            try Self.answer { e in core.exportMarkdown(pageID, error: e) }
        }
    }

    /// exportPDF returns a page to print as a PDF, on the paper of region,
    /// a code such as "US", its web page in a file of the app's own for
    /// PagePDF to print and remove. It waits beside the other calls: the
    /// page's pictures may download first.
    nonisolated(nonsending) func exportPDF(_ pageID: String, region: String) async throws
        -> ExportedPDF
    {
        let dir = FileManager.default.temporaryDirectory.appending(
            path: "Print", directoryHint: .isDirectory
        ).path(percentEncoded: false)
        return try await call(ExportedPDF.self, waiting: true) { core in
            try Self.answer { e in core.exportPDFPage(pageID, region: region, dir: dir, error: e) }
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

    /// copyText writes blocks as Copy puts them on the clipboard: their
    /// Markdown, as the desktop's Copy writes it.
    nonisolated(nonsending) func copyText(_ nodes: [DocNode]) async throws -> String {
        let json = String(decoding: try JSONEncoder().encode(nodes), as: UTF8.self)
        return try await call { core in
            try Self.answer { e in core.copyText(json, error: e) }
        }
    }

    /// addFile keeps a file with a page, and returns the block that shows
    /// it.
    /// It waits beside the other calls, as copying a video takes a while.
    nonisolated(nonsending) func addFile(_ pageID: String, path: String) async throws -> DocNode {
        try await call(DocNode.self, waiting: true) { core in
            try Self.answer { e in core.addFile(pageID, path: path, error: e) }
        }
    }

    /// appendBlock puts a block at the end of a page: one addFile returned,
    /// once its page left the window.
    nonisolated(nonsending) func appendBlock(_ pageID: String, _ node: DocNode) async throws {
        let json = String(decoding: try JSONEncoder().encode(node), as: UTF8.self)
        try await call { try $0.appendBlock(pageID, node: json) }
    }

    /// picture reads a picture an image block links to on the web, as the
    /// desktop's image blocks read one: through the core, from a public
    /// address only, http too, which the app's own loading refuses. A few
    /// come at a time, as files do, but beside them, for the files kept
    /// with pages not to wait behind a slow site; one whose task is
    /// cancelled, as its block leaves the window, stops waiting on its site
    /// and leaves its turn to the next.
    nonisolated(nonsending) func picture(_ link: String) async throws -> Data {
        try await Downloads.pictures.run {
            try await self.cancellable { core, token in
                try core.pictureCancellable(link, token: token)
            }
        }
    }

    /// localFile returns the path on this device of a file kept with a
    /// page, by its ID, downloaded first when it is not here.
    /// A few come at a time, and the rest wait their turn without a
    /// thread, as a page of many pictures asks for each.
    nonisolated(nonsending) func localFile(_ id: String) async throws -> String {
        try await Downloads.shared.run {
            try await self.cancellable { core, token in
                try Self.answer { e in core.localFile(id, token: token, error: e) }
            }
        }
    }
}

/// Downloads lets a few files come from where they went up at a time:
/// each holds a thread while it waits on the network, and a page of many
/// pictures, each asking for its own, would hold many.
actor Downloads {
    static let shared = Downloads(most: 4)
    /// pictures are the web's pictures, which wait on sites the app has no
    /// say over.
    static let pictures = Downloads(most: 4)

    private let most: Int
    private var running = 0
    private var waiting: [CheckedContinuation<Void, Never>] = []

    init(most: Int) { self.most = most }

    /// run runs body once fewer than most others run, unless the task that
    /// asked is cancelled meanwhile.
    func run<T: Sendable>(_ body: @Sendable () async throws -> T) async throws -> T {
        if running < most {
            running += 1
        } else {
            // The one that ends hands this its turn.
            await withCheckedContinuation { waiting.append($0) }
        }
        defer {
            if waiting.isEmpty {
                running -= 1
            } else {
                waiting.removeFirst().resume()
            }
        }
        try Task.checkCancellation()
        return try await body()
    }
}
