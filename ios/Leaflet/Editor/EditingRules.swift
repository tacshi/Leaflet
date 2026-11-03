import LeafletCore
import UIKit

/// EditingRules follow the desktop's editor as keys go in: Enter splits a
/// block or ends a list, Backspace at a block's start turns it into text
/// or joins it to the one before, Tab nests it, Markdown typed becomes the
/// blocks and formats it stands for, and a mention is one unit. A rule
/// that takes a key changes the text itself, in place of the keyboard.
@MainActor
final class EditingRules {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }
    private var edits: TextEdits { editor.edits }

    init(_ editor: EditorController) { self.editor = editor }

    /// shouldChange applies the rules to a change the text view is about to
    /// make, and reports whether to let it through as it is.
    func shouldChange(_ range: NSRange, _ s: String) -> Bool {
        // An edit that empties the page's last paragraph leaves no
        // character to hold its block, which the trailing block keeps.
        if s.isEmpty, range.length > 0, NSMaxRange(range) == storage.length,
            text.paragraph(at: range.location).range.location == range.location
        {
            text.emptied = text.blockAt(range.location)
        }
        // Undo and redo put back what was: a rule taking one for an edit,
        // as Backspace at a list item's start, would refuse it, and leave
        // the steps before it where the text no longer is, which undone
        // there crashed the app.
        if edits.bypass || edits.undoing { return true }
        edits.inEdit += 1
        defer { edits.inEdit -= 1 }
        let allowed = follow(range, s)
        // A rule changed the text itself, in place of the keyboard's edit.
        if !allowed {
            text.emptied = nil
            edits.tellKeyboard()
        }
        return allowed
    }

    /// follow applies the editing rules to an edit the keyboard is making,
    /// and reports whether to let it through as it is.
    private func follow(_ range: NSRange, _ s: String) -> Bool {
        let menus = editor.caretMenu
        if let menu = menus.menu, s == "\n" || s == "\t" {
            if let item = menu.current {
                menus.pick(item)
                return false
            }
            menus.close()
            // Return or Tab before the menu has its items is the key's
            // own; once it has none to pick, it only closes the menu.
            if menu.loaded { return false }
        }
        // A mention is one unit, as on the desktop: an edit that cuts one
        // takes all of it, and what is typed inside one goes after it.
        if s != "\n", s != "\t" {
            let whole = text.widenToMentions(range)
            if whole != range {
                edits.insert(s, replacing: whole)
                textView.selectedRange = NSRange(
                    location: whole.location + (s as NSString).length, length: 0)
                return false
            }
            if range.length == 0, let run = text.mentionRun(at: range.location),
                run.location < range.location
            {
                edits.insert(s, replacing: NSRange(location: NSMaxRange(run), length: 0))
                textView.selectedRange = NSRange(
                    location: NSMaxRange(run) + (s as NSString).length, length: 0)
                return false
            }
        }
        let (para, info) = text.paragraph(at: range.location)
        switch s {
        case "\n":
            return enter(range, para, info)
        case "":
            guard range.length == 1 else { return true }
            let ns = storage.string as NSString
            let sel = textView.selectedRange
            if ns.character(at: range.location) == 0x0A, sel.length == 0 {
                // Backspace at a block's start deletes the separator
                // before it, the caret after it: the rules go by the block
                // the caret is in. Delete at a block's end deletes the
                // same separator, the caret before it.
                if sel.location == NSMaxRange(range) {
                    let (caretPara, caretInfo) = text.paragraph(at: NSMaxRange(range))
                    if caretPara.location == NSMaxRange(range) {
                        return backspaceAtStart(caretPara, caretInfo)
                    }
                } else if sel.location == range.location {
                    return deleteAtEnd(para, info)
                }
            }
            return true
        case "\t":
            editor.commands.indent(by: 1)
            return false
        default:
            break
        }
        if !info.isText {
            // Text typed by a block without text goes in a new block after
            // it.
            edits.insertParagraph(after: para, info: BlockInfo.paragraph(), text: s)
            return false
        }
        if s == " " || s == "\u{3000}", markdownShortcut(range, para, info, typing: s) {
            return false
        }
        if dividerShortcut(s, range, para, info) { return false }
        let clipboard = editor.clipboard
        if info.type != "code", (s as NSString).length > 1,
            clipboard.pastedCopy(s, replacing: range, para, info)
        {
            return false
        }
        if info.type != "code", (s as NSString).length > 1, !s.contains("\u{FFFC}"),
            clipboard.pasted(s, replacing: range, para, info)
        {
            return false
        }
        let format = editor.format
        let length = (s as NSString).length
        if s.utf16.contains(where: { [0x0A, 0x0D, 0x2029, 0x85].contains($0) }) {
            // Lines pasted, or written by dictation or Writing Tools: a
            // code block keeps them as its line breaks, in its one block.
            var lines = DocumentMapper.lines(s)
            if info.type == "code" {
                lines = lines.replacingOccurrences(of: "\n", with: String(lineBreak))
            }
            if lines != s {
                let attributes = format.typing
                edits.insert(lines, replacing: range)
                let r = NSRange(location: range.location, length: (lines as NSString).length)
                format.takeTyping(r, attributes)
                editor.edited(editor.paragraphs(around: r))
                return false
            }
        }
        format.typed = (
            NSRange(location: range.location, length: length),
            range.length > 0 ? format.replacing(range) : format.typing
        )
        if info.type != "code",
            let trigger = editor.caretMenu.trigger(s, at: range.location, in: para)
        {
            editor.caretMenu.opening = (range.location, trigger)
        }
        return true
    }

    /// deleteAtEnd follows Delete at the end of a block, which joins the
    /// block after it to it, as the core's rules have it (mobile.Delete): a
    /// block without text, after or before, is chosen first, to go with
    /// another Delete, as Backspace does.
    private func deleteAtEnd(_ para: NSRange, _ info: BlockInfo) -> Bool {
        let (next, nextInfo) = text.paragraph(at: NSMaxRange(para) + 1)
        switch MobileDelete(info.type, nextInfo.type) {
        case "selectAfter":
            textView.selectedRange = next
        case "selectThis":
            textView.selectedRange = para
        default:
            return true
        }
        return false
    }

    /// Line is what Enter does, as the core's rules say (mobile.Enter): an
    /// action, and the new block's type and data.
    private struct Line: Decodable {
        var action: String
        var type: String?
        var data: [String: JSONValue]?
        var unfold: Bool?
    }

    /// enter follows Enter at range in the block of para, as the core's
    /// rules have it, the desktop's (mobile.Enter): it splits the block,
    /// ends an empty one, or puts a line before it at its start. It makes
    /// the change itself, and lets the key through only should the core
    /// say nothing.
    private func enter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) -> Bool {
        let caret = range.location
        // A paragraph of a Markdown fence becomes its block first.
        if info.type == "paragraph", range.length == 0, caret == NSMaxRange(para), para.length >= 2,
            fenceEnter(para, info)
        {
            return false
        }
        let holds = hasNested(para, info)
        let raw = MobileEnter(
            info.type, folded(info), para.length == 0 && range.length == 0,
            caret == para.location && para.length > 0, info.depth > 0, isLast(para, info), holds)
        guard let line = try? JSONDecoder().decode(Line.self, from: Data(raw.utf8)) else {
            return true
        }
        let new = { (depth: Int) in
            BlockInfo(
                id: BlockInfo.newID(), type: line.type ?? "paragraph", data: line.data ?? [:],
                depth: depth)
        }
        switch line.action {
        case "after":
            edits.insertParagraph(after: para, info: new(info.depth), text: "")
        case "code":
            codeEnter(range, para, info)
        case "outdent":
            edits.setBlock(info.with(depth: info.depth - 1), at: para.location)
        case "plain":
            edits.setBlock(info.with(type: "paragraph", data: [:]), at: para.location)
        case "before":
            edits.insert("\n", replacing: range)
            let next = textView.selectedRange.location
            // Enter at the start makes an empty block before the text,
            // which keeps its block.
            let before = new(info.depth)
            storage.addAttribute(
                .leafletBlock, value: before, range: NSRange(location: para.location, length: 1))
            // The text keeps its block. The one normalize gave it for a
            // moment, as the line went in before it, is no step to undo:
            // undone first, it would leave the text a new ID, and no type.
            edits.setBlock(info, at: next, undoable: false)
            // Before a heading that ends a folded section, the new line
            // would join it, out of sight.
            editor.layout.keepShown(before.id)
        default:
            // Split the block: the text after the caret goes in a new one,
            // first in the blocks it holds, or after them.
            let inside = line.action == "inside"
            let after = new(info.depth + (inside ? 1 : 0))
            if !inside, holds {
                splitAfterNested(range, para, after)
                return false
            }
            edits.insert("\n", replacing: range)
            let next = textView.selectedRange.location
            if line.unfold == true {
                // A new line after a folded heading starts its section,
                // which shows.
                edits.setBlock(info.setting("collapsed", .bool(false)), at: para.location)
            }
            edits.setBlock(after, at: next)
            editor.layout.keepShown(after.id)
        }
        return false
    }

    /// folded reports whether info is a toggle or a heading folded.
    private func folded(_ info: BlockInfo) -> Bool {
        (info.type == "toggle_list" || info.type == "heading")
            && info.data["collapsed"]?.bool == true
    }

    /// CodeLine is what Enter does in code, as the core's rules say
    /// (mobile.CodeEnter).
    private struct CodeLine: Decodable {
        var leave: Bool?
        var keep: Int?
        var insert: String?
    }

    /// codeEnter breaks a code block's line at range, the new line indented
    /// as the one it leaves, as on the desktop (mobile.CodeEnter). Enter on
    /// the second blank line at its end leaves the block, without the blank
    /// lines, for the empty paragraph after it, or a new one.
    private func codeEnter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) {
        let br = String(lineBreak)
        let code = (storage.string as NSString).substring(with: para)
            .replacingOccurrences(of: br, with: "\n")
        let raw = MobileCodeEnter(code, range.location - para.location)
        guard let made = try? JSONDecoder().decode(CodeLine.self, from: Data(raw.utf8)) else {
            return
        }
        guard made.leave == true, range.length == 0 else {
            let line = (made.insert ?? "\n").replacingOccurrences(of: "\n", with: br)
            return edits.insert(line, replacing: range)
        }
        // The two blank lines and the breaks before them go.
        let kept = NSRange(location: para.location, length: made.keep ?? 0)
        edits.delete(NSRange(location: NSMaxRange(kept), length: para.length - kept.length))
        let (next, nextInfo) = text.paragraph(at: NSMaxRange(kept) + 1)
        if NSMaxRange(kept) < storage.length, next.length == 0, nextInfo.type == "paragraph",
            nextInfo.depth == info.depth
        {
            textView.selectedRange = NSRange(location: next.location, length: 0)
            return
        }
        edits.insertParagraph(
            after: kept,
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
    }

    /// fenceEnter turns a paragraph of a Markdown fence, as "```go", into a
    /// code block in that language as Enter goes at its end, as on the
    /// desktop (mobile.FenceEnter); "```mermaid" makes a diagram and "$$"
    /// math, opened to write. It reports whether it did.
    private func fenceEnter(_ para: NSRange, _ info: BlockInfo) -> Bool {
        let line = (storage.string as NSString).substring(with: para)
        guard line.contains("`") || line.contains("$") else { return false }
        struct Fence: Decodable {
            var code: Bool?
            var language: String?
            var block: DocNode?
        }
        var error: NSError?
        let raw = MobileFenceEnter(line, &error)
        guard error == nil, let f = try? JSONDecoder().decode(Fence.self, from: Data(raw.utf8))
        else { return false }
        if f.code == true {
            edits.delete(para)
            edits.setBlock(
                info.with(type: "code", data: ["language": .string(f.language ?? "")]),
                at: para.location)
            // A code block last on the page has a line after it, to leave
            // it by.
            if para.location == storage.length
                || NSMaxRange(text.paragraph(at: para.location).range) == storage.length
            {
                edits.insertParagraph(
                    after: text.paragraph(at: para.location).range,
                    info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth),
                    text: "")
                textView.selectedRange = NSRange(location: para.location, length: 0)
            }
            return true
        }
        guard let block = f.block else { return false }
        edits.delete(para)
        editor.commands.insertBlock(block, at: para.location)
        editor.onEditBlock?(block)
        return true
    }

    /// hasNested reports whether blocks are nested in the block of the
    /// paragraph para.
    private func hasNested(_ para: NSRange, _ info: BlockInfo) -> Bool {
        guard NSMaxRange(para) < storage.length else { return false }
        return text.paragraph(at: NSMaxRange(para) + 1).info.depth > info.depth
    }

    /// isLast reports whether the block of the paragraph para is the last
    /// of the blocks in the one it is in: no block after it at its depth
    /// before one shallower.
    private func isLast(_ para: NSRange, _ info: BlockInfo) -> Bool {
        var at = NSMaxRange(para)
        while at < storage.length {
            let (next, nextInfo) = text.paragraph(at: at + 1)
            if nextInfo.depth <= info.depth { return nextInfo.depth < info.depth }
            at = NSMaxRange(next)
        }
        return true
    }

    /// splitAfterNested splits a closed toggle at range: the text after
    /// the caret goes in a new block, new, after the blocks folded in it,
    /// which stay in it, as on the desktop.
    private func splitAfterNested(_ range: NSRange, _ para: NSRange, _ new: BlockInfo) {
        if range.length > 0 { edits.delete(range) }
        let caret = range.location
        let all = text.paragraphs()
        let i = BlockText.index(all, at: para.location)
        let end = NSMaxRange(all[BlockText.subtree(all, i)].range)
        let tail = NSRange(
            location: caret, length: NSMaxRange(text.paragraph(at: caret).range) - caret)
        let piece = NSMutableAttributedString(
            string: "\n", attributes: [.leafletBlock: text.paragraph(at: end).info])
        let moved = NSMutableAttributedString(
            attributedString: storage.attributedSubstring(from: tail))
        moved.addAttribute(
            .leafletBlock, value: new, range: NSRange(location: 0, length: moved.length))
        piece.append(moved)
        edits.replace(NSRange(location: end, length: 0), with: piece)
        if tail.length > 0 { edits.replace(tail, with: NSAttributedString()) }
        let start = end - tail.length + 1
        edits.setBlock(new, at: start, undoable: false)
        textView.selectedRange = NSRange(location: start, length: 0)
    }

    /// backspaceAtPageStart follows Backspace with the caret at the start
    /// of the page, which UIKit asks no rule about, and reports whether it
    /// took it.
    func backspaceAtPageStart() -> Bool {
        guard textView.isEditable, textView.markedTextRange == nil,
            textView.selectedRange == NSRange(location: 0, length: 0)
        else { return false }
        let (para, info) = text.paragraph(at: 0)
        _ = backspaceAtStart(para, info)
        edits.tellKeyboard()
        return true
    }

    /// roomToCompose puts the caret in a new paragraph after a block
    /// without text it is by, as a composition starts there (Chinese,
    /// Japanese or Korean input): text typed by such a block goes in a new
    /// block after it, which the rules can't do for marked text, the
    /// keyboard keeping it where it began.
    func roomToCompose() {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        let (para, info) = text.paragraph(at: textView.selectedRange.location)
        guard !info.isText else { return }
        edits.insertParagraph(after: para, info: BlockInfo.paragraph(), text: "")
    }

    /// backspaceAtStart follows Backspace at the start of the block of
    /// para, as the core's rules have it (mobile.Backspace): a list item, a
    /// quote or a heading turns into text, or goes out of the list it is
    /// in, as does a paragraph last in a toggle; else the block joins the
    /// one before, a block without text before it chosen first.
    private func backspaceAtStart(_ para: NSRange, _ info: BlockInfo) -> Bool {
        if !info.isText {
            // The caret before a block without text: it goes out of the
            // list it is in, or the caret back into the block before.
            if info.depth > 0 {
                edits.setBlock(info.with(depth: info.depth - 1), at: para.location)
            } else if para.location > 0 {
                let (prev, prevInfo) = text.paragraph(at: para.location - 1)
                textView.selectedRange =
                    prevInfo.isText ? NSRange(location: NSMaxRange(prev), length: 0) : prev
            }
            return false
        }
        let prev = para.location > 0 ? text.paragraph(at: para.location - 1) : nil
        let action = MobileBackspace(
            info.type, para.length == 0, info.depth > 0, isLast(para, info),
            prev?.info.type ?? "")
        switch action {
        case "outdent":
            edits.setBlock(info.with(depth: info.depth - 1), at: para.location)
        case "plain":
            edits.setBlock(info.with(type: "paragraph", data: [:]), at: para.location)
            // A heading that ended a folded section would now join it.
            editor.layout.keepShown(info.id)
        case "remove":
            // An empty block after an image goes.
            if let prev { edits.delete(NSRange(location: NSMaxRange(prev.range), length: 1)) }
        case "selectBefore":
            // The image is chosen first, to be deleted with another
            // Backspace.
            if let prev { textView.selectedRange = prev.range }
        case "nothing":
            break
        default:
            return true
        }
        return false
    }

    /// markdownShortcut turns "#", "-", "1.", "[]", ">", "```" and the like,
    /// as a space is typed after them at a block's start, into the block
    /// they stand for, as the core's rules have it, the desktop's.
    private func markdownShortcut(
        _ range: NSRange, _ para: NSRange, _ info: BlockInfo, typing space: String
    ) -> Bool {
        guard range.length == 0 else { return false }
        let prefix = (storage.string as NSString).substring(
            with: NSRange(location: para.location, length: range.location - para.location))
        let raw = MobileMarkdownShortcut(info.type, prefix + space)
        guard
            let made = try? JSONDecoder().decode(
                CaretMenuController.MenuBlock.self, from: Data(raw.utf8)),
            let type = made.type
        else { return false }
        edits.delete(NSRange(location: para.location, length: prefix.utf16.count))
        edits.setBlock(info.with(type: type, data: made.data ?? [:]), at: para.location)
        return true
    }

    /// dividerShortcut turns a paragraph of "--" and a third "-" typed,
    /// full-width too, into a divider, as the core's rules have it.
    private func dividerShortcut(
        _ typed: String, _ range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        guard typed.count == 1, range.length == 0, range.location == NSMaxRange(para),
            MobileDividerShortcut(
                info.type, (storage.string as NSString).substring(with: para) + typed)
        else { return false }
        edits.delete(para)
        let divider = DocNode(id: BlockInfo.newID(), type: "divider", data: nil, children: nil)
        let (attachment, dInfo) = DocumentMapper.paragraph(divider, depth: info.depth)
        edits.replace(NSRange(location: para.location, length: 0), with: attachment)
        edits.setBlock(dInfo, at: para.location, undoable: false)
        edits.insertParagraph(
            after: NSRange(location: para.location, length: 1),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
        return true
    }

    /// inlineShortcut turns **bold**, *italic*, `code` and ~~strike~~ typed
    /// in full, its closing marker ending at end, into their formats, as the
    /// core's rules have it, the desktop's. It reports whether it did.
    func inlineShortcut(_ end: Int) -> Bool {
        let (para, info) = text.paragraph(at: end)
        guard info.isText, info.type != "code", end <= NSMaxRange(para) else { return false }
        let upTo = NSRange(location: para.location, length: end - para.location)
        // The mentions' runs, for a marker in one to be no marker.
        var mentions: [[Int]] = []
        storage.enumerateAttribute(.leafletMention, in: upTo) { v, r, _ in
            if v != nil {
                mentions.append([r.location - para.location, NSMaxRange(r) - para.location])
            }
        }
        let runs = (try? JSONEncoder().encode(mentions)).map { String(decoding: $0, as: UTF8.self) }
        let raw = MobileInlineShortcut(
            (storage.string as NSString).substring(with: upTo), runs ?? "[]")
        guard let m = try? JSONDecoder().decode(InlineMatch.self, from: Data(raw.utf8)) else {
            return false
        }
        let (key, value): (NSAttributedString.Key, Any) =
            switch m.format {
            case "bold": (.leafletBold, true)
            case "strike": (.leafletStrike, true)
            case "code": (.leafletCode, true)
            default: (.leafletItalic, true)
            }
        let at = para.location
        let whole = NSRange(location: at + m.open, length: m.end - m.open)
        let before = storage.attributedSubstring(from: whole)
        let undo = textView.undoManager
        edits.bypass = true
        storage.beginEditing()
        storage.replaceCharacters(
            in: NSRange(location: at + m.close, length: m.end - m.close), with: "")
        storage.addAttribute(
            key, value: value, range: NSRange(location: at + m.inner, length: m.close - m.inner))
        storage.replaceCharacters(
            in: NSRange(location: at + m.open, length: m.inner - m.open), with: "")
        storage.endEditing()
        edits.bypass = false
        let after = NSRange(location: whole.location, length: m.close - m.inner)
        edits.registerUndo(undo, range: after, before: before, trailing: text.trailing)
        // What is typed after the closing marker is plain again.
        editor.format.plainAt = NSMaxRange(after)
        textView.selectedRange = NSRange(location: NSMaxRange(after), length: 0)
        edits.tellKeyboard()
        return true
    }

    /// InlineMatch is a marker pair the core's InlineShortcut found, in
    /// UTF-16 offsets in the block: the opening marker from open to inner,
    /// the text to format to close, and the closing marker to end.
    private struct InlineMatch: Decodable {
        var open: Int
        var inner: Int
        var close: Int
        var end: Int
        var format: String
    }
}
