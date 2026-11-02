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

    /// Calls made one after another without waiting, each from a task of
    /// its own, as a slider's changes are, reach the core in that order:
    /// the last one made is the one kept.
    @Test func callsReachTheCoreInTheOrderMade() async throws {
        let tasks = (1...40).map { size in
            Task { try await client.saveSettings(#"{"text_size":\#(size)}"#) }
        }
        for t in tasks { try await t.value }
        let raw = try await client.settings()
        let settings = try JSONDecoder().decode([String: JSONValue].self, from: Data(raw.utf8))
        #expect(settings["text_size"]?.double == 40)
    }

    @Test func applyingATemplateGivesTheTitleItTook() async throws {
        let page = try await client.createPage(NewPage())
        let template = try #require(try await client.templates().first { !$0.title.isEmpty })
        let title = try await client.applyTemplate(page.id, key: template.key)
        #expect(!title.isEmpty)
        #expect(try await client.library().page(page.id)?.title == title)
    }
}
