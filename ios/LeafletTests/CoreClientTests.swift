import Foundation
import Testing

@testable import Leaflet

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

    @Test func aCancelledSearchNeverReachesTheCore() async throws {
        let search = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            return try await client.search("anything")
        }
        await #expect(throws: CancellationError.self) { try await search.value }
    }

    @Test func callsReachTheCoreInTheOrderMade() async throws {
        let tasks = (1...40).map { size in
            Task { try await client.saveSettings(#"{"text_size":\#(size)}"#) }
        }
        for t in tasks { try await t.value }
        let raw = try await client.settings()
        let settings = try JSONDecoder().decode([String: JSONValue].self, from: Data(raw.utf8))
        #expect(settings["text_size"]?.double == 40)
    }

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

    @Test func toDosComeADayAtATime() async throws {
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
            todo("c", "Sooner", "2999-01-01"), todo("d", "Also sooner", "2999-01-01"),
        ]
        try await client.editPage(
            page.id, editor: opened.editor, content: opened.content, rev: opened.rev)
        await client.closePage(page.id, editor: opened.editor)
        let mine = try await client.todos().map { group in
            (group, group.todos.filter { $0.pageID == page.id }.map(\.text))
        }.filter { !$0.1.isEmpty }
        #expect(mine.map(\.0.key) == ["2001-01-01", "2999-01-01", "2999-01-02"])
        #expect(mine.map(\.0.section) == ["overdue", "upcoming", "upcoming"])
        #expect(mine.first?.0.todos.first?.overdue == true)
        #expect(mine.map(\.1) == [["Late"], ["Sooner", "Also sooner"], ["Later"]])

        try await client.checkTodo(page.id, "b", checked: true)
        let left = try await client.todos().flatMap(\.todos).filter { $0.pageID == page.id }
        #expect(left.map(\.text) == ["Sooner", "Also sooner", "Later"])
    }

    @Test func aSignInWithSeveralProjectsAsksWhichToUse() throws {
        let raw = #"""
            {"email":"","lost":0,"projects":[
              {"ref":"older","name":"Leaflet","region":"us-east-1","paused":false},
              {"ref":"newer","name":"Leaflet 2","region":"eu-central-1","paused":true}]}
            """#
        let asked = try JSONDecoder().decode(SignedIn.self, from: Data(raw.utf8))
        #expect(asked.projects?.map(\.id) == ["older", "newer"])
        #expect(asked.projects?.last?.paused == true)
        let done = try JSONDecoder().decode(
            SignedIn.self, from: Data(#"{"email":"me@example.com","lost":0}"#.utf8))
        #expect(done.projects == nil && done.email == "me@example.com")
    }

    @Test func aProjectIsChosenOnlyOfASignInsOwn() async throws {
        await #expect(throws: (any Error).self) { try await client.useSupabaseProject("older") }
        #expect(try await client.account().signedIn == false)
    }

    @Test func applyingATemplateGivesTheTitleItTook() async throws {
        let page = try await client.createPage(NewPage())
        let template = try #require(try await client.templates().first { !$0.title.isEmpty })
        let title = try await client.applyTemplate(page.id, key: template.key)
        #expect(!title.isEmpty)
        #expect(try await client.library().page(page.id)?.title == title)
    }
}
