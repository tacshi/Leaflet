import Foundation
import Testing
import UIKit

@testable import Leaflet

/// LongPageTests check that the editor does on each change only what the
/// change reaches, and that it comes out as doing it all again would: the
/// numbers of a list, the index of the paragraphs, the folds, a change
/// from outside and the page sent.
@MainActor
@Suite(.serialized)
struct LongPageTests {
    private func editor(_ blocks: [DocNode]) -> EditorController {
        let c = EditorController(
            style: EditorStyle(textSize: 17, palette: Palette.palettes["Indigo"]!["light"]!))
        c.load(page(blocks), tinted: [])
        return c
    }

    private func page(_ blocks: [DocNode]) -> DocContent {
        DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil)
    }

    private func block(
        _ id: String, _ type: String, _ text: String, data: [String: JSONValue] = [:],
        children: [DocNode]? = nil
    ) -> DocNode {
        var d = data
        d["delta"] = .array(text.isEmpty ? [] : [.object(["insert": .string(text)])])
        return DocNode(id: id, type: type, data: d, children: children)
    }

    /// numbers are the numbers the list's items show, and those they would
    /// be given counted again from the page's start.
    private func numbers(_ c: EditorController) -> (shown: [Int?], counted: [Int?]) {
        let s = c.text.storage
        var shown: [Int?] = []
        var counted: [Int?] = []
        for p in c.text.blocks() where p.range.location < s.length {
            shown.append(
                s.attribute(.leafletOrdinal, at: p.range.location, effectiveRange: nil) as? Int)
            counted.append(
                p.info.type == "numbered_list"
                    ? EditorStyle.ordinal(s, at: p.range.location, p.info) : nil)
        }
        return (shown, counted)
    }

    /// A key typed in a long list leaves the numbers as they are, and a
    /// new item numbers those after it again, as do an item turned into
    /// another kind and one nested.
    @Test func aLongListKeepsItsNumbers() throws {
        var items = (0..<300).map { block("n\($0)", "numbered_list", "Item \($0)") }
        items[20].children = [block("in", "numbered_list", "Nested")]
        let c = editor(items)
        var n = numbers(c)
        #expect(n.shown == n.counted)
        let tenth = try #require(c.text.blocks().first { $0.info.id == "n9" })
        c.textView.selectedRange = NSRange(location: NSMaxRange(tenth.range), length: 0)
        c.type("x")
        n = numbers(c)
        #expect(n.shown == n.counted)
        #expect(n.shown[10] == 11)

        // A new item after the tenth: those after it go up by one.
        c.type("\nNew")
        n = numbers(c)
        #expect(n.shown == n.counted)
        #expect(n.shown.last == 301)

        // The fifth no longer numbered: those after it count from one.
        let fifth = try #require(c.text.blocks().first { $0.info.id == "n4" })
        c.setBlock(fifth.info.with(type: "bulleted_list"), at: fifth.range.location)
        n = numbers(c)
        #expect(n.shown == n.counted)
        #expect(n.shown[5] == 1)

        // An item nested in the one before it.
        let sixth = try #require(c.text.blocks().first { $0.info.id == "n5" })
        c.setBlock(sixth.info.with(depth: 1), at: sixth.range.location)
        n = numbers(c)
        #expect(n.shown == n.counted)
    }

    /// The paragraphs' index, found again only where the text changed,
    /// is the one finding them all again gives, after every kind of edit.
    @Test func theIndexFollowsEachEdit() {
        let kinds = ["paragraph", "bulleted_list", "numbered_list", "heading", "toggle_list"]
        let c = editor(
            (0..<60).map { block("b\($0)", kinds[$0 % kinds.count], "Block \($0)") })
        var seed: UInt64 = 7
        func roll(_ n: Int) -> Int {
            seed = seed &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
            return Int((seed >> 33) % UInt64(max(n, 1)))
        }
        for round in 0..<400 {
            let s = c.text.storage
            let at = roll(s.length + 1)
            c.textView.selectedRange = NSRange(location: at, length: 0)
            switch roll(4) {
            case 0: c.paste(["a", "bc\nd", "\n", "e\nf\ng"][roll(4)])
            case 1:
                c.textView.selectedRange = NSRange(
                    location: at, length: min(roll(6), s.length - at))
                c.backspace()
            case 2:
                let (_, info) = c.paragraph(at: at)
                c.setBlock(info.with(type: kinds[roll(kinds.count)]), at: at)
            default:
                let (_, info) = c.paragraph(at: at)
                c.setBlock(info.setting("collapsed", .bool(roll(2) == 0)), at: at)
            }
            let kept = c.text.blocks()
            let found = DocumentMapper.blocks(s, trailing: c.text.trailing)
            #expect(kept.count == found.count, "round \(round)")
            #expect(
                zip(kept, found).allSatisfy { $0.info === $1.info && $0.range == $1.range },
                "round \(round)")
        }
    }

    /// A key typed leaves what is folded as it was, without asking the
    /// core again; a toggle opened is found at once.
    @Test func foldsAreFoundAgainAsWhatTheyGoByChanges() throws {
        let c = editor(
            [
                block(
                    "t", "toggle_list", "Details", data: ["collapsed": .bool(true)],
                    children: [block("in", "paragraph", "Hidden")])
            ] + (0..<50).map { block("p\($0)", "paragraph", "Paragraph \($0)") })
        #expect(c.layout.folded == ["in"])
        let changes = c.text.foldChanges
        let p = try #require(c.text.blocks().first { $0.info.id == "p10" })
        c.textView.selectedRange = NSRange(location: p.range.location, length: 0)
        c.type("x")
        _ = c.text.blocks()
        #expect(c.text.foldChanges == changes)
        #expect(c.layout.folded == ["in"])
        let (_, toggle) = c.paragraph(at: 0)
        c.setBlock(toggle.setting("collapsed", .bool(false)), at: 0)
        #expect(c.text.foldChanges != changes)
        #expect(c.layout.folded.isEmpty)
    }

    /// A change from outside to one block of a long page puts that block
    /// in alone: the others keep their paragraphs, and the page sent
    /// after has the change.
    @Test func aChangeFromOutsidePutsInOnlyWhatChanged() throws {
        var blocks = (0..<200).map { block("p\($0)", "paragraph", "Paragraph \($0)") }
        let c = editor(blocks)
        let before = Dictionary(uniqueKeysWithValues: c.text.blocks().map { ($0.info.id, $0.info) })
        blocks[100] = block("p100", "paragraph", "Changed by an agent")
        blocks.append(block("added", "heading", "Added", data: ["level": .int(2)]))
        c.update(page(blocks), tinted: [])
        let after = c.text.blocks()
        #expect(after.count == 201)
        for p in after where p.info.id != "p100" && p.info.id != "added" {
            #expect(p.info === before[p.info.id], "\(p.info.id) put in again")
        }
        var sent: DocContent?
        c.onEdit = { sent = $0 }
        c.send()
        #expect(sent?.blocks == blocks)

        // Typed in since, a block is sent as the text has it.
        let typed = try #require(c.text.blocks().first { $0.info.id == "p3" })
        c.textView.selectedRange = NSRange(location: NSMaxRange(typed.range), length: 0)
        c.type("!")
        c.send()
        blocks[3] = block("p3", "paragraph", "Paragraph 3!")
        #expect(sent?.blocks == blocks)
        #expect(
            sent == DocumentMapper.content(c.text.storage, trailing: c.text.trailing, meta: nil))
    }

    /// A list of many blocks comes back from the text whole, in order.
    @Test func aWideListComesBackWhole() {
        let kids = (0..<2000).map { block("c\($0)", "paragraph", "Child \($0)") }
        let list = block("list", "bulleted_list", "List", children: kids)
        let (text, trailing) = DocumentMapper.attributedString(page([list]))
        let back = DocumentMapper.content(text, trailing: trailing, meta: nil)
        #expect(back.blocks == [list])
    }
}
