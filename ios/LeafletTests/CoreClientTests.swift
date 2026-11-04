import Foundation
import Testing

@testable import Leaflet

/// CoreClientTests drive the Go core through the bridge, in a folder of
/// their own, as the app does.
@MainActor
struct CoreClientTests {
    let core: ScratchCore
    var client: CoreClient { core.client }

    init() throws {
        core = try ScratchCore("CoreClientTests")
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

    /// A search whose task is cancelled before its turn, as one typed over
    /// is, never reaches the core.
    @Test func aCancelledSearchNeverReachesTheCore() async throws {
        let search = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            return try await client.search("anything")
        }
        await #expect(throws: CancellationError.self) { try await search.value }
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

    /// Every answer the app reads decodes from a core just opened, as the
    /// app's types have them: a key the core leaves out, or a list it
    /// sends as null, fails here rather than show an empty sidebar.
    @Test func everyAnswerDecodesFromAFreshCore() async throws {
        let page = try await client.createPage(NewPage(title: "Plan"))
        _ = try await client.library()
        _ = try await client.todos()
        _ = try await client.templates()
        _ = try await client.conflicts()
        _ = try await client.status()
        _ = try await client.account()
        _ = try await client.agentState(page.id)
        _ = try await client.threads(page.id)
        _ = try await client.members(page.id)
        _ = try await client.history(page.id)
        _ = try await client.moveTargets(page.id)
        _ = try await client.exportMarkdown(page.id)
        _ = try await client.speechSettings()
        _ = try await client.recordingState()
        _ = try await client.presets()
        _ = try await client.providers()
        let opened = try await client.openPage(page.id)
        await client.closePage(page.id, editor: opened.editor)
    }

    /// The To-dos tab's sections come from the core in order, overdue
    /// first, each by date.
    @Test func toDosComeInTheirSections() async throws {
        let page = try await client.createPage(NewPage(title: "Chores"))
        var opened = try await client.openPage(page.id)
        func todo(_ id: String, _ text: String, _ due: String) -> DocNode {
            DocNode(
                id: id, type: "todo_list",
                data: [
                    "checked": .bool(false), "due": .string(due),
                    "delta": .array([.object(["insert": .string(text)])]),
                ], children: nil)
        }
        opened.content.document.children = [
            todo("a", "Later", "2999-01-02"), todo("b", "Late", "2001-01-01"),
            todo("c", "Sooner", "2999-01-01"),
        ]
        try await client.editPage(
            page.id, editor: opened.editor, content: opened.content, rev: opened.rev)
        await client.closePage(page.id, editor: opened.editor)
        // Those of this page: the welcome page may have its own.
        let mine = try await client.todos().map { group in
            (group, group.todos.filter { $0.pageID == page.id }.map(\.text))
        }.filter { !$0.1.isEmpty }
        #expect(mine.map(\.0.section) == ["overdue", "upcoming"])
        #expect(mine.first?.0.overdue == true)
        #expect(mine.last?.1 == ["Sooner", "Later"])
    }

    @Test func applyingATemplateGivesTheTitleItTook() async throws {
        let page = try await client.createPage(NewPage())
        let template = try #require(try await client.templates().first { !$0.title.isEmpty })
        let title = try await client.applyTemplate(page.id, key: template.key)
        #expect(!title.isEmpty)
        #expect(try await client.library().page(page.id)?.title == title)
    }
}
