import Foundation
import LeafletCore

extension CoreClient {
    nonisolated(nonsending) func templates() async throws -> [TemplateRow] {
        try await call([TemplateRow].self) { core in try Self.answer { e in core.templates(e) } }
    }

    nonisolated(nonsending) func applyTemplate(_ pageID: String, key: String) async throws -> String
    {
        try await call(waiting: true) { core in
            try Self.answer { e in core.applyTemplate(pageID, key: key, error: e) }
        }
    }

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

    nonisolated(nonsending) func menuItems(_ pageID: String, trigger: Character) async throws
        -> String
    {
        try await call { core in
            try Self.answer { e in core.menuItems(pageID, trigger: String(trigger), error: e) }
        }
    }

    nonisolated(nonsending) func editTemplate(_ key: String) async throws -> String {
        try await call { core in try Self.answer { e in core.editTemplate(key, error: e) } }
    }

    nonisolated(nonsending) func setStarterDeleted(_ key: String, deleted: Bool) async throws {
        try await call { try $0.setStarterDeleted(key, deleted: deleted) }
    }

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

    nonisolated(nonsending) func currentMarkdown(_ pageID: String) async throws -> String {
        try await call(waiting: true) { core in
            try Self.answer { e in core.currentMarkdown(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func addFolderFile(_ folderID: String, path: String) async throws {
        try await call(waiting: true) { try $0.addFolderFile(folderID, path: path) }
    }

    nonisolated(nonsending) func deleteFile(_ id: String) async throws {
        try await call { try $0.deleteFile(id) }
    }

    nonisolated(nonsending) func nameVersion(_ pageID: String, name: String) async throws {
        try await call { try $0.nameVersion(pageID, name: name) }
    }

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

    nonisolated(nonsending) func plainCopy(_ nodes: [DocNode]) async throws -> String {
        let json = String(decoding: try JSONEncoder().encode(nodes), as: UTF8.self)
        return try await call { core in
            try Self.answer { e in core.plainCopy(json, error: e) }
        }
    }

    nonisolated(nonsending) func copyText(_ nodes: [DocNode]) async throws -> String {
        let json = String(decoding: try JSONEncoder().encode(nodes), as: UTF8.self)
        return try await call { core in
            try Self.answer { e in core.copyText(json, error: e) }
        }
    }

    nonisolated(nonsending) func addFile(_ pageID: String, path: String) async throws -> DocNode {
        try await call(DocNode.self, waiting: true) { core in
            try Self.answer { e in core.addFile(pageID, path: path, error: e) }
        }
    }

    nonisolated(nonsending) func appendBlock(_ pageID: String, _ node: DocNode) async throws {
        let json = String(decoding: try JSONEncoder().encode(node), as: UTF8.self)
        try await call { try $0.appendBlock(pageID, node: json) }
    }

    nonisolated(nonsending) func picture(_ link: String) async throws -> Data {
        try await Downloads.pictures.run {
            try await self.cancellable { core, token in
                try core.pictureCancellable(link, token: token)
            }
        }
    }

    nonisolated(nonsending) func localFile(_ id: String) async throws -> String {
        try await Downloads.shared.run {
            try await self.cancellable { core, token in
                try Self.answer { e in core.localFile(id, token: token, error: e) }
            }
        }
    }
}

actor Downloads {
    static let shared = Downloads(most: 4)
    static let pictures = Downloads(most: 4)

    private let most: Int
    private var running = 0
    private var waiting: [CheckedContinuation<Void, Never>] = []

    init(most: Int) { self.most = most }

    func run<T: Sendable>(_ body: @Sendable () async throws -> T) async throws -> T {
        if running < most {
            running += 1
        } else {
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
