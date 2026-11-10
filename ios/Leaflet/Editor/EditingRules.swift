import LeafletCore
import UIKit

@MainActor
final class EditingRules {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }
    private var edits: TextEdits { editor.edits }

    init(_ editor: EditorController) { self.editor = editor }

    func shouldChange(_ range: NSRange, _ s: String) -> Bool {
        if s.isEmpty, range.length > 0, NSMaxRange(range) == storage.length,
            text.paragraph(at: range.location).range.location == range.location
        {
            text.emptied = text.blockAt(range.location)
        }
        if edits.bypass || edits.undoing { return true }
        edits.inEdit += 1
        defer { edits.inEdit -= 1 }
        let allowed = follow(range, s)
        if !allowed {
            text.emptied = nil
            edits.tellKeyboard()
        }
        return allowed
    }

    private func follow(_ range: NSRange, _ s: String) -> Bool {
        let menus = editor.caretMenu
        if let menu = menus.menu, s == "\n" || s == "\t" {
            if let item = menu.current {
                menus.pick(item)
                return false
            }
            menus.close()
            if menu.loaded { return false }
        }
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
            if NSMaxRange(range) > NSMaxRange(para) { return replaceSelection(range, para, s) }
            edits.insertParagraph(
                after: para, info: BlockInfo.paragraph(depth: info.depth), text: s)
            return false
        }
        if s == " " || s == "\u{3000}", markdownShortcut(range, para, info, typing: s) {
            return false
        }
        if dividerShortcut(s, range, para, info) { return false }
        let clipboard = editor.clipboard
        let pasting =
            info.type != "code" && (s as NSString).length > 1
            && (range.length == 0 || range == textView.selectedRange)
        let fromPaste = textView.isPasting
        textView.pasteTaken()
        if pasting, fromPaste, clipboard.pastedCopy(s, replacing: range, para, info) {
            return false
        }
        if pasting, !s.contains("\u{FFFC}"),
            clipboard.pasted(s, replacing: range, para, info)
        {
            return false
        }
        let format = editor.format
        let length = (s as NSString).length
        if s.utf16.contains(where: { [0x0A, 0x0D, 0x2029, 0x85].contains($0) }) {
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

    private func replaceSelection(_ range: NSRange, _ para: NSRange, _ s: String) -> Bool {
        var gone = range
        if gone.location == NSMaxRange(para) {
            gone = NSRange(location: gone.location + 1, length: gone.length - 1)
        }
        if gone.length > 0 { edits.delete(gone) }
        let caret = NSRange(location: gone.location, length: 0)
        textView.selectedRange = caret
        if follow(caret, s) { edits.insert(s, replacing: caret) }
        return false
    }

    private func deleteAtEnd(_ para: NSRange, _ info: BlockInfo) -> Bool {
        guard let after = shownAfter(para) else { return false }
        let (next, nextInfo) = after
        switch MobileDelete(info.type, nextInfo.type) {
        case "selectAfter":
            textView.selectedRange = next
        case "selectThis":
            textView.selectedRange = para
        default:
            joinPast(next, into: para)
        }
        return false
    }

    private struct Line: Decodable {
        var action: String
        var type: String?
        var data: [String: JSONValue]?
        var unfold: Bool?
    }

    private func enter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) -> Bool {
        let caret = range.location
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
            let before = new(info.depth)
            storage.addAttribute(
                .leafletBlock, value: before, range: NSRange(location: para.location, length: 1))
            edits.setBlock(info, at: next, undoable: false)
            editor.layout.keepShown(before.id)
        default:
            let inside = line.action == "inside"
            let after = new(info.depth + (inside ? 1 : 0))
            if !inside, holds {
                splitAfterNested(range, para, after)
                return false
            }
            edits.insert("\n", replacing: range)
            let next = textView.selectedRange.location
            if line.unfold == true {
                edits.setBlock(info.setting("collapsed", .bool(false)), at: para.location)
            }
            edits.setBlock(after, at: next)
            editor.layout.keepShown(after.id)
        }
        return false
    }

    private func folded(_ info: BlockInfo) -> Bool {
        (info.type == "toggle_list" || info.type == "heading")
            && info.data["collapsed"]?.bool == true
    }

    private struct CodeLine: Decodable {
        var leave: Bool?
        var keep: Int?
        var insert: String?
    }

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

    private func hasNested(_ para: NSRange, _ info: BlockInfo) -> Bool {
        guard NSMaxRange(para) < storage.length else { return false }
        return text.paragraph(at: NSMaxRange(para) + 1).info.depth > info.depth
    }

    private func isLast(_ para: NSRange, _ info: BlockInfo) -> Bool {
        var at = NSMaxRange(para)
        while at < storage.length {
            let (next, nextInfo) = text.paragraph(at: at + 1)
            if nextInfo.depth <= info.depth { return nextInfo.depth < info.depth }
            at = NSMaxRange(next)
        }
        return true
    }

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

    func backspaceAtPageStart() -> Bool {
        guard textView.isEditable, textView.markedTextRange == nil,
            textView.selectedRange == NSRange(location: 0, length: 0)
        else { return false }
        let (para, info) = text.paragraph(at: 0)
        _ = backspaceAtStart(para, info)
        edits.tellKeyboard()
        return true
    }

    func roomToCompose() {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        let (para, info) = text.paragraph(at: textView.selectedRange.location)
        guard !info.isText else { return }
        edits.insertParagraph(after: para, info: BlockInfo.paragraph(depth: info.depth), text: "")
    }

    private func backspaceAtStart(_ para: NSRange, _ info: BlockInfo) -> Bool {
        if !info.isText {
            if info.depth > 0 {
                edits.setBlock(info.with(depth: info.depth - 1), at: para.location)
            } else if let prev = shownBefore(para) {
                textView.selectedRange =
                    prev.info.isText
                    ? NSRange(location: NSMaxRange(prev.range), length: 0) : prev.range
            }
            return false
        }
        let prev = shownBefore(para)
        let action = MobileBackspace(
            info.type, para.length == 0, info.depth > 0, isLast(para, info),
            prev?.info.type ?? "")
        switch action {
        case "outdent":
            edits.setBlock(info.with(depth: info.depth - 1), at: para.location)
        case "plain":
            edits.setBlock(info.with(type: "paragraph", data: [:]), at: para.location)
            editor.layout.keepShown(info.id)
        case "remove":
            if let prev {
                remove(para)
                textView.selectedRange = NSRange(location: NSMaxRange(prev.range), length: 0)
            }
        case "selectBefore":
            if let prev { textView.selectedRange = prev.range }
        case "nothing":
            break
        default:
            guard let prev else { return true }
            joinPast(para, into: prev.range)
        }
        return false
    }

    private func shownBefore(_ para: NSRange) -> (range: NSRange, info: BlockInfo)? {
        guard para.location > 0 else { return nil }
        let prev = text.paragraph(at: para.location - 1)
        let folded = editor.layout.folded
        guard folded.contains(prev.info.id) else { return prev }
        let all = text.blocks()
        let i = all.lastIndex { $0.range.location <= para.location } ?? 0
        return all[..<i].last { !folded.contains($0.info.id) }.map { ($0.range, $0.info) }
    }

    private func shownAfter(_ para: NSRange) -> (range: NSRange, info: BlockInfo)? {
        guard NSMaxRange(para) < storage.length else { return nil }
        let next = text.paragraph(at: NSMaxRange(para) + 1)
        let folded = editor.layout.folded
        guard folded.contains(next.info.id) else { return next }
        let all = text.blocks()
        let i = all.lastIndex { $0.range.location <= para.location } ?? 0
        return all[(i + 1)...].first { !folded.contains($0.info.id) }.map { ($0.range, $0.info) }
    }

    private func remove(_ para: NSRange) {
        let all = text.paragraphs()
        let i = BlockText.index(all, at: para.location)
        if i + 1 < all.count {
            edits.rewrite(all, i, i, [])
        } else if i > 0 {
            edits.rewrite(all, i - 1, i, [(all[i - 1].info, all[i - 1].text)])
        }
    }

    private func joinPast(_ para: NSRange, into prev: NSRange) {
        let all = text.paragraphs()
        let p = BlockText.index(all, at: prev.location)
        let i = BlockText.index(all, at: para.location)
        guard p < i else { return }
        let last = BlockText.subtree(all, i)
        let folded = editor.layout.folded
        let shown = all[(i + 1)...].first { !folded.contains($0.info.id) }?.info.id
        let joined = NSMutableAttributedString(attributedString: all[p].text)
        joined.append(all[i].text)
        let taken = p + 1 == i && all[p].text.length == 0 && !all[p].info.isCollapsed
        var blocks: [(info: BlockInfo, text: NSAttributedString)] = [
            (all[taken ? i : p].info, joined)
        ]
        blocks += all[(p + 1)..<i].map { ($0.info, $0.text) }
        blocks += all[(i + 1)..<(last + 1)].map {
            (taken ? $0.info : $0.info.with(depth: max($0.info.depth - 1, 0)), $0.text)
        }
        edits.rewrite(all, p, last, blocks)
        if let shown { editor.layout.keepShown(shown) }
        textView.selectedRange = NSRange(location: NSMaxRange(prev), length: 0)
    }

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

    func inlineShortcut(_ end: Int) -> Bool {
        let (para, info) = text.paragraph(at: end)
        guard info.isText, info.type != "code", end <= NSMaxRange(para) else { return false }
        let upTo = NSRange(location: para.location, length: end - para.location)
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
        editor.format.plainAt = NSMaxRange(after)
        textView.selectedRange = NSRange(location: NSMaxRange(after), length: 0)
        edits.tellKeyboard()
        return true
    }

    private struct InlineMatch: Decodable {
        var open: Int
        var inner: Int
        var close: Int
        var end: Int
        var format: String
    }
}
