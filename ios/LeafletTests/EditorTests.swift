import Foundation
import LeafletCore
import SwiftUI
import Testing
import UIKit

@testable import Leaflet

/// EditorTests check the editor's text against the content the core keeps:
/// that a page goes into the editor and back unchanged, and that editing
/// keeps each paragraph one block, as the desktop's editor does. They run
/// one at a time: a test that shows the editor in a window brings up the
/// keyboard, which one window at a time holds.
@MainActor
@Suite(.serialized)
struct EditorTests {
    /// welcome is the welcome page, as a new core makes it.
    private func welcome() async throws -> DocContent {
        let core = try ScratchCore("EditorTests")
        let lib = try await core.client.library()
        let page = try #require(
            lib.pages.first { $0.parentID == nil && $0.title.contains("Welcome") })
        return try await core.client.openPage(page.id).content
    }

    /// The welcome page goes into the editor, styled as it shows, and back
    /// unchanged: what only shows, as a link's underline and a checked
    /// to-do's line through, is not saved as a format.
    @Test func welcomePageRoundTrips() async throws {
        let content = try await welcome()
        let (text, trailing) = DocumentMapper.attributedString(content)
        let back = DocumentMapper.content(text, trailing: trailing, meta: content.meta)
        #expect(back.document.children == content.document.children)
        let c = editor(content.document.children ?? [])
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.document.children == content.document.children)
    }

    /// A closed toggle folds what is in it out of the page's layout, and
    /// opening it brings it back.
    @Test func aClosedToggleFoldsAwayWhatIsInIt() {
        let c = editor([
            block(
                "t", "toggle_list", "Details", data: ["collapsed": .bool(true)],
                children: [block("in", "paragraph", "Hidden")]),
            block("after", "paragraph", "After"),
        ])
        c.textView.frame = CGRect(x: 0, y: 0, width: 600, height: 800)
        c.textView.layoutIfNeeded()
        #expect(laidOut(c) == ["Details", "After"])

        let (_, toggle) = c.paragraph(at: 0)
        c.setBlock(toggle.setting("collapsed", .bool(false)), at: 0)
        c.textView.layoutIfNeeded()
        #expect(laidOut(c) == ["Details", "Hidden", "After"])
    }

    /// A folded heading folds its section away, as the desktop's: up to the
    /// next heading of its level or higher.
    @Test func aFoldedHeadingFoldsItsSection() {
        let c = editor([
            block("h", "heading", "Plan", data: ["level": .int(2), "collapsed": .bool(true)]),
            block("p", "paragraph", "Body"),
            block("sub", "heading", "Detail", data: ["level": .int(3)]),
            block("q", "bulleted_list", "Item", children: [block("n", "paragraph", "Nested")]),
            block("next", "heading", "Next", data: ["level": .int(2)]),
            block("after", "paragraph", "After"),
        ])
        c.textView.frame = CGRect(x: 0, y: 0, width: 600, height: 800)
        c.textView.layoutIfNeeded()
        #expect(laidOut(c) == ["Plan", "Next", "After"])
    }

    /// An agent writing on past the window, as its blocks stream in, does
    /// not scroll the page: the user scrolls it, wherever the caret is.
    @Test(arguments: ["top", "written"])
    func anAgentWritingDoesNotScrollThePage(caretIn: String) async throws {
        let filler = (0..<12).map {
            block(
                "p\($0)", "paragraph", "Paragraph \($0) of what was there before the agent began.")
        }
        let c = editor(filler + [block("into", "paragraph", "")])
        let scene = try #require(
            UIApplication.shared.connectedScenes.first { $0 is UIWindowScene } as? UIWindowScene)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 1000, height: 700)
        window.addSubview(c.textView)
        c.textView.frame = window.bounds
        window.makeKeyAndVisible()
        // The window goes as the test ends, with the keyboard it brought
        // up, leaving the app's own as they were.
        defer {
            _ = c.textView.resignFirstResponder()
            c.textView.removeFromSuperview()
            window.isHidden = true
            window.windowScene = nil
        }
        c.textView.layoutIfNeeded()
        _ = c.textView.becomeFirstResponder()
        // Where the user asked: the top of the page, or the empty line the
        // agent writes in place of, the view scrolled to it.
        let (range, _) = c.paragraph(at: c.textView.textStorage.length)
        c.textView.selectedRange = NSRange(
            location: caretIn == "top" ? 0 : range.location, length: 0)
        // Once an iPhone's keyboard has come up, which moves the page as it
        // does: a page moved by the keyboard is not one an agent moved.
        try await settle(c)
        c.textView.scrollRangeToVisible(c.textView.selectedRange)
        try await Task.sleep(for: .milliseconds(100))
        let start = c.textView.contentOffset.y

        var written: [DocNode] = []
        for i in 0..<40 {
            // A block more each time, the last growing as it is typed.
            written.append(block("w\(i)", "paragraph", "Line \(i) of the story the agent writes."))
            var into = written
            into[0] = block("into", "heading", "A long story", data: ["level": .int(2)])
            c.update(
                DocContent(
                    document: DocNode(id: "root", type: "page", data: nil, children: filler + into),
                    meta: nil),
                tinted: [])
            try await Task.sleep(for: .milliseconds(30))
        }
        #expect(
            c.textView.contentOffset.y == start,
            "scrolled from \(start) to \(c.textView.contentOffset.y)")
    }

    /// laidOut is the text of each paragraph the page lays out.
    private func laidOut(_ c: EditorController) -> [String] {
        guard let tlm = c.textView.textLayoutManager else { return [] }
        tlm.ensureLayout(for: tlm.documentRange)
        var out: [String] = []
        tlm.enumerateTextLayoutFragments(from: tlm.documentRange.location, options: []) { f in
            if let p = f.textElement as? NSTextParagraph {
                out.append(
                    p.attributedString.string.trimmingCharacters(in: .whitespacesAndNewlines))
            }
            return true
        }
        return out
    }

    private func editor(_ blocks: [DocNode]) -> EditorController {
        let c = EditorController(
            style: EditorStyle(textSize: 17, palette: Palette.palettes["Indigo"]!["light"]!))
        c.load(
            DocContent(
                document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil),
            tinted: [])
        return c
    }

    private func block(
        _ id: String, _ type: String, _ text: String, data: [String: JSONValue] = [:],
        children: [DocNode]? = nil
    ) -> DocNode {
        var d = data
        d["delta"] = .array(text.isEmpty ? [] : [.object(["insert": .string(text)])])
        return DocNode(id: id, type: type, data: d, children: children)
    }

    /// offer has the editor's menus offer items, as the core would.
    private func offer(_ c: EditorController, _ items: [(key: String, label: String)]) {
        let rows = items.map {
            ["key": $0.key, "label": $0.label, "hint": "", "icon": "", "group": ""]
        }
        let data = (try? JSONSerialization.data(withJSONObject: rows)) ?? Data()
        let json = String(decoding: data, as: UTF8.self)
        c.menuItems = { _, give in give(json) }
    }

    private func blocks(_ c: EditorController) -> [DocNode] {
        var out: [DocNode] = []
        func walk(_ n: [DocNode]) {
            for b in n {
                out.append(b)
                walk(b.children ?? [])
            }
        }
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        walk(sent?.blocks ?? [])
        return out
    }

    @Test func enterSplitsAListItem() {
        let c = editor([block("a", "bulleted_list", "Milk")])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.type("\n")
        c.type("Eggs")
        let b = blocks(c)
        #expect(b.map(\.type) == ["bulleted_list", "bulleted_list"])
        #expect(b.map(\.text) == ["Milk", "Eggs"])
        #expect(b[0].id == "a")
        #expect(b[1].id != "a")
    }

    @Test func enterOnAnEmptyItemEndsTheList() {
        let c = editor([block("a", "bulleted_list", "Milk")])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.type("\n")
        c.type("\n")
        #expect(blocks(c).map(\.type) == ["bulleted_list", "paragraph"])
    }

    @Test func markdownShortcutsTurnText() {
        let c = editor([block("a", "paragraph", "")])
        c.type("## Title")
        let b = blocks(c)
        #expect(b.first?.type == "heading")
        #expect(b.first?.data?["level"]?.int == 2)
        #expect(b.first?.text == "Title")
    }

    @Test func shortcutsFollowTheDesktopsRules() {
        // A full-width space, as Chinese and Japanese input types it.
        let c = editor([block("a", "paragraph", "")])
        c.type("#\u{3000}見出し")
        #expect(blocks(c).first?.type == "heading")
        #expect(blocks(c).first?.text == "見出し")
        let d = editor([block("a", "paragraph", "")])
        d.type("---")
        #expect(blocks(d).map(\.type) == ["divider", "paragraph"])
        // "/" after Korean is in a word, as on the desktop.
        let k = editor([block("a", "paragraph", "")])
        k.type("한/")
        #expect(k.menu == nil)
    }

    @Test func aBulletTurnsIntoAToDo() {
        let c = editor([block("a", "bulleted_list", "")])
        c.type("[] Buy")
        let b = blocks(c)
        #expect(b.first?.type == "todo_list")
        #expect(b.first?.text == "Buy")
        // Other shortcuts leave a bullet as it is, as on the desktop.
        let d = editor([block("a", "bulleted_list", "")])
        d.type("> No")
        #expect(blocks(d).first?.type == "bulleted_list")
        #expect(blocks(d).first?.text == "> No")
    }

    @Test func inlineMarkdownFormatsAsYouType() {
        let c = editor([block("a", "paragraph", "")])
        c.type("a **b** *i* `c` ~~s~~ x")
        let delta = blocks(c).first?.data?["delta"]?.array ?? []
        let runs = delta.map { ($0["insert"]?.string ?? "", $0["attributes"]) }
        #expect(runs.map(\.0).joined() == "a b i c s x")
        func format(_ text: String, _ key: String) -> Bool {
            runs.first { $0.0 == text }?.1?[key]?.bool == true
        }
        #expect(format("b", "bold"))
        #expect(format("i", "italic"))
        #expect(format("c", "code"))
        #expect(format("s", "strikethrough"))
        // What follows a closing marker is plain.
        #expect(runs.last?.0 == " x")
        #expect(runs.last?.1 == nil)
    }

    @Test func markersAroundSpaceStayText() {
        let c = editor([block("a", "paragraph", "")])
        c.type("2 * 3 * 4")
        #expect(blocks(c).first?.text == "2 * 3 * 4")
    }

    @Test func tabNestsInTheItemBefore() {
        let c = editor([block("a", "bulleted_list", "One"), block("b", "bulleted_list", "Two")])
        c.textView.selectedRange = NSRange(location: 5, length: 0)
        c.type("\t")
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.count == 1)
        #expect(sent?.blocks.first?.children?.map(\.id) == ["b"])
    }

    @Test func backspaceAtAStartTurnsAnItemIntoText() {
        let c = editor([
            block("a", "paragraph", "One"),
            block("b", "todo_list", "Two", data: ["checked": .bool(false)]),
        ])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.backspace()
        let b = blocks(c)
        #expect(b.map(\.type) == ["paragraph", "paragraph"])
        #expect(b.map(\.id) == ["a", "b"])
    }

    @Test func slashMenuTurnsTheBlock() {
        let c = editor([block("a", "paragraph", "")])
        offer(c, [("paragraph", "Text"), ("h1", "Heading 1"), ("h2", "Heading 2")])
        c.type("/hea")
        #expect(c.menu?.items.first?.id == "h1")
        c.type("\n")
        #expect(c.menu == nil)
        let b = blocks(c)
        #expect(b.first?.type == "heading")
        #expect(b.first?.text == "")
    }

    @Test func slashMenuInsertsATableAfterText() {
        let c = editor([block("a", "paragraph", "Plan")])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        // The second item, chosen with the down arrow.
        offer(c, [("chart", "Chart"), ("table", "Table")])
        c.type(" /")
        #expect(c.menu?.current?.id == "chart")
        c.press(UIKeyCommand.inputDownArrow)
        #expect(c.menu?.current?.id == "table")
        c.type("\t")
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        let b = sent?.blocks ?? []
        #expect(b.map(\.type) == ["paragraph", "table", "paragraph"])
        #expect(b.first?.text == "Plan ")
        // Three columns, as the desktop makes it: a header row and two.
        #expect((b[1].children ?? []).count == 9)
    }

    @Test func aTableTakesItsSheetsCells() {
        func table(_ rows: [[String]]) -> DocNode {
            var error: NSError?
            let raw = MobileSetTableRows(
                #"{"id":"t","type":"table","data":{"colsLen":1,"rowsLen":1}}"#,
                String(decoding: (try? JSONEncoder().encode(rows)) ?? Data(), as: UTF8.self),
                &error)
            return (try? JSONDecoder().decode(DocNode.self, from: Data(raw.utf8)))
                ?? DocNode(id: "t", type: "table", data: nil, children: nil)
        }
        let c = editor([block("a", "paragraph", "Plan"), table([["A"]])])
        c.replaceBlock(table([["A", "B"], ["1", "2"]]))
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        let t = sent?.blocks.first { $0.id == "t" }
        #expect(t.map(TableGrid.rows) == [["A", "B"], ["1", "2"]])
    }

    @Test func slashLeavesBlocksFromADialogToThePage() {
        let c = editor([block("a", "paragraph", "")])
        offer(c, [("embed", "Embed"), ("link_preview", "Link card")])
        var asked: (key: String, blockID: String)?
        c.onMenuItem = { asked = ($0, $1) }
        c.type("/link")
        c.type("\n")
        #expect(asked?.key == "link_preview")
        #expect(asked?.blockID == "a")
        #expect(blocks(c).first?.text == "")
    }

    @Test func slashInAWordIsText() {
        let c = editor([block("a", "paragraph", "and")])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.type("/or")
        #expect(c.menu == nil)
        #expect(blocks(c).first?.text == "and/or")
    }

    @Test func atMentionsAPage() {
        let c = editor([block("a", "paragraph", "")])
        offer(c, [("page:p1", "Groceries"), ("page:p2", "Plan")])
        c.type("@gro")
        #expect(c.menu?.items.map(\.id) == ["page:p1"])
        c.type("\n")
        let delta = blocks(c).first?.data?["delta"]?.array ?? []
        #expect(delta.first?["insert"]?.string == "Groceries")
        #expect(delta.first?["attributes"]?["mention"]?["id"]?.string == "p1")
        #expect(delta.first?["attributes"]?["href"]?.string == "leaflet://page/p1")
    }

    @Test func atOffersTheAgentsFirstAndAsksOne() {
        let c = editor([block("a", "paragraph", "")])
        let agent = AgentState.Option(
            providerID: "d", model: "deepseek-flash", name: "DeepSeek", label: "DeepSeek",
            local: false)
        offer(c, [("agent:d", "DeepSeek"), ("page:p1", "Groceries")])
        c.agents = { [agent] }
        var asked: AgentState.Option?
        c.onAsk = { _, _, chosen in asked = chosen }
        c.type("@")
        #expect(c.menu?.items.first?.id == "agent:d")
        c.type("\n")
        #expect(asked == agent)
        // The "@" goes, as the desktop's prompt takes its place.
        #expect(blocks(c).first?.text == "")
    }

    @Test func escapeEndsASelection() {
        let c = editor([block("a", "paragraph", "hello world")])
        c.textView.selectedRange = NSRange(location: 0, length: 5)
        let escape = c.textView.keyCommands?.first { $0.input == UIKeyCommand.inputEscape }
        #expect(escape != nil)
        c.textView.collapseSelection()
        #expect(c.textView.selectedRange == NSRange(location: 5, length: 0))
        // With nothing selected, Escape is the system's.
        #expect(c.textView.keyCommands?.contains { $0.input == UIKeyCommand.inputEscape } == false)
    }

    @Test func escapeClosesTheMenu() {
        let c = editor([block("a", "paragraph", "")])
        c.type("/")
        #expect(c.menu != nil)
        c.press(UIKeyCommand.inputEscape)
        #expect(c.menu == nil)
        #expect(blocks(c).first?.text == "/")
    }

    @Test func codeIsColoredByItsLanguage() {
        let c = editor([
            block(
                "a", "code", "function add(a, b) { return a + b }",
                data: ["language": .string("javascript")])
        ])
        let text = c.textView.textStorage
        var colors = Set<UIColor>()
        text.enumerateAttribute(.foregroundColor, in: NSRange(location: 0, length: text.length)) {
            v, _, _ in
            if let color = v as? UIColor { colors.insert(color) }
        }
        #expect(colors.count > 1)
    }

    @Test func codeColorsCallsMembersAndTypesAsTheDesktop() {
        let code = "x.filter(Boolean).length"
        let spans = CodeHighlighter.spans(code, language: "javascript", dark: false)
        func color(_ word: String) -> UIColor? {
            let r = (code as NSString).range(of: word)
            return spans.first { NSIntersectionRange($0.0, r).length == r.length }?.1
        }
        #expect(color("filter") == CodeHighlighter.color(.function, dark: false))
        #expect(color("Boolean") == CodeHighlighter.color(.type, dark: false))
        #expect(color("length") == CodeHighlighter.color(.constant, dark: false))
        #expect(color("x") == nil)
    }

    @Test func versionsReadAsBlocks() {
        let md = "## Plan\n\n- [ ] Pack\n1. Go\n\n| | A |\n| --- | --- |\n| x | 1 |"
        let lines = MarkdownPreview.lines(md)
        #expect(lines.count == 4)
        if case .heading(2, "Plan") = lines[0] {} else { Issue.record("heading \(lines[0])") }
        if case .item("☐", "Pack", 0) = lines[1] {} else { Issue.record("to-do \(lines[1])") }
        if case .table(let rows) = lines[3] {
            #expect(rows == [["", "A"], ["x", "1"]])
        } else {
            Issue.record("table \(lines[3])")
        }
    }

    @Test func tagsTakeTheDesktopsColors() {
        // As internal/app's tagColor picks them, whatever the case.
        #expect(TagChip<EmptyView>.color("guide") == UIColor(hex: "#14b8a6"))
        #expect(TagChip<EmptyView>.color("Work") == UIColor(hex: "#22c55e"))
        #expect(TagChip<EmptyView>.color("HOME") == UIColor(hex: "#ec4899"))
    }

    @Test func codeWithoutALanguageStaysPlain() {
        let c = editor([block("a", "code", "function add(a, b) { return a + b }")])
        let text = c.textView.textStorage
        var colors = Set<UIColor>()
        text.enumerateAttribute(.foregroundColor, in: NSRange(location: 0, length: text.length)) {
            v, _, _ in
            if let color = v as? UIColor { colors.insert(color) }
        }
        #expect(colors.count == 1)
    }

    @Test func boldStaysAFormat() {
        let c = editor([block("a", "paragraph", "Hello world")])
        c.textView.selectedRange = NSRange(location: 0, length: 5)
        c.toggleInline(.leafletBold)
        let delta = blocks(c).first?.data?["delta"]?.array ?? []
        #expect(delta.count == 2)
        #expect(delta.first?["attributes"]?["bold"]?.bool == true)
        #expect(delta.first?["insert"]?.string == "Hello")
    }

    @Test func changesFromOutsideKeepTheCaret() {
        let c = editor([block("a", "paragraph", "One"), block("b", "paragraph", "Two")])
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        let content = DocContent(
            document: DocNode(
                id: "root", type: "page", data: nil,
                children: [block("a", "paragraph", "One more"), block("b", "paragraph", "Two")]),
            meta: nil)
        c.update(content, tinted: [])
        #expect(c.textView.text == "One more\nTwo")
        // The caret stays after "Tw".
        #expect(c.textView.selectedRange.location == 11)
    }

    /// A to-do's due date reads and writes as the core keeps it, a day or
    /// a day with its time, and says when it is as the desktop's chip.
    @Test func aDueDateKeepsTheCoresForm() throws {
        let timed = try #require(
            Due(BlockInfo(id: "t", type: "todo_list", data: ["due": .string("2026-11-02T09:00")])))
        #expect(timed.timed)
        #expect(timed.value == .string("2026-11-02T09:00"))
        let day = try #require(
            Due(BlockInfo(id: "t", type: "todo_list", data: ["due": .string("2026-11-02")])))
        #expect(!day.timed)
        #expect(day.value == .string("2026-11-02"))
        #expect(
            Due(BlockInfo(id: "p", type: "paragraph", data: ["due": .string("2026-11-02")])) == nil)

        let now = try #require(
            Calendar.current.date(from: DateComponents(year: 2026, month: 11, day: 1, hour: 23)))
        #expect(day.label(now: now) == L.editorTomorrow)
        #expect(!day.overdue(now: now))
        let later = now.addingTimeInterval(3 * 86400)
        #expect(day.overdue(now: later))
        // A day without a time is due all of it.
        #expect(!day.overdue(now: now.addingTimeInterval(86400)))
    }

    /// runs is a block's text as runs of text and their formats.
    private func runs(_ node: DocNode?) -> [(String, JSONValue?)] {
        (node?.data?["delta"]?.array ?? []).map { ($0["insert"]?.string ?? "", $0["attributes"]) }
    }

    /// A checked to-do shows its text struck through, which is not a format
    /// of its text: edited, it saves none, and unchecked it shows none.
    @Test func aCheckedToDosLineIsNoFormat() {
        let c = editor([block("a", "todo_list", "Milk", data: ["checked": .bool(true)])])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.type("s")
        #expect(runs(blocks(c).first).map(\.1) == [nil])
        let (_, todo) = c.paragraph(at: 0)
        c.setBlock(todo.setting("checked", .bool(false)), at: 0)
        let text = c.textView.textStorage
        var struck = false
        text.enumerateAttribute(
            .strikethroughStyle, in: NSRange(location: 0, length: text.length)
        ) { v, _, _ in if v != nil { struck = true } }
        #expect(!struck)
        #expect(runs(blocks(c).first).map(\.1) == [nil])
    }

    /// A link shows underlined, which is not a format either: the text
    /// typed after it is plain.
    @Test func textTypedAfterALinkIsPlain() {
        var d: [String: JSONValue] = [:]
        d["delta"] = .array([
            .object([
                "insert": .string("Site"),
                "attributes": .object(["href": .string("https://example.com")]),
            ])
        ])
        let c = editor([DocNode(id: "a", type: "paragraph", data: d, children: nil)])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.type(" x")
        let r = runs(blocks(c).first)
        #expect(r.map(\.0) == ["Site", " x"])
        #expect(r.first?.1?["underline"] == nil)
        #expect(r.last?.1 == nil)
    }

    /// A format toggled at the caret is the next character's, though the
    /// selection's change comes before the text's.
    @Test func boldToggledAtTheCaretTakesTheNextCharacter() {
        let c = editor([block("a", "paragraph", "Hi ")])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.toggleInline(.leafletBold)
        c.type("yo")
        let r = runs(blocks(c).first)
        #expect(r.map(\.0) == ["Hi ", "yo"])
        #expect(r.last?.1?["bold"]?.bool == true)
    }

    /// A change from outside that only nests a block shows, and is kept.
    @Test func aChangeThatOnlyNestsABlockShows() {
        let c = editor([block("a", "bulleted_list", "One"), block("b", "bulleted_list", "Two")])
        c.update(
            DocContent(
                document: DocNode(
                    id: "root", type: "page", data: nil,
                    children: [
                        block(
                            "a", "bulleted_list", "One",
                            children: [block("b", "bulleted_list", "Two")])
                    ]),
                meta: nil),
            tinted: [])
        #expect(c.paragraph(at: 5).info.depth == 1)
        c.textView.selectedRange = NSRange(location: 7, length: 0)
        c.type("!")
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.map(\.id) == ["a"])
        #expect(sent?.blocks.first?.children?.map(\.text) == ["Two!"])
    }

    /// Lines pasted are a block each, which keep their IDs as the page is
    /// sent again; in code they are the block's lines.
    @Test func pastedLinesAreABlockEach() {
        let c = editor([block("a", "bulleted_list", "x")])
        c.textView.selectedRange = NSRange(location: 1, length: 0)
        c.paste("a\nb\nc\nd")
        let first = blocks(c)
        #expect(first.map(\.text) == ["xa", "b", "c", "d"])
        #expect(first.map(\.type) == Array(repeating: "bulleted_list", count: 4))
        #expect(Set(first.map(\.id)).count == 4)
        #expect(blocks(c).map(\.id) == first.map(\.id))

        let code = editor([block("k", "code", "")])
        code.paste("let a = 1\r\nlet b = 2")
        #expect(blocks(code).map(\.type) == ["code"])
        #expect(blocks(code).first?.text == "let a = 1\nlet b = 2")
    }

    /// A space typed by an image, before or after it, goes in a paragraph
    /// of its own: the image stays.
    @Test func aSpaceTypedByAnImageKeepsIt() {
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        for at in [0, 1] {
            let c = editor([image, block("p", "paragraph", "After")])
            c.textView.selectedRange = NSRange(location: at, length: 0)
            c.type(" ")
            let b = blocks(c)
            #expect(b.map(\.type) == ["image", "paragraph", "paragraph"])
            #expect(b.first?.id == "i")
        }
    }

    /// The last block emptied stays the block it was: its checkbox stays,
    /// and its ID.
    @Test func theLastBlockEmptiedStaysItself() {
        let c = editor([
            block("a", "paragraph", "One"),
            block("t", "todo_list", "abc", data: ["checked": .bool(false)]),
        ])
        c.textView.selectedRange = NSRange(location: 7, length: 0)
        for _ in 0..<3 { c.backspace() }
        let b = blocks(c)
        #expect(b.map(\.type) == ["paragraph", "todo_list"])
        #expect(b.map(\.id) == ["a", "t"])
        #expect(blocks(c).map(\.id) == ["a", "t"])
        c.type("x")
        #expect(blocks(c).last?.text == "x")
        #expect(blocks(c).last?.id == "t")
    }

    /// Enter at the end of a folded heading starts its section, which
    /// shows; at the end of a closed toggle it makes one after what is
    /// folded in it, which stays in it.
    @Test func enterKeepsWhatIsFolded() {
        let c = editor([
            block("h", "heading", "Plan", data: ["level": .int(2), "collapsed": .bool(true)]),
            block("p", "paragraph", "Body"),
        ])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.type("\nNew")
        let b = blocks(c)
        #expect(b.map(\.text) == ["Plan", "New", "Body"])
        #expect(b.first?.data?["collapsed"]?.bool != true)

        let t = editor([
            block(
                "t", "toggle_list", "Details", data: ["collapsed": .bool(true)],
                children: [block("in", "paragraph", "Hidden")]),
            block("after", "paragraph", "After"),
        ])
        t.textView.selectedRange = NSRange(location: 4, length: 0)
        t.type("\n")
        var sent: DocContent?
        t.onEdit = { sent = $0 }
        t.send()
        let top = sent?.blocks ?? []
        #expect(top.map(\.text) == ["Deta", "ils", "After"])
        #expect(top.first?.children?.map(\.id) == ["in"])
        #expect(top[1].type == "toggle_list")
        #expect(t.paragraph(at: t.textView.selectedRange.location).info.id == top[1].id)

        // In an open toggle, the new line is its first block.
        let o = editor([block("t", "toggle_list", "Open")])
        o.textView.selectedRange = NSRange(location: 4, length: 0)
        o.type("\nIn")
        o.onEdit = { sent = $0 }
        o.send()
        #expect(sent?.blocks.first?.children?.map(\.text) == ["In"])
    }

    /// A block moves with the blocks nested in it, past the block before
    /// or after it at its depth; indented, they go in with it.
    @Test func blocksMoveAndNestWithWhatIsInThem() {
        let c = editor([
            block("a", "bulleted_list", "A", children: [block("b", "bulleted_list", "B")]),
            block("c", "bulleted_list", "C"),
        ])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.moveBlock(by: -1)
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.map(\.id) == ["c", "a"])
        #expect(sent?.blocks.last?.children?.map(\.id) == ["b"])
        #expect(c.paragraph(at: c.textView.selectedRange.location).info.id == "c")

        // A child moved above its parent goes out before it.
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        c.moveBlock(by: -1)
        c.send()
        #expect(sent?.blocks.map(\.id) == ["c", "b", "a"])
        #expect(c.paragraph(at: 2).info.depth == 0)

        let n = editor([
            block("a", "bulleted_list", "A"),
            block("b", "bulleted_list", "B", children: [block("x", "bulleted_list", "X")]),
        ])
        n.textView.selectedRange = NSRange(location: 2, length: 0)
        n.type("\t")
        n.onEdit = { sent = $0 }
        n.send()
        #expect(sent?.blocks.map(\.id) == ["a"])
        #expect(sent?.blocks.first?.children?.map(\.id) == ["b"])
        #expect(sent?.blocks.first?.children?.first?.children?.map(\.id) == ["x"])
    }

    /// A selection ending at a paragraph's start does not choose it.
    @Test func aSelectionEndingAtABlocksStartLeavesIt() {
        let c = editor([block("a", "paragraph", "One"), block("b", "paragraph", "Two")])
        c.textView.selectedRange = NSRange(location: 0, length: 4)
        c.setType("quote")
        #expect(blocks(c).map(\.type) == ["quote", "paragraph"])
    }

    /// Delete at a block's end joins the block after it, as Backspace at
    /// that one's start does not.
    @Test func deleteAtTheEndJoinsTheNextBlock() {
        let c = editor([
            block("a", "paragraph", "One"),
            block("b", "todo_list", "Two", data: ["checked": .bool(false)]),
        ])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.forwardDelete()
        let b = blocks(c)
        #expect(b.map(\.text) == ["OneTwo"])
        #expect(b.map(\.id) == ["a"])
    }

    /// Backspace at the start of the page turns a heading into text, which
    /// UIKit asks no rule about.
    @Test func backspaceAtThePagesStartTurnsAHeadingIntoText() {
        let c = editor([block("h", "heading", "Title", data: ["level": .int(1)])])
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.backspace()
        #expect(blocks(c).map(\.type) == ["paragraph"])
        #expect(blocks(c).first?.id == "h")
    }

    /// Return before the menu has its items is a new line.
    @Test func returnBeforeTheMenuLoadsIsANewLine() {
        let c = editor([block("a", "paragraph", "")])
        c.menuItems = { _, _ in }
        c.type("/")
        #expect(c.menu != nil)
        c.type("\n")
        #expect(c.menu == nil)
        #expect(blocks(c).map(\.text) == ["/", ""])
    }

    /// A subpage made from "/" goes where the "/" was, though the caret
    /// moved on while the page was made.
    @Test func aSubpageGoesWhereTheSlashWas() async throws {
        let c = editor([block("a", "paragraph", ""), block("b", "paragraph", "Later")])
        offer(c, [("subpage", "Subpage")])
        c.createSubpage = {
            try? await Task.sleep(for: .milliseconds(50))
            return "page1"
        }
        c.type("/sub")
        c.type("\n")
        c.textView.selectedRange = NSRange(location: c.textView.textStorage.length, length: 0)
        // The card goes in once the page is made.
        for _ in 0..<200 where !blocks(c).contains(where: { $0.type == "subpage" }) {
            try await Task.sleep(for: .milliseconds(10))
        }
        let b = blocks(c)
        #expect(b.map(\.type) == ["subpage", "paragraph", "paragraph"])
        #expect(b.last?.text == "Later")
    }

    /// VoiceOver checks a to-do from the actions at the caret.
    @Test func voiceOverChecksAToDo() throws {
        let c = editor([block("t", "todo_list", "Milk", data: ["checked": .bool(false)])])
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        let check = try #require(
            c.textView.accessibilityCustomActions?.first { $0.name == LI.markDone })
        _ = check.actionHandler?(check)
        #expect(blocks(c).first?.data?["checked"]?.bool == true)
    }

    /// The editor's format commands are offered, which UIKit offers only
    /// to a text view that edits its attributes itself.
    @Test func theFormatCommandsAreOffered() throws {
        let c = editor([block("a", "paragraph", "Hello")])
        let (window, done) = try show(c)
        defer { done() }
        _ = window
        #expect(
            c.textView.canPerformAction(#selector(UIResponder.toggleBoldface(_:)), withSender: nil))
        c.textView.isEditable = false
        #expect(
            !c.textView.canPerformAction(#selector(UIResponder.toggleBoldface(_:)), withSender: nil)
        )
    }

    /// Undo after a mention, a divider and a rule's change gives the text
    /// back as it was before each, the text view's own steps with them.
    @Test func undoFollowsTheEditorsChanges() async throws {
        let c = editor([block("a", "paragraph", "Hi"), block("b", "paragraph", "")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        offer(c, [("page:p1", "Groceries")])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        for ch in "@gro" {
            c.type(String(ch))
            try await Task.sleep(for: .milliseconds(20))
        }
        c.type("\n")
        try await Task.sleep(for: .milliseconds(20))
        #expect(c.textView.text == "Hi\nGroceries ")
        undo.undo()
        #expect(c.textView.text == "Hi\n@gro")
        undoAll(undo)
        #expect(c.textView.text == "Hi\n")

        for ch in "---" {
            c.type(String(ch))
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(blocks(c).map(\.type) == ["paragraph", "divider", "paragraph"])
        undo.undo()
        #expect(blocks(c).map(\.type) == ["paragraph", "paragraph"])
        undoAll(undo)
        #expect(c.textView.text == "Hi\n")
        #expect(blocks(c).map(\.id) == ["a", "b"])
    }

    /// Typing after the editor's own change is a step after it: undone
    /// first, and the change after it, each over the text it was made on.
    @Test func undoKeepsItsOrderAroundTheEditorsChanges() async throws {
        let c = editor([block("a", "paragraph", "")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        offer(c, [("page:p1", "Groceries")])
        func slowly(_ text: String) async throws {
            for ch in text {
                c.type(String(ch))
                try await Task.sleep(for: .milliseconds(20))
            }
        }
        try await slowly("x @gro\nyz")
        #expect(c.textView.text == "x Groceries yz")
        var texts: [String] = []
        while undo.canUndo, texts.count < 20 {
            undo.undo()
            texts.append(c.textView.text)
        }
        // The typing after the mention goes first, then the mention, which
        // gives back what it took the place of.
        let mention = try #require(texts.firstIndex(of: "x Groceries "))
        #expect(texts[..<mention].allSatisfy { $0.hasPrefix("x Groceries ") })
        #expect(texts.dropFirst(mention + 1).first == "x @gro")
        #expect(texts.last == "")
        #expect(!texts.contains { $0.contains("Groceries") && $0.contains("@") })
    }

    /// A to-do checked between typing is a step of its own.
    @Test func aToDoCheckedBetweenTypingIsAStepOfItsOwn() async throws {
        let c = editor([block("t", "todo_list", "", data: ["checked": .bool(false)])])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        let undo = try #require(c.textView.undoManager)
        for ch in "ab" {
            c.type(String(ch))
            try await Task.sleep(for: .milliseconds(20))
        }
        c.setBlock(c.paragraph(at: 0).info.setting("checked", .bool(true)), at: 0)
        try await Task.sleep(for: .milliseconds(20))
        c.type("c")
        try await Task.sleep(for: .milliseconds(20))
        undo.undo()
        #expect(c.textView.text == "ab")
        #expect(c.paragraph(at: 0).info.checked)
        undo.undo()
        #expect(c.textView.text == "ab")
        #expect(!c.paragraph(at: 0).info.checked)
    }

    /// Undoing Enter at a block's start gives the block back as it was:
    /// its ID, and its type.
    @Test func undoingEnterAtABlocksStartKeepsTheBlock() async throws {
        let c = editor([block("h", "heading", "Title", data: ["level": .int(2)])])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        let undo = try #require(c.textView.undoManager)
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.type("\n")
        try await Task.sleep(for: .milliseconds(20))
        #expect(blocks(c).map(\.type) == ["paragraph", "heading"])
        #expect(blocks(c).last?.id == "h")
        undoAll(undo)
        #expect(c.textView.text == "Title")
        #expect(blocks(c).map(\.id) == ["h"])
        #expect(blocks(c).first?.type == "heading")
        #expect(blocks(c).first?.data?["level"]?.int == 2)
    }

    /// The page's last block deleted, an image, leaves an empty paragraph:
    /// saved as an image without its picture, it would come back.
    @Test func deletingTheLastImageLeavesAParagraph() {
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        let c = editor([block("a", "paragraph", "One"), image])
        c.textView.selectedRange = NSRange(location: 4, length: 1)
        c.backspace()
        #expect(c.textView.text == "One\n")
        let b = blocks(c)
        #expect(b.map(\.type) == ["paragraph", "paragraph"])
        #expect(b.last?.data?["url"] == nil)
        #expect(b.last?.text == "")
        // A block without text and without its attachment is never
        // saved as that block.
        let node = DocumentMapper.node(
            for: BlockInfo(id: "i", type: "image", data: ["url": .string("x")]),
            text: NSAttributedString())
        #expect(node.type == "paragraph")
        #expect(node.data?["url"] == nil)
    }

    /// A word corrected, as autocorrect does, keeps its link and formats.
    @Test func aCorrectedWordKeepsItsLinkAndFormats() {
        var d: [String: JSONValue] = [:]
        d["delta"] = .array([
            .object(["insert": .string("See ")]),
            .object([
                "insert": .string("teh site"),
                "attributes": .object([
                    "href": .string("https://example.com"), "bold": .bool(true),
                ]),
            ]),
        ])
        let c = editor([DocNode(id: "a", type: "paragraph", data: d, children: nil)])
        c.textView.selectedRange = NSRange(location: 12, length: 0)
        c.correct(NSRange(location: 4, length: 3), with: "the")
        let r = runs(blocks(c).first)
        #expect(r.map(\.0) == ["See ", "the site"])
        #expect(r.last?.1?["href"]?.string == "https://example.com")
        #expect(r.last?.1?["bold"]?.bool == true)
    }

    /// An empty last to-do draws checked as it is checked, and unchecked
    /// again: a tap on its box goes by what it draws.
    @Test func anEmptyLastToDoDrawsAsItIsChecked() {
        let c = editor([
            block("a", "paragraph", "One"),
            block("t", "todo_list", "", data: ["checked": .bool(false)]),
        ])
        c.textView.frame = CGRect(x: 0, y: 0, width: 600, height: 800)
        c.textView.layoutIfNeeded()
        func drawn() -> BlockInfo? {
            guard let tlm = c.textView.textLayoutManager else { return nil }
            tlm.ensureLayout(for: tlm.documentRange)
            var last: BlockLayoutFragment?
            tlm.enumerateTextLayoutFragments(from: tlm.documentRange.location, options: []) { f in
                last = f as? BlockLayoutFragment
                return true
            }
            return last?.trailing?.info
        }
        #expect(drawn()?.id == "t")
        #expect(drawn()?.checked == false)
        let end = c.textView.textStorage.length
        c.setBlock(c.paragraph(at: end).info.setting("checked", .bool(true)), at: end)
        c.textView.layoutIfNeeded()
        #expect(drawn()?.checked == true)
        c.setBlock(c.paragraph(at: end).info.setting("checked", .bool(false)), at: end)
        c.textView.layoutIfNeeded()
        #expect(drawn()?.checked == false)
    }

    /// A change from outside keeps the selection, not only the caret.
    @Test func changesFromOutsideKeepTheSelection() {
        let c = editor([block("a", "paragraph", "One"), block("b", "paragraph", "Two")])
        // From after "O" to after "Tw".
        c.textView.selectedRange = NSRange(location: 1, length: 5)
        c.update(
            DocContent(
                document: DocNode(
                    id: "root", type: "page", data: nil,
                    children: [
                        block("a", "paragraph", "One more"), block("b", "paragraph", "Two"),
                    ]),
                meta: nil),
            tinted: [])
        #expect(c.textView.selectedRange == NSRange(location: 1, length: 10))
    }

    /// A change from outside, which has what was typed or follows it,
    /// leaves nothing waiting to be sent.
    @Test func aChangeFromOutsideLeavesNothingToSend() {
        let c = editor([block("a", "paragraph", "One")])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.type("!")
        #expect(c.hasPendingEdit)
        c.update(
            DocContent(
                document: DocNode(
                    id: "root", type: "page", data: nil,
                    children: [block("a", "paragraph", "One! And more")]),
                meta: nil),
            tinted: [])
        #expect(!c.hasPendingEdit)
    }

    /// The menu at the caret goes on from its "/" as a change from outside
    /// moves it, and takes the place of the "/" alone. On screen, as the
    /// app has it, the selection's changes on the way are UIKit's own.
    @Test func theMenuFollowsItsSlashThroughAChangeFromOutside() async throws {
        let c = editor([block("a", "paragraph", "Intro"), block("b", "paragraph", "")])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        offer(c, [("h1", "Heading 1")])
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        for ch in "/h" {
            c.type(String(ch))
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(c.menu?.anchor == 6)
        c.update(
            DocContent(
                document: DocNode(
                    id: "root", type: "page", data: nil,
                    children: [
                        block("a", "paragraph", "A longer intro"), block("b", "paragraph", "/h"),
                    ]),
                meta: nil),
            tinted: [])
        #expect(c.menu?.anchor == 15)
        #expect(c.textView.selectedRange == NSRange(location: 17, length: 0))
        try await Task.sleep(for: .milliseconds(20))
        #expect(c.menu?.anchor == 15)
        c.type("\n")
        let b = blocks(c)
        #expect(b.map(\.text) == ["A longer intro", ""])
        #expect(b.map(\.type) == ["paragraph", "heading"])
    }

    /// Two blocks of one ID are told apart once, the second given an ID
    /// it keeps as the page is sent again.
    @Test func aDuplicateIDIsReplacedOnce() {
        let c = editor([block("a", "paragraph", "One"), block("a", "paragraph", "Two")])
        let first = blocks(c)
        #expect(first.first?.id == "a")
        #expect(Set(first.map(\.id)).count == 2)
        #expect(blocks(c).map(\.id) == first.map(\.id))
    }

    /// Blocks in text that holds none, as a quote, come after it: left out,
    /// they would be deleted as the page is sent.
    @Test func blocksInTextThatHoldsNoneComeAfterIt() {
        let c = editor([
            block("q", "quote", "Said", children: [block("in", "paragraph", "Inside")]),
            block("after", "paragraph", "After"),
        ])
        #expect(c.textView.text == "Said\nInside\nAfter")
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.map(\.id) == ["q", "in", "after"])
        #expect(sent?.blocks.first?.children == nil)
    }

    /// settle waits for the keyboard to come up, as an iPhone's software
    /// keyboard does, in steps, and to stop changing the room the page has.
    private func settle(_ c: EditorController) async throws {
        var last = c.textView.adjustedContentInset
        var still = 0
        for _ in 0..<40 where still < 3 {
            try await Task.sleep(for: .milliseconds(100))
            let now = c.textView.adjustedContentInset
            still = now == last ? still + 1 : 0
            last = now
        }
    }

    /// undoAll undoes every step there is, a bounded number of them: an
    /// undo that made one more would otherwise never end the test.
    private func undoAll(_ undo: UndoManager) {
        for _ in 0..<100 where undo.canUndo { undo.undo() }
        #expect(!undo.canUndo)
    }

    /// show puts the editor in a window with the keyboard, as the app does,
    /// and returns what takes it away again.
    private func show(_ c: EditorController) throws -> (UIWindow, () -> Void) {
        let scene = try #require(
            UIApplication.shared.connectedScenes.first { $0 is UIWindowScene } as? UIWindowScene)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 1000, height: 700)
        window.addSubview(c.textView)
        c.textView.frame = window.bounds
        window.makeKeyAndVisible()
        c.textView.layoutIfNeeded()
        _ = c.textView.becomeFirstResponder()
        return (
            window,
            {
                _ = c.textView.resignFirstResponder()
                c.textView.removeFromSuperview()
                window.isHidden = true
                window.windowScene = nil
            }
        )
    }
}

extension EditorController {
    /// type types text a character at a time as the keyboard does: through
    /// the editor's rules, which a text view not on screen leaves out, and
    /// in UIKit's order, the selection's change before the text's.
    func type(_ text: String) {
        for ch in text { paste(String(ch)) }
    }

    /// paste puts text in at once, as a paste or dictation does.
    func paste(_ text: String) {
        // A text view on screen asks the rules itself, and says what
        // changed: asked again, they would take the text twice.
        if textView.window != nil { return textView.insertText(text) }
        if textView(textView, shouldChangeTextIn: textView.selectedRange, replacementText: text) {
            textView.insertText(text)
            textViewDidChangeSelection(textView)
            textViewDidChange(textView)
        }
    }

    /// backspace deletes as the keyboard's Backspace does: at the start of
    /// the page, UIKit asks no rule, and the text view's own takes it.
    func backspace() {
        let sel = textView.selectedRange
        guard sel.length > 0 || sel.location > 0 else { return textView.deleteBackward() }
        let range = sel.length > 0 ? sel : NSRange(location: sel.location - 1, length: 1)
        if textView(textView, shouldChangeTextIn: range, replacementText: "") {
            textView.deleteBackward()
            textViewDidChangeSelection(textView)
            textViewDidChange(textView)
        }
    }

    /// correct puts text in place of range, the caret elsewhere, as
    /// autocorrect, a text replacement or dictation corrects a word.
    func correct(_ range: NSRange, with text: String) {
        guard textView(textView, shouldChangeTextIn: range, replacementText: text),
            let start = textView.position(
                from: textView.beginningOfDocument, offset: range.location),
            let end = textView.position(from: start, offset: range.length),
            let r = textView.textRange(from: start, to: end)
        else { return }
        textView.replace(r, withText: text)
        textViewDidChangeSelection(textView)
        textViewDidChange(textView)
    }

    /// forwardDelete deletes after the caret, as a keyboard's Delete does.
    func forwardDelete() {
        let range = NSRange(location: textView.selectedRange.location, length: 1)
        guard textView(textView, shouldChangeTextIn: range, replacementText: ""),
            let start = textView.position(
                from: textView.beginningOfDocument, offset: range.location),
            let end = textView.position(from: start, offset: 1),
            let r = textView.textRange(from: start, to: end)
        else { return }
        textView.replace(r, withText: "")
        textViewDidChangeSelection(textView)
        textViewDidChange(textView)
    }

    /// press presses a key the text view has a command for.
    func press(_ input: String, _ flags: UIKeyModifierFlags = []) {
        guard
            let command = textView.keyCommands?.first(where: {
                $0.input == input && $0.modifierFlags == flags
            }), let action = command.action
        else {
            Issue.record("no command for \(input)")
            return
        }
        textView.perform(action, with: command)
    }
}
