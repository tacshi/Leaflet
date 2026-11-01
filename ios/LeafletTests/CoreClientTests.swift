import Foundation
import Testing

@testable import Leaflet

/// CoreClientTests drive the Go core through the bridge, in a folder of
/// their own, as the app does.
@MainActor
struct CoreClientTests {
    let client: CoreClient

    init() throws {
        let dir = FileManager.default.temporaryDirectory
            .appending(path: "CoreClientTests-\(UUID().uuidString)", directoryHint: .isDirectory)
        client = try CoreClient.open(dataDir: dir, iCloudRoot: nil, locale: "en")
    }

    @Test func createsAndListsPages() async throws {
        let page = try await client.createPage(NewPage(title: "Groceries", tags: ["home"]))
        #expect(page.title == "Groceries")
        #expect(page.canEdit)
        let library = try await client.library()
        #expect(library.pages.contains { $0.id == page.id })
        #expect(library.tags.contains { $0.tag == "home" })
        #expect(!library.spaceID.isEmpty)
    }

    @Test func editsSaveAndReopen() async throws {
        let page = try await client.createPage(NewPage(title: "Notes"))
        var opened = try await client.openPage(page.id)
        let block = DocNode(
            id: "a", type: "paragraph",
            data: ["delta": .array([.object(["insert": .string("Milk")])])], children: nil)
        opened.content.document.children = [block]
        try await client.editPage(
            page.id, editor: opened.editor, content: opened.content, rev: opened.rev)
        await client.closePage(page.id, editor: opened.editor)

        let again = try await client.openPage(page.id)
        let texts = again.content.document.children?.compactMap {
            $0.data?["delta"]?.array?.first?["insert"]?.string
        }
        #expect(texts == ["Milk"])
    }
}
