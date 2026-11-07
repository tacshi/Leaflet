import Foundation
import ImageIO
import LeafletCore
import SwiftUI
import Testing
import UIKit
import UniformTypeIdentifiers

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

    /// A block whose text holds a break TextKit ends a paragraph at, as
    /// from another app or device, stays one block: shown as it is, it
    /// split the block in two, one more each time the page was saved.
    @Test func paragraphBreaksInTextStayInTheBlock() {
        for brk in ["\r", "\r\n", "\u{2029}", "\u{85}"] {
            let c = editor([
                block("a", "paragraph", "one\(brk)two"), block("b", "paragraph", "after"),
            ])
            var sent: DocContent?
            c.onEdit = { sent = $0 }
            c.send()
            let blocks = sent?.document.children ?? []
            #expect(blocks.map(\.id) == ["a", "b"], "\(brk.unicodeScalars.map(\.value))")
            #expect(
                blocks.first?.data?["delta"]?.array?.first?["insert"]?.string == "one\ntwo",
                "\(brk.unicodeScalars.map(\.value))")
        }
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

    /// The menus open for full-width "／" and "＠", as Chinese and Japanese
    /// input methods type them, and the / menu for "、" at a block's start
    /// or after a space, as on the desktop; "－－－" makes a divider.
    @Test func fullWidthMenusAndDividersFollowTheDesktop() {
        for (typed, trigger) in [("／", "/"), ("＠", "@"), ("、", "/")] {
            let c = editor([block("a", "paragraph", "")])
            offer(c, [("h1", "Heading 1")])
            c.type(typed)
            #expect(c.menu?.trigger == Character(trigger), "\(typed)")
            c.type("\n")
            #expect(c.menu == nil, "\(typed)")
        }
        // "、" is a comma in Chinese text.
        let comma = editor([block("a", "paragraph", "")])
        offer(comma, [("h1", "Heading 1")])
        comma.type("字、")
        #expect(comma.menu == nil)
        let slash = editor([block("a", "paragraph", "")])
        offer(slash, [("h1", "Heading 1")])
        slash.type("字／")
        #expect(slash.menu?.trigger == "/")

        let d = editor([block("a", "paragraph", "")])
        d.type("－－－")
        #expect(blocks(d).map(\.type) == ["divider", "paragraph"])
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

    /// A version reads as the page's blocks, as the core reads Markdown:
    /// a heading, a to-do, a numbered item and a table.
    @Test func versionsReadAsBlocks() {
        let md = "## Plan\n\n- [ ] Pack\n1. Go\n\n| | A |\n| --- | --- |\n| x | 1 |"
        let rows = MarkdownPreview.rows(MarkdownPreview.blocks(md))
        #expect(rows.map(\.node.type) == ["heading", "todo_list", "numbered_list", "table"])
        #expect(rows[0].node.data?["level"]?.int == 2)
        #expect(MarkdownPreview.marker(rows[1]) == "☐")
        #expect(MarkdownPreview.marker(rows[2]) == "1.")
        #expect(TableGrid.rows(rows[3].node) == [["", "A"], ["x", "1"]])
        // Read with the rows, off the main thread, as a preview shows them.
        #expect(rows[3].cells == [["", "A"], ["x", "1"]])
    }

    /// A comment's links are only those the page opens: a web, mail or
    /// page link, not a file's or another app's, which whoever comments
    /// could write; their text stays.
    @Test func aCommentLinksOnlyWhatThePageOpens() {
        let text = CommentRow.text(
            "[a](https://example.com) [b](shortcuts://run-shortcut?name=X) [c](file:///etc/hosts) "
                + "[d](leaflet://page/p1) [e](mailto:me@example.com)")
        #expect(String(text.characters) == "a b c d e")
        #expect(
            text.runs.compactMap { $0.link?.absoluteString } == [
                "https://example.com", "leaflet://page/p1", "mailto:me@example.com",
            ])
    }

    /// An agent's answer reads as blocks too: a fence's lines are code,
    /// though one starts with "#", a fence still open as it streams in
    /// runs to the end, and inline formats and links stay.
    @Test func answersReadAsBlocks() {
        let md =
            "Run **this**:\n\n```sh\n# not a heading\nls\n```\n\n1. [Go](https://go.dev)\n2. On\n\n```go\nfunc f() {"
        let rows = MarkdownPreview.rows(MarkdownPreview.blocks(md))
        #expect(
            rows.map(\.node.type) == [
                "paragraph", "code", "numbered_list", "numbered_list", "code",
            ])
        #expect(MarkdownPreview.source(rows[1].node) == "# not a heading\nls")
        #expect(MarkdownPreview.marker(rows[3]) == "2.")
        #expect(MarkdownPreview.source(rows[4].node) == "func f() {")
        let text = MarkdownPreview.text(rows[0].node)
        let bold = text.runs.first { String(text[$0.range].characters) == "this" }
        #expect(bold?.inlinePresentationIntent == .stronglyEmphasized)
        let link = MarkdownPreview.text(rows[2].node).runs.first { $0.link != nil }?.link
        #expect(link == URL(string: "https://go.dev"))
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

    /// Tab and Shift-Tab over a code block's lines indent them and take
    /// the indent off again, each line's formats staying where they were.
    @Test func codeIndentedKeepsItsFormats() {
        var d = block("c", "code", "")
        d.data?["delta"] = .array([
            .object(["insert": .string("let ")]),
            .object(["insert": .string("a"), "attributes": .object(["bold": .bool(true)])]),
            .object(["insert": .string("\nb")]),
        ])
        let c = editor([d])
        c.textView.selectedRange = NSRange(location: 0, length: 7)
        c.indent(by: 1)
        #expect(runs(blocks(c).first).map(\.0) == ["  let ", "a", "\n  b"])
        #expect(runs(blocks(c).first)[1].1?["bold"]?.bool == true)
        c.textView.selectedRange = NSRange(location: 0, length: 11)
        c.indent(by: -1)
        #expect(runs(blocks(c).first).map(\.0) == ["let ", "a", "\nb"])
        #expect(runs(blocks(c).first)[1].1?["bold"]?.bool == true)
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

    /// Lines pasted are a block each, read as Markdown as on the desktop:
    /// the first joins the line it goes in, plain lines are paragraphs and
    /// "- " ones list items. They keep their IDs as the page is sent again;
    /// in code they are the block's lines.
    @Test func pastedLinesAreABlockEach() {
        let c = editor([block("a", "bulleted_list", "x")])
        c.textView.selectedRange = NSRange(location: 1, length: 0)
        c.paste("a\nb\nc\n- d")
        let first = blocks(c)
        #expect(first.map(\.text) == ["xa", "b", "c", "d"])
        #expect(first.map(\.type) == ["bulleted_list", "paragraph", "paragraph", "bulleted_list"])
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

    /// Backspace after a closed toggle joins the block to the toggle's own
    /// line, the block before it that shows, past what is folded in it,
    /// which stays folded; Delete at the toggle's end does the same. The
    /// key's own join would put it in the last of the folded blocks, out
    /// of sight.
    @Test func backspaceAndDeleteGoPastWhatIsFolded() {
        func folded() -> EditorController {
            editor([
                block(
                    "t", "toggle_list", "Details", data: ["collapsed": .bool(true)],
                    children: [block("in", "paragraph", "Hidden")]),
                block("b", "paragraph", "After"),
            ])
        }
        var sent: DocContent?
        // "Details\nHidden\nAfter", the caret at the start of After.
        let c = folded()
        c.textView.selectedRange = NSRange(location: 15, length: 0)
        c.backspace()
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.map(\.text) == ["DetailsAfter"])
        #expect(sent?.blocks.first?.children?.map(\.text) == ["Hidden"])
        #expect(sent?.blocks.first?.data?["collapsed"]?.bool == true)
        #expect(c.textView.selectedRange == NSRange(location: 7, length: 0))

        let d = folded()
        d.textView.selectedRange = NSRange(location: 7, length: 0)
        d.forwardDelete()
        d.onEdit = { sent = $0 }
        d.send()
        #expect(sent?.blocks.map(\.text) == ["DetailsAfter"])
        #expect(sent?.blocks.first?.children?.map(\.text) == ["Hidden"])
        #expect(d.textView.selectedRange == NSRange(location: 7, length: 0))
    }

    /// Delete at the end of a folded heading joins the heading after it,
    /// which ended its section: what that one's section showed still
    /// shows, the heading unfolding, as on the desktop, whether its own
    /// section held blocks or none.
    @Test func deleteAtAFoldedHeadingKeepsWhatShowedAfter() {
        for body in [[block("p", "paragraph", "Body")], []] {
            let c = editor(
                [block("h", "heading", "Plan", data: ["level": .int(2), "collapsed": .bool(true)])]
                    + body + [
                        block("n", "heading", "Next", data: ["level": .int(2)]),
                        block("q", "paragraph", "Shown"),
                    ])
            c.textView.selectedRange = NSRange(location: 4, length: 0)
            c.forwardDelete()
            let b = blocks(c)
            #expect(b.map(\.text) == ["PlanNext"] + body.map(\.text) + ["Shown"])
            #expect(b.first?.data?["collapsed"]?.bool != true)
        }
    }

    /// Delete at the end of a closed toggle with nothing shown after it
    /// does nothing, as on the desktop: what is folded in it stays there.
    @Test func deleteAtAClosedToggleLastOnThePageLeavesIt() {
        let c = editor([
            block(
                "t", "toggle_list", "Details", data: ["collapsed": .bool(true)],
                children: [block("in", "paragraph", "Hidden")])
        ])
        c.textView.selectedRange = NSRange(location: 7, length: 0)
        c.forwardDelete()
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.map(\.text) == ["Details"])
        #expect(sent?.blocks.first?.children?.map(\.text) == ["Hidden"])
        #expect(c.textView.selectedRange == NSRange(location: 7, length: 0))
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

    /// A to-do at the caret offers a date however full its last line is:
    /// one that leaves no room takes some while it offers one, gives it
    /// back as the caret leaves, and says nothing new in the page.
    @Test func aFullLastLineStillOffersADate() async throws {
        var made = false
        for words in 14...40 {
            let text = Array(repeating: "word", count: words).joined(separator: " ")
            let c = editor([
                block("t", "todo_list", text, data: ["checked": .bool(false)]),
                block("p", "paragraph", "After"),
            ])
            let (_, done) = try show(c)
            defer { done() }
            try await settle(c)
            let (range, info) = c.text.paragraph(at: 0)
            let before = blocks(c)
            c.textView.selectedRange = NSRange(location: 1, length: 0)
            c.textViewDidChangeSelection(c.textView)
            try await settle(c)
            try await settle(c)
            #expect(c.due.chipRect(info) != nil, "\(words) words")
            let style =
                c.text.storage.attribute(
                    .paragraphStyle, at: range.location, effectiveRange: nil) as? NSParagraphStyle
            guard style?.tailIndent != 0 else { continue }
            made = true
            #expect(blocks(c) == before)
            c.textView.selectedRange = NSRange(location: NSMaxRange(range) + 2, length: 0)
            c.textViewDidChangeSelection(c.textView)
            try await settle(c)
            try await settle(c)
            let back =
                c.text.storage.attribute(
                    .paragraphStyle, at: range.location, effectiveRange: nil) as? NSParagraphStyle
            #expect(back?.tailIndent == 0)
            break
        }
        #expect(made, "no length left its last line too full for the offer")
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

    /// Undo, again and again, goes back through a mention taken whole, as
    /// through the typing around it, to the page as it was.
    @Test func undoAllAfterAMentionTakenWhole() async throws {
        let c = editor([block("a", "paragraph", "Hi")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        offer(c, [("page:p1", "Groceries")])
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        try await slowly(c, " @gro\n")
        #expect(c.textView.text == "Hi Groceries ")
        // Typing inside the mention goes after it; Backspace in it takes
        // all of it.
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        try await slowly(c, "x")
        #expect(c.textView.text == "Hi Groceriesx ")
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        c.backspace()
        #expect(c.textView.text == "Hi x ")
        try await slowly(c, "yz")
        undoAll(undo)
        #expect(c.textView.text == "Hi")
    }

    /// A letter typed on a picture chosen whole, as Backspace and Delete
    /// choose it, starts a block after it, as on the desktop: the picture
    /// stays.
    @Test func typingOnAPictureChosenWholeKeepsIt() {
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        let c = editor([image, block("p", "paragraph", "After")])
        c.textView.selectedRange = NSRange(location: 0, length: 1)
        c.type("x")
        let b = blocks(c)
        #expect(b.map(\.type) == ["image", "paragraph", "paragraph"])
        #expect(b.map(\.text) == ["", "x", "After"])
        #expect(b.first?.id == "i")
    }

    /// Text typed or pasted over a selection that starts at a picture
    /// takes its place, the picture with it, as over any other; from the
    /// picture's end, the picture stays and the text goes after it.
    @Test func textOverASelectionFromAPictureReplacesIt() async throws {
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        // All of the page, then from the picture's end to the end of After.
        for (chosen, types, texts) in [
            (NSRange(location: 0, length: 7), ["paragraph"], ["New"]),
            (NSRange(location: 1, length: 6), ["image", "paragraph"], ["", "New"]),
        ] {
            let c = editor([image, block("p", "paragraph", "After")])
            let (_, done) = try show(c)
            try await settle(c)
            c.textView.selectedRange = chosen
            c.paste("New")
            try await Task.sleep(for: .milliseconds(20))
            #expect(blocks(c).map(\.type) == types)
            #expect(blocks(c).map(\.text) == texts)
            done()
        }
    }

    /// Undo, again and again, goes back through lines pasted as blocks, a
    /// fence's code block, and the lines and indents typed in it.
    @Test func undoAllAfterPastedLinesAndCode() async throws {
        let c = editor([block("a", "paragraph", "Hi")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        try await slowly(c, "\n")
        c.paste("- one\n- two")
        try await Task.sleep(for: .milliseconds(20))
        #expect(blocks(c).map(\.type) == ["paragraph", "bulleted_list", "bulleted_list"])
        try await slowly(c, "\n\n```go\n")
        #expect(blocks(c).contains { $0.type == "code" })
        try await slowly(c, "if x {\n")
        c.indent(by: 1)
        try await slowly(c, "y\n\n\nend")
        undoAll(undo)
        #expect(c.textView.text == "Hi")
    }

    /// Enter, lines pasted, Enter again: undo, again and again, takes back
    /// each, the line breaks too, to the page as it was. Undoing a list
    /// item's line break went through Backspace's rule, which refused it,
    /// and the steps before it, undone where the text no longer was,
    /// crashed the app.
    @Test func undoAllAfterEnterAndAPaste() async throws {
        let c = editor([block("a", "paragraph", "Hi")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        try await slowly(c, "\n")
        c.paste("- one\n- two")
        try await Task.sleep(for: .milliseconds(20))
        try await slowly(c, "\n")
        var texts: [String] = []
        for _ in 0..<100 where undo.canUndo {
            undo.undo()
            texts.append(c.textView.text)
        }
        #expect(texts.last == "Hi")
        #expect(texts.contains("Hi\n"))
        #expect(blocks(c).map(\.id) == ["a"])
    }

    /// Undo takes back a list item's line break, as a paragraph's, though
    /// Backspace at a list item's start makes it a paragraph instead.
    @Test func undoTakesBackAListItemsLineBreak() async throws {
        for type in ["bulleted_list", "todo_list", "paragraph"] {
            let c = editor([block("a", type, "x")])
            let (_, done) = try show(c)
            try await settle(c)
            let undo = try #require(c.textView.undoManager)
            c.textView.selectedRange = NSRange(location: 1, length: 0)
            try await slowly(c, "\ny")
            undoAll(undo)
            #expect(c.textView.text == "x", "\(type)")
            #expect(blocks(c).map(\.id) == ["a"], "\(type)")
            done()
        }
    }

    /// Undo, again and again, goes back through a format typed in full.
    @Test func undoAllAfterAnInlineFormat() async throws {
        let c = editor([block("a", "paragraph", "")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        try await slowly(c, "a **b** c")
        undoAll(undo)
        #expect(c.textView.text == "")
    }

    /// Blocks copied paste back as the blocks they were, as on the desktop,
    /// with IDs of their own: the clipboard has their Markdown, for other
    /// apps; text within a block pastes back with its formats.
    @Test func copiedBlocksPasteBack() async throws {
        var bold = block("c", "paragraph", "")
        bold.data?["delta"] = .array([
            .object(["insert": .string("Bold"), "attributes": .object(["bold": .bool(true)])])
        ])
        let c = editor([
            block("a", "bulleted_list", "abcd"), block("b", "bulleted_list", "efgh"), bold,
            block("p", "paragraph", ""),
        ])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        c.copyText = { _ in "- abcd\n- efgh" }
        c.textView.selectedRange = NSRange(location: 0, length: 9)
        c.textView.copy(nil)
        for _ in 0..<50 where UIPasteboard.general.string != "- abcd\n- efgh" {
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(UIPasteboard.general.string == "- abcd\n- efgh")
        c.textView.selectedRange = NSRange(location: 15, length: 0)
        c.paste(UIPasteboard.general.string ?? "")
        var b = blocks(c)
        #expect(
            b.map(\.type) == [
                "bulleted_list", "bulleted_list", "paragraph", "bulleted_list", "bulleted_list",
            ])
        #expect(b.map(\.text) == ["abcd", "efgh", "Bold", "abcd", "efgh"])
        #expect(Set(b.map(\.id)).count == 5)

        // "Bold", within its block, pastes bold.
        c.textView.selectedRange = NSRange(location: 10, length: 4)
        c.textView.copy(nil)
        try await Task.sleep(for: .milliseconds(20))
        c.textView.selectedRange = NSRange(location: 5, length: 0)
        c.paste(UIPasteboard.general.string ?? "")
        b = blocks(c)
        #expect(b[1].text == "Boldefgh")
        let run = b[1].data?["delta"]?.array?.first?.object
        #expect(run?["attributes"]?.object?["bold"] == .bool(true))
    }

    /// An agent's answer or a transcript put in the page goes after the
    /// block and those in it, at its depth, and is one step to undo, the
    /// typing before it a step still.
    @Test func blocksPutInAreOneStepToUndo() async throws {
        let c = editor([
            block("a", "bulleted_list", "One", children: [block("n", "paragraph", "Nested")]),
            block("b", "paragraph", "Two"),
        ])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        let undo = try #require(c.textView.undoManager)
        c.textView.selectedRange = NSRange(location: 14, length: 0)
        try await slowly(c, "!")
        c.insertBlocks(
            [block("x", "paragraph", "Answer"), block("y", "bulleted_list", "Point")], after: "a")
        var b = blocks(c)
        #expect(b.map(\.id) == ["a", "n", "x", "y", "b"])
        #expect(
            b.map(\.type) == [
                "bulleted_list", "paragraph", "paragraph", "bulleted_list", "paragraph",
            ])
        #expect(b.map(\.text) == ["One", "Nested", "Answer", "Point", "Two!"])
        #expect(c.textView.selectedRange == NSRange(location: 28, length: 0))
        // After the nested block, they are in the item too: a step of its
        // own, as a tap later is.
        try await Task.sleep(for: .milliseconds(20))
        c.insertBlocks([block("z", "paragraph", "Inside")], after: "n")
        try await Task.sleep(for: .milliseconds(20))
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks.first?.children?.map(\.id) == ["n", "z"])
        undo.undo()
        b = blocks(c)
        #expect(b.map(\.id) == ["a", "n", "x", "y", "b"])
        undo.undo()
        #expect(c.textView.text == "One\nNested\nTwo!")
        // The typing before them is a step still, and they come back.
        #expect(undo.canUndo)
        undo.redo()
        b = blocks(c)
        #expect(b.map(\.text) == ["One", "Nested", "Answer", "Point", "Two!"])
        #expect(
            b.map(\.type) == [
                "bulleted_list", "paragraph", "paragraph", "bulleted_list", "paragraph",
            ])
    }

    /// undoesAndRedoes types, then has command change the page, and checks
    /// that the change is a step of its own that keeps those before it:
    /// undo, again and again, goes back to the page as it was, its text and
    /// its blocks' IDs, and redo, again and again, to the page after it.
    private func undoesAndRedoes(
        _ c: EditorController, typing: String, _ command: @MainActor () -> Void
    ) async throws {
        let undo = try #require(c.textView.undoManager)
        let start = (c.textView.text ?? "", blocks(c).map(\.id))
        try await slowly(c, typing)
        command()
        try await Task.sleep(for: .milliseconds(20))
        let after = (c.textView.text ?? "", blocks(c).map(\.id))
        undoAll(undo)
        #expect(c.textView.text == start.0)
        #expect(blocks(c).map(\.id) == start.1)
        for _ in 0..<100 where undo.canRedo { undo.redo() }
        #expect(c.textView.text == after.0)
        #expect(blocks(c).map(\.id) == after.1)
    }

    /// A block put in from a sheet or an upload is a step to undo after the
    /// typing before it, which stays.
    @Test func aBlockPutInKeepsTheStepsBefore() async throws {
        let c = editor([block("a", "paragraph", "Hi")])
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        try await undoesAndRedoes(c, typing: " there") { c.insertBlock(image) }
        #expect(blocks(c).map(\.type) == ["paragraph", "image", "paragraph"])
    }

    /// A block's sheet saved, as a table's cells, is a step to undo after
    /// the typing before it.
    @Test func aSheetSavedKeepsTheStepsBefore() async throws {
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
        let (_, done) = try show(c)
        try await settle(c)
        defer { done() }
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        try await undoesAndRedoes(c, typing: "s") { c.replaceBlock(table([["A", "B"]])) }
        let t = blocks(c).first { $0.id == "t" }
        #expect(t.map(TableGrid.rows) == [["A", "B"]])
    }

    /// A "/" or "@" item picked with a tap, not Return, is a step to undo
    /// after the typing before it.
    @Test func aMenuItemTappedKeepsTheStepsBefore() async throws {
        for (typing, key, label) in [
            ("x /h", "h1", "Heading 1"), ("x @gro", "page:p1", "Groceries"),
        ] {
            let c = editor([block("a", "paragraph", "")])
            let (_, done) = try show(c)
            try await settle(c)
            offer(c, [(key, label)])
            try await undoesAndRedoes(c, typing: typing) {
                guard let menu = c.menu, let item = menu.current else {
                    Issue.record("no menu for \(typing)")
                    return
                }
                menu.onPick?(item)
            }
            done()
        }
    }

    /// A block moved, or nested, is a step to undo after the typing before
    /// it.
    @Test func aBlockMovedOrNestedKeepsTheStepsBefore() async throws {
        for command in ["move", "nest"] {
            let c = editor([
                block("a", "bulleted_list", "One"), block("b", "bulleted_list", "Two"),
            ])
            let (_, done) = try show(c)
            try await settle(c)
            c.textView.selectedRange = NSRange(location: 7, length: 0)
            try await undoesAndRedoes(c, typing: "!") {
                if command == "move" { c.moveBlock(by: -1) } else { c.indent(by: 1) }
            }
            #expect(c.textView.text == (command == "move" ? "Two!\nOne" : "One\nTwo!"))
            done()
        }
    }

    /// slowly types text a key at a time, as a person does, the editor's
    /// changes after each made before the next.
    private func slowly(_ c: EditorController, _ text: String) async throws {
        for ch in text {
            c.type(String(ch))
            try await Task.sleep(for: .milliseconds(20))
        }
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

    /// Enter at a block's start makes an empty paragraph before it, and the
    /// text keeps its block: its ID, and its type.
    @Test func enterAtABlocksStartKeepsTheBlock() {
        let c = editor([block("h", "heading", "Title", data: ["level": .int(2)])])
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.type("\n")
        let b = blocks(c)
        #expect(b.map(\.type) == ["paragraph", "heading"])
        #expect(b.map(\.text) == ["", "Title"])
        #expect(b.last?.id == "h")
        #expect(b.last?.data?["level"]?.int == 2)
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

    /// Enter, Backspace and Tab go by the core's rules, the desktop's: an
    /// empty heading writes on after it; an empty paragraph in a toggle
    /// with more after it stays in it, and Backspace at its start joins it
    /// to the block before; Tab nests text in a toggle, not in a list item.
    @Test func enterBackspaceAndTabFollowTheCoresRules() {
        let h = editor([block("h", "heading", "", data: ["level": .int(2)])])
        h.textView.selectedRange = NSRange(location: 0, length: 0)
        h.type("\n")
        #expect(blocks(h).map(\.type) == ["heading", "paragraph"])

        let toggle = {
            self.editor([
                self.block(
                    "t", "toggle_list", "T",
                    children: [self.block("a", "paragraph", ""), self.block("b", "paragraph", "B")])
            ])
        }
        let e = toggle()
        e.textView.selectedRange = NSRange(location: 2, length: 0)
        e.type("\n")
        var sent: DocContent?
        e.onEdit = { sent = $0 }
        e.send()
        #expect(sent?.blocks.map(\.id) == ["t"])
        #expect(sent?.blocks.first?.children?.count == 3)

        let j = toggle()
        j.textView.selectedRange = NSRange(location: 2, length: 0)
        j.type("x")
        j.textView.selectedRange = NSRange(location: 2, length: 0)
        j.backspace()
        j.onEdit = { sent = $0 }
        j.send()
        #expect(sent?.blocks.map(\.text) == ["Tx"])
        #expect(sent?.blocks.first?.children?.map(\.id) == ["b"])

        let l = editor([block("l", "bulleted_list", "Item"), block("p", "paragraph", "Text")])
        l.textView.selectedRange = NSRange(location: 5, length: 0)
        l.type("\t")
        #expect(l.paragraph(at: 5).info.depth == 0)
        let g = editor([block("g", "toggle_list", "Toggle"), block("p", "paragraph", "Text")])
        g.textView.selectedRange = NSRange(location: 7, length: 0)
        g.type("\t")
        #expect(g.paragraph(at: 7).info.depth == 1)
    }

    /// Enter on the second blank line at the end of a code block leaves it
    /// for the empty paragraph after it, as on the desktop, without making
    /// another.
    @Test func leavingCodeGoesToTheEmptyLineAfterIt() {
        let c = editor([block("k", "code", "x\n\n"), block("p", "paragraph", "")])
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.type("\n")
        #expect(blocks(c).map(\.id) == ["k", "p"])
        #expect(blocks(c).first?.text == "x")
        #expect(c.paragraph(at: c.textView.selectedRange.location).info.id == "p")
    }

    /// What the core decides for the page shows: halfwidth katakana are
    /// Japanese, set higher; a link to a file, or a page link with no page,
    /// opens nothing.
    @Test func textAndLinksAsTheCoreReadsThem() {
        #expect(EditorStyle.mostlyCJK("ﾊﾝｶｸｶﾀｶﾅ"))
        #expect(!EditorStyle.mostlyCJK("Plain English"))
        #expect(EditorStyle.linkTarget([.leafletHref: "file:///etc/hosts"]) == nil)
        #expect(EditorStyle.linkTarget([.leafletHref: "leaflet://page/"]) == nil)
        #expect(
            EditorStyle.linkTarget([.leafletHref: " https://example.com "])
                == URL(string: "https://example.com"))
    }

    /// A transcript goes after the recording's block it was asked from, as
    /// on the desktop: a copied page's block has an ID of its own, which
    /// the recording doesn't know.
    @Test func aTranscriptGoesAfterTheBlockItWasAskedFrom() {
        let copy = DocNode(
            id: "copy", type: "recording", data: ["recording_id": .string("r")], children: nil)
        let c = editor([block("a", "paragraph", "Before"), copy, block("b", "paragraph", "After")])
        Recorder.put(Transcript(blocks: [block("t", "paragraph", "Said")]), after: "copy", in: c)
        #expect(blocks(c).map(\.id) == ["a", "copy", "t", "b"])
    }

    /// content is a page of blocks, as the core sends it.
    private func content(_ blocks: [DocNode]) -> DocContent {
        DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil)
    }

    /// The keyboard bar's Insert offers what "/" does and puts it in as "/"
    /// does: after the block with text the caret is in, which it leaves as
    /// it was.
    @Test func insertPutsABlockAfterTheText() async {
        let c = editor([block("a", "paragraph", "Plan")])
        offer(c, [("h1", "Heading 1"), ("table", "Table")])
        c.textView.selectedRange = NSRange(location: 4, length: 0)
        let offered = await withCheckedContinuation { done in
            c.insertMenu { done.resume(returning: $0) }
        }
        let titles = offered.flatMap { ($0 as? UIMenu)?.children ?? [$0] }.compactMap {
            ($0 as? UIAction)?.title
        }
        #expect(titles == ["Heading 1", "Table"])
        c.insertItem(MenuItem(key: "h1", label: "Heading 1", hint: "", icon: "", group: ""))
        let b = blocks(c)
        #expect(b.map(\.type) == ["paragraph", "heading"])
        #expect(b.map(\.text) == ["Plan", ""])
        #expect(b.first?.id == "a")
    }

    /// Insert with a picture chosen puts the kind picked after it, as the
    /// caret by it does: the picture stays.
    @Test func insertByAPictureKeepsIt() {
        let image = DocNode(
            id: "i", type: "image", data: ["url": .string("https://example.com/a.png")],
            children: nil)
        let c = editor([image, block("p", "paragraph", "After")])
        offer(c, [("h1", "Heading 1")])
        c.textView.selectedRange = NSRange(location: 0, length: 1)
        c.insertItem(MenuItem(key: "h1", label: "Heading 1", hint: "", icon: "", group: ""))
        let b = blocks(c)
        #expect(b.map(\.type) == ["image", "heading", "paragraph"])
        #expect(b.first?.id == "i")
        #expect(b.map(\.text) == ["", "", "After"])
    }

    /// A change from outside that takes away the block the caret is in
    /// leaves the caret at the end of the block before it.
    @Test func theCaretOfABlockTakenAwayGoesToTheOneBefore() {
        let c = editor([
            block("a", "paragraph", "One"), block("b", "paragraph", "Two"),
            block("c", "paragraph", "Three"),
        ])
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        c.update(
            content([block("a", "paragraph", "One more"), block("c", "paragraph", "Three")]),
            tinted: [])
        #expect(c.textView.text == "One more\nThree")
        #expect(c.textView.selectedRange == NSRange(location: 8, length: 0))
        // The first block taken away, the caret goes to the start of the
        // one after it.
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        c.update(content([block("c", "paragraph", "Three")]), tinted: [])
        #expect(c.textView.selectedRange == NSRange(location: 0, length: 0))
    }

    /// VoiceOver copies a code block and offers its language from the
    /// actions at the caret, as its bar does, which VoiceOver can't reach
    /// in the text view, one element.
    @Test func voiceOverCopiesCode() throws {
        let c = editor([block("k", "code", "let a = 1", data: ["language": .string("swift")])])
        c.textView.selectedRange = NSRange(location: 2, length: 0)
        let actions = c.textView.accessibilityCustomActions ?? []
        let copy = try #require(actions.first { $0.name == L.editorCopyCode })
        #expect(
            actions.contains { $0.name == L.editorCodeLanguage(name: CodeLanguage.name("swift")) })
        _ = copy.actionHandler?(copy)
        #expect(UIPasteboard.general.string == "let a = 1")
        // A page that can't be changed copies, and keeps its language.
        c.textView.isEditable = false
        #expect(
            !(c.textView.accessibilityCustomActions ?? []).contains {
                $0.name.hasPrefix(L.editorCodeLanguage(name: ""))
            })
    }

    /// A to-do's due date is drawn again as the day changes: its chip says
    /// Today, Tomorrow or overdue as of when it was drawn.
    @Test func dueDatesAreDrawnAgainAsTheDayChanges() throws {
        let c = editor([
            block(
                "t", "todo_list", "Call",
                data: ["checked": .bool(false), "due": .string("2026-01-02")])
        ])
        c.textView.frame = CGRect(x: 0, y: 0, width: 600, height: 400)
        c.textView.layoutIfNeeded()
        let tlm = try #require(c.textView.textLayoutManager)
        tlm.ensureLayout(for: tlm.documentRange)
        let before = try #require(tlm.textLayoutFragment(for: tlm.documentRange.location))
        NotificationCenter.default.post(
            name: UIApplication.significantTimeChangeNotification, object: nil)
        tlm.ensureLayout(for: tlm.documentRange)
        let after = try #require(tlm.textLayoutFragment(for: tlm.documentRange.location))
        #expect(after !== before)
    }

    /// Typing that never pauses is still sent, every two seconds or so: a
    /// crash in it loses no more.
    @Test func typingOnIsSentEveryTwoSeconds() async throws {
        let c = editor([block("a", "paragraph", "")])
        var sent = 0
        c.onEdit = { _ in sent += 1 }
        let start = ContinuousClock.now
        while ContinuousClock.now - start < .milliseconds(2600) {
            c.type("a")
            try await Task.sleep(for: .milliseconds(100))
        }
        #expect(sent >= 1)
    }

    /// Text being composed, as Japanese is typed, is not sent, and holds a
    /// change from outside, which would commit it; once composed, it goes
    /// to the core at once, for the core to merge with the change.
    @Test func aCompositionHoldsChangesAndSendsOnceComposed() async throws {
        let c = editor([block("a", "paragraph", ""), block("b", "paragraph", "Other")])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.textView.setMarkedText("か", selectedRange: NSRange(location: 1, length: 0))
        #expect(c.textView.markedTextRange != nil)
        try await Task.sleep(for: .milliseconds(400))
        #expect(sent == nil)
        c.update(
            content([block("a", "paragraph", ""), block("b", "paragraph", "Other, changed")]),
            tinted: [])
        #expect(c.textView.text == "か\nOther")
        c.textView.unmarkText()
        for _ in 0..<20 where sent == nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(sent?.blocks.first?.text == "か")
    }

    /// editing is a model of a new page of two blocks, over host, and an
    /// editor showing it through the coordinator the page's view has, whose
    /// edits go to the model.
    private func editing(_ host: PageTests.Host) async throws -> (
        PageModel, EditorController, PageEditorView<EmptyView, EmptyView>.Coordinator
    ) {
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let model = PageModel(id: page.id, app: host)
        await model.open()
        var two = try #require(model.content)
        two.document.children = [block("a", "paragraph", ""), block("b", "paragraph", "Other")]
        model.edit(two)
        await model.flush()
        let c = editor([])
        c.onEdit = { [weak model] in model?.edit($0) }
        let coordinator = PageEditorView<EmptyView, EmptyView>.Coordinator(controller: c)
        coordinator.show(model)
        return (model, c, coordinator)
    }

    /// A change from outside moves the rev the editor's edits go over, once
    /// the editor shows it.
    @Test func aChangeFromOutsideMovesTheRevTheEditorShows() async throws {
        let host = try PageTests.Host()
        let (model, c, coordinator) = try await editing(host)
        let rev = model.rev + 1
        model.handle(
            .pageUpdated(
                pageID: model.id,
                content: content([
                    block("a", "paragraph", "Synced"), block("b", "paragraph", "Other"),
                ]),
                rev: rev, reason: "external", newStep: false))
        coordinator.show(model)
        #expect(c.textView.text == "Synced\nOther")
        #expect(model.shown == rev)
        await model.close()
    }

    /// A change from outside that comes while text is composed stays out of
    /// the editor, and so out of the rev its edits go over: what is composed
    /// goes to the core over the rev the editor shows, for the core to merge
    /// with the change. Over the change's own rev, it would take the change
    /// away, and a page's only editor would save the page without it.
    @Test func aCompositionMergesWithTheChangeItHeld() async throws {
        let host = try PageTests.Host()
        let client = host.core.client
        let (model, c, coordinator) = try await editing(host)
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        let rev = model.shown
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.textView.setMarkedText("か", selectedRange: NSRange(location: 1, length: 0))
        c.edited(NSRange(location: 0, length: 1))

        // Another window changes the page and goes, as sync or an agent
        // would change it: this editor is the page's only one again.
        let other = try await client.openPage(model.id)
        var changed = other.content
        changed.document.children = [
            block("a", "paragraph", ""), block("b", "paragraph", "Changed"),
        ]
        let left = try await client.editPage(
            model.id, editor: other.editor, content: changed, rev: other.rev)
        await client.closePage(model.id, editor: other.editor)
        model.handle(
            .pageUpdated(
                pageID: model.id, content: changed, rev: left, reason: "external", newStep: false))
        coordinator.show(model)
        #expect(model.rev == left)
        #expect(model.shown == rev)
        #expect(c.textView.text == "か\nOther")

        c.textView.unmarkText()
        await model.flush()
        let merged = try await client.openPage(model.id)
        #expect(merged.content.blocks.map(\.text) == ["か", "Changed"])
        #expect(host.toasts.isEmpty)
        await client.closePage(model.id, editor: merged.editor)
        await model.close()
    }

    /// What a composition keeps from going counts as not sent: the page
    /// closing or the app leaving the screen sends it, with the typing it
    /// held up, which would otherwise go with the editor.
    @Test func aCompositionLeavesItsEditPending() async throws {
        let c = editor([block("a", "paragraph", "One")])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.textView.selectedRange = NSRange(location: 3, length: 0)
        c.textView.setMarkedText("か", selectedRange: NSRange(location: 1, length: 0))
        c.edited(NSRange(location: 3, length: 1))
        try await Task.sleep(for: .milliseconds(400))
        #expect(sent == nil)
        #expect(c.hasPendingEdit)
        c.send()
        #expect(sent?.blocks.first?.text == "Oneか")
        #expect(!c.hasPendingEdit)
    }

    /// A composition that ends with no change of the text is sent all the
    /// same, though no edit follows it to send it.
    @Test func aCompositionEndingUnchangedIsSent() async throws {
        let c = editor([block("a", "paragraph", "")])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.textView.setMarkedText("か", selectedRange: NSRange(location: 1, length: 0))
        c.edited(NSRange(location: 0, length: 1))
        #expect(sent == nil)
        c.textView.unmarkText()
        for _ in 0..<50 where sent == nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(sent?.blocks.first?.text == "か")
        #expect(!c.hasPendingEdit)
    }

    /// png is a picture of width by height pixels, in shades of gray.
    private func png(_ width: Int, _ height: Int) throws -> Data {
        let context = try #require(
            CGContext(
                data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                space: CGColorSpaceCreateDeviceGray(), bitmapInfo: CGImageAlphaInfo.none.rawValue))
        let image = try #require(context.makeImage())
        let out = NSMutableData()
        let file = try #require(
            CGImageDestinationCreateWithData(out, UTType.png.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(file, image, nil)
        #expect(CGImageDestinationFinalize(file))
        return out as Data
    }

    /// A picture of more pixels than one may have is not decoded, as a
    /// small file can say it is far larger; one of as many is, and what is
    /// no bitmap passes on to what reads it. The file here is small for
    /// its pixels, as such a file is.
    @Test func aPictureOfTooManyPixelsIsNotDecoded() throws {
        let limit = Int(MobileMaxPixels)
        #expect(limit == 50_000_000)
        let huge = try png(5000, limit / 5000 + 1)
        #expect(huge.count < 1 << 20)
        #expect(!Pictures.fit(huge))
        #expect(Pictures.bitmap(huge) == nil)
        #expect(Pictures.fit(try png(5000, limit / 5000)))
        #expect(Pictures.bitmap(try png(20, 10)) != nil)
        #expect(Pictures.fit(Data("<svg xmlns=\"http://www.w3.org/2000/svg\"/>".utf8)))
    }

    /// A picture a block links to on this device is read only from the
    /// app's data folder, wherever the folder is now: the link can come
    /// from another device or person.
    @Test func onlyTheDataFoldersFilesAreRead() throws {
        let files = FileManager.default
        let dir = files.temporaryDirectory.appending(
            path: "LocalFiles-" + UUID().uuidString, directoryHint: .isDirectory)
        defer { try? files.removeItem(at: dir) }
        let folder = dir.appending(path: "Leaflet", directoryHint: .isDirectory)
        try files.createDirectory(at: folder, withIntermediateDirectories: true)
        let inside = folder.appending(path: "a.png")
        let outside = dir.appending(path: "b.png")
        try Data("picture".utf8).write(to: inside)
        try Data("picture".utf8).write(to: outside)
        #expect(LocalFiles.resolve(inside.path, in: folder) == inside.path)
        #expect(LocalFiles.resolve("/gone/Leaflet/a.png", in: folder) == inside.path)
        #expect(LocalFiles.resolve(outside.path, in: folder) == nil)
        #expect(LocalFiles.resolve("/gone/Leaflet/../b.png", in: folder) == nil)
    }

    /// A table copied pastes back with IDs of its own, its cells' too, and
    /// one dropped beside itself takes them as it is lifted into a block.
    @Test func blocksCopiedTakeIDsOfTheirOwn() {
        let cell = { (id: String, r: Int, col: Int, text: String) in
            DocNode(
                id: id, type: "table_cell",
                data: ["rowPosition": .int(Int64(r)), "colPosition": .int(Int64(col))],
                children: [self.block(id + "p", "paragraph", text)])
        }
        let table = DocNode(
            id: "t", type: "table", data: [:],
            children: [cell("c1", 0, 0, "A"), cell("c2", 1, 0, "1")])
        let c = editor([table, block("p", "paragraph", "After")])
        // As UIKit drops a card: in the text, with the block it went in.
        let (para, info) = c.paragraph(at: 2)
        let card = NSMutableAttributedString(attachment: BlockAttachment(node: table))
        card.addAttribute(.leafletBlock, value: info, range: NSRange(location: 0, length: 1))
        c.textView.textStorage.insert(card, at: NSMaxRange(para))
        c.textViewDidChange(c.textView)
        let b = blocks(c)
        #expect(b.filter { $0.type == "table" }.count == 2)
        #expect(Set(b.map(\.id)).count == b.count)
        #expect(b.first?.id == "t")
    }

    /// A page that can't be changed takes no command: Tab, a format, a
    /// kind, a move or a block put in would never be saved.
    @Test func aPageThatCantBeChangedTakesNoCommand() {
        let c = editor([block("a", "bulleted_list", "One"), block("b", "bulleted_list", "Two")])
        let before = blocks(c)
        c.textView.isEditable = false
        c.textView.selectedRange = NSRange(location: 4, length: 3)
        c.indent(by: 1)
        c.setType("heading", ["level": .int(1)])
        c.toggleInline(.leafletBold)
        c.moveBlock(by: -1)
        c.insertBlock(DocNode(id: "d", type: "divider", data: nil, children: nil))
        c.softBreak()
        #expect(blocks(c) == before)
    }

    /// The blocks an agent lit follow its tint as it changes, apart from
    /// the content, and none are lit once it ends.
    @Test func theBlocksLitFollowTheTint() {
        let c = editor([block("a", "paragraph", "One"), block("b", "paragraph", "Two")])
        let lit = { (at: Int) in
            c.textView.textStorage.attribute(.leafletTinted, at: at, effectiveRange: nil) != nil
        }
        c.tint(["b"])
        #expect(!lit(0))
        #expect(lit(4))
        c.tint([])
        #expect(!lit(4))
    }

    /// In code, Tab indents the line, Enter keeps its indent and leaves the
    /// block on a second blank line, as on the desktop; Shift-Return breaks
    /// a line within a block of text.
    @Test func codeAndLineBreaksFollowTheDesktop() {
        let c = editor([block("k", "code", "if x {")])
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        c.type("\n")
        c.press("\t")
        c.type("y\n")
        #expect(blocks(c).first?.text == "if x {\n  y\n  ")
        c.type("\n")
        c.type("\n")
        #expect(blocks(c).map(\.type) == ["code", "paragraph"])

        let p = editor([block("a", "paragraph", "One")])
        p.textView.selectedRange = NSRange(location: 3, length: 0)
        p.softBreak()
        p.type("Two")
        #expect(blocks(p).map(\.text) == ["One\nTwo"])
    }

    /// Emoji, two code units or more, go through the rules as one
    /// character: a Markdown shortcut, an inline format, and "/" after one
    /// and a space.
    @Test func emojiGoThroughTheRules() {
        let c = editor([block("a", "paragraph", "")])
        c.type("## 👍🏽 Done")
        #expect(blocks(c).first?.type == "heading")
        #expect(blocks(c).first?.text == "👍🏽 Done")

        let d = editor([block("a", "paragraph", "")])
        d.type("a **👩‍💻** b")
        let runs = blocks(d).first?.data?["delta"]?.array ?? []
        #expect(
            runs.first { $0["insert"]?.string == "👩‍💻" }?["attributes"]?["bold"]?.bool == true)

        let e = editor([block("a", "paragraph", "")])
        offer(e, [("h1", "Heading 1")])
        e.type("😀 /")
        #expect(e.menu != nil)
        e.type("\n")
        #expect(blocks(e).map(\.type) == ["paragraph", "heading"])
        #expect(blocks(e).map(\.text) == ["😀 ", ""])
    }

    /// The item chosen in the menu stays chosen through a change from
    /// outside, which filters the menu again.
    @Test func theMenusChoiceSurvivesAChangeFromOutside() {
        let c = editor([block("a", "paragraph", "Intro"), block("b", "paragraph", "")])
        offer(c, [("h1", "Heading 1"), ("h2", "Heading 2")])
        c.textView.selectedRange = NSRange(location: 6, length: 0)
        c.type("/")
        c.moveInMenu(1)
        #expect(c.menu?.current?.key == "h2")
        c.update(
            content([block("a", "paragraph", "A longer intro"), block("b", "paragraph", "/")]),
            tinted: [])
        #expect(c.menu?.current?.key == "h2")
    }

    /// Text pasted for real, as the edit menu's Paste does, goes through
    /// the editor's rules: lines pasted are blocks.
    @Test func aRealPasteGoesThroughTheRules() async throws {
        let c = editor([block("a", "paragraph", "")])
        let (_, done) = try show(c)
        defer { done() }
        try await settle(c)
        UIPasteboard.general.string = "- one\n- two"
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.textView.paste(nil)
        // A paste comes when the system has read the pasteboard, in its
        // own time.
        for _ in 0..<150 where blocks(c).count < 2 { try await Task.sleep(for: .milliseconds(20)) }
        #expect(blocks(c).map(\.type) == ["bulleted_list", "bulleted_list"])
        #expect(blocks(c).map(\.text) == ["one", "two"])
    }

    /// Tables, cards and charts are taller at the largest text sizes, as
    /// their text is.
    @Test func blocksGrowWithTheTextSize() {
        // A table of its own, which no other test has measured.
        let table = DocNode(
            id: "t-" + UUID().uuidString, type: "table", data: [:],
            children: [
                DocNode(
                    id: "c", type: "table_cell",
                    data: ["rowPosition": .int(0), "colPosition": .int(0)],
                    children: nil)
            ])
        let height = { (size: UIContentSizeCategory) in
            var h: [CGFloat] = []
            UITraitCollection(preferredContentSizeCategory: size).performAsCurrent {
                h = ["table", "chart", "link_preview"].map {
                    BlockCard.height(
                        $0 == "table"
                            ? table : DocNode(id: "x", type: $0, data: [:], children: nil),
                        width: 600)
                }
            }
            return h
        }
        let large = height(.large)
        let largest = height(.accessibilityExtraExtraExtraLarge)
        for (l, xl) in zip(large, largest) { #expect(xl > l) }
    }

    /// A table's columns are as wide as their text and fill the page; too
    /// wide for it, the widest wrap down to one width, unless that leaves
    /// them too narrow, as many columns would be, and the table scrolls
    /// instead. No column is wider than the page.
    @Test func tableColumnsFitThePageOrScroll() {
        #expect(TableLayout.fit([100, 300], in: 800, wrapTo: 160) == [200, 600])
        #expect(TableLayout.fit([300, 1000], in: 720, wrapTo: 160) == [300, 420])
        let many = Array(repeating: CGFloat(150), count: 8)
        #expect(TableLayout.fit(many, in: 720, wrapTo: 160) == many)
        #expect(TableLayout.fit([2000], in: 720, wrapTo: 160) == [720])
    }

    /// Heights blocks measured are kept by width: the same page in two
    /// windows of different widths keeps each; an embed grows to its
    /// content, and back.
    @Test func measuredHeightsAreKeptByWidth() {
        let id = "h-" + UUID().uuidString
        #expect(BlockHeights.set(id, CGSize(width: 300, height: 400)))
        #expect(BlockHeights.set(id, CGSize(width: 600, height: 200)))
        #expect(!BlockHeights.set(id, CGSize(width: 600.5, height: 200.4)))
        #expect(BlockHeights.of(id, width: 300) == 400)
        #expect(BlockHeights.of(id, width: 600) == 200)
        #expect(BlockHeights.of(id, width: 450) == nil)
        let embed = DocNode(id: id, type: "embed", data: ["height": .int(320)], children: nil)
        #expect(BlockCard.height(embed, width: 300) == 400)
        _ = BlockHeights.set(id, CGSize(width: 300, height: 100))
        #expect(BlockCard.height(embed, width: 300) == 320)
    }

    /// placed puts the editor in a window of size, with no keyboard, and
    /// returns what takes it away again.
    private func placed(_ c: EditorController, _ size: CGSize) throws -> () -> Void {
        let scene = try #require(
            UIApplication.shared.connectedScenes.first { $0 is UIWindowScene } as? UIWindowScene)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(origin: .zero, size: size)
        window.addSubview(c.textView)
        c.textView.frame = window.bounds
        window.isHidden = false
        c.textView.layoutIfNeeded()
        return {
            c.textView.removeFromSuperview()
            window.isHidden = true
            window.windowScene = nil
        }
    }

    /// long is an editor of many long paragraphs, p0 on.
    private func long() -> EditorController {
        editor(
            (0..<40).map {
                block("p\($0)", "paragraph", String(repeating: "word \($0) ", count: 80))
            })
    }

    /// scroll brings block id's line index to the top of the view, laid out.
    private func scroll(_ c: EditorController, to id: String, line index: Int = 0) throws {
        let view = c.textView
        let tlm = try #require(view.textLayoutManager)
        let at = try #require(c.text.blocks().first { $0.info.id == id }).range.location
        let start = try #require(tlm.location(tlm.documentRange.location, offsetBy: at))
        let before = try #require(NSTextRange(location: tlm.documentRange.location, end: start))
        tlm.ensureLayout(for: before)
        let fragment = try #require(tlm.textLayoutFragment(for: start))
        tlm.ensureLayout(for: fragment.rangeInElement)
        let line = fragment.textLineFragments[index].typographicBounds
        view.contentOffset.y =
            fragment.layoutFragmentFrame.minY + line.minY + view.textContainerInset.top
            - view.adjustedContentInset.top
        view.layoutIfNeeded()
    }

    /// top is the block at the top of the view, and the words of the line
    /// there, at the start of it.
    private func top(_ c: EditorController) throws -> (block: String, line: String) {
        let view = c.textView
        let tlm = try #require(view.textLayoutManager)
        let y =
            view.contentOffset.y + view.adjustedContentInset.top - view.textContainerInset.top + 1
        let fragment = try #require(tlm.textLayoutFragment(for: CGPoint(x: 0, y: y)))
        let top = y - fragment.layoutFragmentFrame.minY
        let line = try #require(
            fragment.textLineFragments.first { $0.typographicBounds.maxY > top })
        let start = tlm.offset(
            from: tlm.documentRange.location, to: fragment.rangeInElement.location)
        let offset = start + line.characterRange.location
        return (
            c.text.paragraph(at: offset).info.id,
            (view.text as NSString).substring(with: NSRange(location: offset, length: 12))
        )
    }

    /// A page keeps the text at the top of the view as a new width wraps
    /// it, as a phone folding or a window resizing gives it: down the
    /// page, and part way through a paragraph, where the line that held
    /// the text is no longer the same line.
    @Test func aNewWidthKeepsTheTextAtTheTopOfTheView() async throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer { done() }
        try scroll(c, to: "p20")
        #expect(try top(c).block == "p20")
        c.textView.frame.size.width = 466
        c.textView.layoutIfNeeded()
        #expect(try top(c).block == "p20")
        try await Task.sleep(for: .milliseconds(300))
        #expect(try top(c).block == "p20")
        c.textView.frame.size.width = 669
        c.textView.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        #expect(try top(c).block == "p20")

        try scroll(c, to: "p30", line: 5)
        let text = try top(c).line
        c.textView.frame.size.width = 466
        c.textView.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        let (block, line) = try top(c)
        #expect(block == "p30")
        // A line's own text, or the line now holding it.
        let wrapped = (c.textView.text as NSString).range(of: line)
        let was = (c.textView.text as NSString).range(of: text)
        #expect(abs(wrapped.location - was.location) < 120)
    }

    /// The text keeps to the middle of the view, across a fold with room
    /// for text on either side, as the page has both halves. With room on
    /// one side only it keeps to that side, clear of the fold, unless that
    /// side is too narrow too, and keeps its width on a wide display.
    @Test func theTextKeepsToOneSideOfAFoldOnlyWithRoomOnOneSide() {
        let sides = EditorTextView.sides
        #expect(sides(951, nil) == (115.5, 115.5))
        #expect(sides(400, nil) == (20, 20))
        // Room on either side: the page has both halves.
        #expect(sides(951, 465...486) == (115.5, 115.5))
        #expect(sides(867, 455.5...495.5) == (73.5, 73.5))
        // Room on one side, either one, and the text in the middle of it.
        #expect(sides(527, 455...495) == (20, 527 - 455 + 20))
        #expect(sides(951, 200...240) == (240 + 20, 20))
        // Too narrow on either side, the text stays and crosses the fold.
        #expect(sides(600, 290...310) == (20, 20))
        // Past the text's edges, it isn't in the way.
        #expect(sides(951, 0...100) == (115.5, 115.5))
        #expect(sides(951, 840...900) == (115.5, 115.5))
        // No wider than a page reads.
        #expect(sides(1200, 260...300) == (300 + 90, 90))
    }

    /// With a fold that leaves room on one side, the text, its title and
    /// what follows it keep to that side, and back with none.
    @Test func aFoldMovesTheTextAndItsHeaderToOneSide() async throws {
        let c = long()
        let header = UIView()
        let footer = UIView()
        c.textView.header = header
        c.textView.footer = footer
        c.textView.measureHeader = { _ in 40 }
        c.textView.measureFooter = { _ in 40 }
        let done = try placed(c, CGSize(width: 527, height: 669))
        defer { done() }
        let view = c.textView
        view.fold = 455...495
        view.layoutIfNeeded()
        #expect(view.textContainerInset.left == 20)
        #expect(view.textContainerInset.right == 92)
        #expect(view.textContainer.size.width == 415)
        #expect(header.frame.minX == 20)
        #expect(header.frame.width == 415)
        #expect(footer.frame.minX == 20)
        view.fold = nil
        view.layoutIfNeeded()
        #expect(view.textContainerInset.right == 20)
        #expect(header.frame.width == 487)
    }

    /// A block keeps to the text as a fold narrows it with the view's own
    /// width the same, as beside the panel: laid out at the old width, a
    /// table would go on past the text and over what is above it.
    @Test func aBlockFollowsTheTextAsAFoldNarrowsIt() async throws {
        let cell = { (row: Int64, col: Int64) in
            DocNode(
                id: "c\(row)\(col)", type: "table_cell",
                data: ["rowPosition": .int(row), "colPosition": .int(col)], children: nil)
        }
        let table = DocNode(
            id: "t-" + UUID().uuidString, type: "table",
            data: ["rowsLen": .int(2), "colsLen": .int(2)],
            children: [cell(0, 0), cell(0, 1), cell(1, 0), cell(1, 1)])
        let c = editor([block("a", "paragraph", "Before"), table, block("b", "paragraph", "After")])
        let done = try placed(c, CGSize(width: 527, height: 669))
        defer { done() }
        let view = c.textView
        view.layoutIfNeeded()
        let card = try #require(view.subviews.first { view.blocks.owns($0) })
        #expect(card.frame.maxX > 500)
        view.fold = 455...495
        view.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        #expect(card.frame.maxX <= 455)
        view.fold = nil
        view.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        #expect(card.frame.maxX > 500)
    }

    /// At a new width the header is measured again once it has laid out
    /// there: a title measures as it last laid out until then.
    @Test func theHeaderIsMeasuredAgainAfterANewWidth() async throws {
        let c = long()
        let header = UIView()
        c.textView.header = header
        c.textView.measureHeader = { _ in 40 }
        let done = try placed(c, CGSize(width: 951, height: 669))
        defer { done() }
        let view = c.textView
        var measures = 0
        view.measureHeader = { _ in
            measures += 1
            return measures == 1 ? 90 : 40
        }
        view.frame.size.width = 600
        view.layoutIfNeeded()
        #expect(header.frame.height == 90)
        try await Task.sleep(for: .milliseconds(50))
        view.layoutIfNeeded()
        #expect(header.frame.height == 40)
    }

    /// A fold the system lets go of just after the hinge stops goes too,
    /// with no more word from the hinge.
    @Test func aFoldLetGoAfterTheHingeStopsGoes() async throws {
        let view = UIView()
        var reserved: ClosedRange<CGFloat>? = 465...486
        var said: [ClosedRange<CGFloat>?] = []
        let watch = FoldWatch(view: view, wait: .milliseconds(20)) { said.append($0) }
        watch.reserved = { _ in reserved }
        watch.hingeMoved()
        try await Task.sleep(for: .milliseconds(100))
        #expect(watch.fold == 465...486)
        watch.hingeMoved()
        reserved = nil
        try await Task.sleep(for: .milliseconds(100))
        #expect(watch.fold == nil)
        #expect(said == [465...486, nil])
        withExtendedLifetime(view) {}
    }

    /// The menu at the caret stays over the text, clear of a fold.
    @Test func theMenuAtTheCaretStaysClearOfAFold() throws {
        let c = long()
        let done = try placed(c, CGSize(width: 527, height: 669))
        defer { done() }
        let view = c.textView
        view.fold = 455...495
        view.layoutIfNeeded()
        let menu = CaretMenu(
            anchor: 0, trigger: "/", palette: Palette.palettes["Indigo"]!["light"]!)
        menu.place(in: view, caret: CGRect(x: 430, y: 100, width: 2, height: 20))
        #expect(menu.view.frame.maxX <= 455)
        view.fold = nil
        view.layoutIfNeeded()
        menu.place(in: view, caret: CGRect(x: 430, y: 100, width: 2, height: 20))
        #expect(menu.view.frame.maxX > 455)
    }

    /// The keyboard a pose brings up leaves the page where it is: a caret
    /// out of view comes to the text in view, as the text view would
    /// otherwise scroll back to it.
    @Test func theKeyboardAPoseBringsLeavesThePageWhereItIs() throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer {
            _ = c.textView.resignFirstResponder()
            done()
        }
        c.textView.window?.makeKey()
        try scroll(c, to: "p20")
        c.textView.selectedRange = NSRange(location: 0, length: 0)
        c.textView.takeKeyboard()
        #expect(c.text.paragraph(at: c.textView.selectedRange.location).info.id == "p20")
        #expect(try top(c).block == "p20")
    }

    /// Text that had the keyboard as the display closed has it back on the
    /// display that takes over, which UIKit doesn't do.
    @Test func textHasTheKeyboardBackAfterTheDisplayCloses() async throws {
        let c = long()
        let (_, done) = try show(c)
        defer { done() }
        #expect(c.textView.isFirstResponder)
        c.textView.displayClosed()
        _ = c.textView.resignFirstResponder()
        c.textView.frame.size.width = 466
        c.textView.layoutIfNeeded()
        for _ in 0..<40 where !c.textView.isFirstResponder {
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(c.textView.isFirstResponder)
    }

    /// Text that didn't have the keyboard isn't given it by a new width.
    @Test func aNewWidthLeavesTheKeyboardAwayIfItWas() async throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer { done() }
        c.textView.displayClosed()
        c.textView.frame.size.width = 466
        c.textView.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        #expect(!c.textView.isFirstResponder)
    }

    /// A page that can't be changed takes no keyboard.
    @Test func aPageThatCantBeChangedTakesNoKeyboard() throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer { done() }
        c.textView.isEditable = false
        c.textView.takeKeyboard()
        #expect(!c.textView.isFirstResponder)
    }

    /// A column too narrow to lay out in, as a split view gives for a
    /// frame as it opens, leaves the page as it was.
    @Test func aColumnNarrowForAFrameLeavesThePageAsItWas() throws {
        let c = long()
        let header = UIView()
        c.textView.header = header
        c.textView.measureHeader = { width in width < 100 ? 600 : 80 }
        let done = try placed(c, CGSize(width: 466, height: 800))
        defer { done() }
        #expect(header.frame.height == 80)
        c.textView.frame.size.width = 82
        c.textView.layoutIfNeeded()
        #expect(header.frame.height == 80)
        #expect(c.textView.textContainerInset.top < 120)
        c.textView.frame.size.width = 567
        c.textView.layoutIfNeeded()
        #expect(header.frame.width == 527)
        #expect(header.frame.height == 80)
    }

    /// A page at its top stays there at a new width.
    @Test func aNewWidthLeavesThePageAtItsTop() async throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer { done() }
        let y = c.textView.contentOffset.y
        c.textView.frame.size.width = 466
        c.textView.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(300))
        #expect(c.textView.contentOffset.y == y)
    }

    /// A scroll held for a change from before a new width doesn't put the
    /// page back on what is by then other text.
    @Test func aScrollHeldOverANewWidthLeavesThePageWhereItKeptItself() async throws {
        let c = long()
        let done = try placed(c, CGSize(width: 669, height: 800))
        defer { done() }
        try scroll(c, to: "p20")
        c.edits.keepingScroll { c.textView.frame.size.width = 466 }
        try await Task.sleep(for: .milliseconds(300))
        #expect(try top(c).block == "p20")
    }

    /// A keyboard that stays keeps the text clear as the view resizes under
    /// it, and says nothing as it does: and one gone leaves no room.
    @Test func aResizeKeepsTheTextClearOfAKeyboardThatStays() throws {
        let c = editor([block("a", "paragraph", "One")])
        let done = try placed(c, CGSize(width: 600, height: 800))
        defer { done() }
        let view = c.textView
        func keyboard(_ name: Notification.Name, top: CGFloat) {
            NotificationCenter.default.post(
                name: name, object: nil,
                userInfo: [
                    UIResponder.keyboardFrameEndUserInfoKey: CGRect(
                        x: 0, y: top, width: 600, height: 300)
                ])
        }
        keyboard(UIResponder.keyboardWillChangeFrameNotification, top: 500)
        #expect(view.contentInset.bottom == 300)
        view.frame.size.height = 600
        view.layoutIfNeeded()
        #expect(view.contentInset.bottom == 100)
        // A page that doesn't reach the keyboard leaves no room for it.
        view.frame.size.height = 400
        view.layoutIfNeeded()
        #expect(view.contentInset.bottom == 0)

        keyboard(UIResponder.keyboardWillHideNotification, top: 800)
        view.frame.size.height = 800
        view.layoutIfNeeded()
        #expect(view.contentInset.bottom == 0)
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
    /// the editor's rules, which a text view leaves out of text put in by
    /// code, and in UIKit's order, the selection's change before the text's.
    func type(_ text: String) {
        for ch in text { paste(String(ch)) }
    }

    /// paste puts text in at once, as a paste or dictation does.
    func paste(_ text: String) {
        // A text view on screen says what changed itself: told again, the
        // editor would take the text twice. Before iOS 27 it asked the
        // rules too; since, it asks them only of the keyboard's text.
        let onScreen = textView.window != nil
        if #unavailable(iOS 27), onScreen { return textView.insertText(text) }
        // Each key is a step of its own to undo, as the keyboard's are.
        let undo = onScreen ? textView.undoManager : nil
        undo?.beginUndoGrouping()
        defer { undo?.endUndoGrouping() }
        guard textView(textView, shouldChangeTextIn: textView.selectedRange, replacementText: text)
        else { return }
        textView.insertText(text)
        if !onScreen {
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
