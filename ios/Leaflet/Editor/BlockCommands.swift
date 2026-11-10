import LeafletCore
import UIKit

@MainActor
final class BlockCommands {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }
    private var edits: TextEdits { editor.edits }

    init(_ editor: EditorController) { self.editor = editor }

    func indent(by step: Int) {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        if text.paragraph(at: textView.selectedRange.location).info.type == "code" {
            indentCode(by: step)
            return
        }
        let caret = text.position(at: textView.selectedRange.location)
        let selection = textView.selectedRange
        var all = text.paragraphs()
        let chosen = Set(text.paragraphs(touching: selection).map(\.info.id))
        var changed: ClosedRange<Int>?
        var shown: String?
        var first = true
        var i = 0
        while i < all.count {
            let last = BlockText.subtree(all, i)
            defer { i = chosen.contains(all[i].info.id) ? last + 1 : i + 1 }
            guard chosen.contains(all[i].info.id) else { continue }
            let info = all[i].info
            let depth = max(info.depth + step, 0)
            if step > 0 {
                defer { first = false }
                guard let under = sibling(before: i, in: all),
                    !editor.layout.folded.contains(under.id), MobileNests(info.type, under.type)
                else {
                    if first { return }
                    continue
                }
                if under.type == "toggle_list" { shown = info.id }
            }
            guard depth != info.depth else { continue }
            if step < 0, chosen.count == 1, !info.isList, info.depth > 0,
                last + 1 < all.count, all[last + 1].info.depth == info.depth
            {
                var end = last + 1
                while end + 1 < all.count, all[end + 1].info.depth >= info.depth { end += 1 }
                let moved = all[i...last].map { p in
                    (p.info.with(depth: p.info.depth - 1), p.text)
                }
                edits.rewrite(all, i, end, all[(last + 1)...end].map { ($0.info, $0.text) } + moved)
                restoreCaret(caret)
                return
            }
            for k in i...last {
                all[k].info = all[k].info.with(depth: all[k].info.depth + depth - info.depth)
            }
            changed = (changed?.lowerBound ?? i)...last
        }
        guard let changed else { return }
        edits.rewrite(
            all, changed.lowerBound, changed.upperBound, all[changed].map { ($0.info, $0.text) })
        restoreCaret(caret)
        if selection.length > 0 { textView.selectedRange = selection }
        if let shown { editor.layout.keepShown(shown) }
    }

    private func sibling(before i: Int, in all: [DocumentMapper.Paragraph]) -> BlockInfo? {
        var k = i - 1
        while k >= 0, all[k].info.depth > all[i].info.depth { k -= 1 }
        return k >= 0 && all[k].info.depth == all[i].info.depth ? all[k].info : nil
    }

    private struct Indented: Decodable {
        var code: String
        var anchor: Int
        var focus: Int
    }

    private func indentCode(by step: Int) {
        let sel = textView.selectedRange
        let (para, info) = text.paragraph(at: sel.location)
        let br = String(lineBreak)
        let code = (storage.string as NSString).substring(with: para)
            .replacingOccurrences(of: br, with: "\n")
        let from = sel.location - para.location
        let raw = MobileIndentCode(code, from, NSMaxRange(sel) - para.location, step < 0)
        guard let made = try? JSONDecoder().decode(Indented.self, from: Data(raw.utf8)) else {
            return
        }
        let lines = made.code.replacingOccurrences(of: "\n", with: br)
        if step > 0, sel.length == 0 {
            let unit = (lines as NSString).substring(
                with: NSRange(location: from, length: made.anchor - from))
            edits.insert(unit, replacing: sel)
            return
        }
        edits.replace(
            para, with: Self.indented(storage.attributedSubstring(from: para), lines, info))
        editor.edited(NSRange(location: para.location, length: (lines as NSString).length))
        let a = para.location + min(made.anchor, made.focus)
        textView.selectedRange = NSRange(location: a, length: abs(made.focus - made.anchor))
    }

    static func indented(_ code: NSAttributedString, _ lines: String, _ info: BlockInfo)
        -> NSAttributedString
    {
        let br = String(lineBreak)
        let plain = NSAttributedString(string: lines, attributes: [.leafletBlock: info])
        let was = code.string.components(separatedBy: br)
        let now = lines.components(separatedBy: br)
        guard was.count == now.count else { return plain }
        var starts: [Int] = []
        var at = 0
        for line in was {
            starts.append(at)
            at += (line as NSString).length + 1
        }
        let out = NSMutableAttributedString(attributedString: code)
        for k in was.indices.reversed() {
            let old = (was[k] as NSString).length
            let new = now[k] as NSString
            if new.length > old {
                out.insert(
                    NSAttributedString(
                        string: new.substring(to: new.length - old),
                        attributes: [.leafletBlock: info]), at: starts[k])
            } else if new.length < old {
                out.deleteCharacters(in: NSRange(location: starts[k], length: old - new.length))
            }
        }
        out.addAttribute(
            .leafletBlock, value: info, range: NSRange(location: 0, length: out.length))
        return out.string == lines ? out : plain
    }

    func setType(_ type: String, _ data: [String: JSONValue] = [:]) {
        guard textView.isEditable else { return }
        for (range, info) in text.paragraphs(touching: textView.selectedRange) where info.isText {
            let same =
                info.type == type
                && (type != "heading" || info.level == Int(data["level"]?.int ?? 0))
            edits.setBlock(
                same ? info.with(type: "paragraph", data: [:]) : info.with(type: type, data: data),
                at: range.location)
        }
    }

    func moveBlock(by step: Int) {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        let caret = text.position(at: textView.selectedRange.location)
        let all = text.paragraphs()
        let i = BlockText.index(all, at: textView.selectedRange.location)
        let last = BlockText.subtree(all, i)
        let depth = all[i].info.depth
        func block(_ p: DocumentMapper.Paragraph, by shift: Int = 0) -> (
            BlockInfo, NSAttributedString
        ) {
            (shift == 0 ? p.info : p.info.with(depth: p.info.depth + shift), p.text)
        }
        if step < 0 {
            var k = i - 1
            while k >= 0, all[k].info.depth > depth { k -= 1 }
            guard k >= 0 else { return }
            let shift = all[k].info.depth - depth
            edits.rewrite(
                all, k, last,
                all[i...last].map { block($0, by: shift) } + all[k..<i].map { block($0) })
        } else {
            guard last + 1 < all.count else { return }
            let next = all[last + 1].info.depth
            if next == depth {
                let end = BlockText.subtree(all, last + 1)
                edits.rewrite(
                    all, i, end,
                    all[(last + 1)...end].map { block($0) } + all[i...last].map { block($0) })
            } else {
                edits.rewrite(all, i, last, all[i...last].map { block($0, by: -1) })
            }
        }
        restoreCaret(caret)
        editor.layout.keepShown(caret.id)
    }

    func softBreak() {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        let chosen = text.paragraphs(touching: textView.selectedRange)
        guard chosen.allSatisfy(\.info.isText) else { return }
        edits.insert(String(lineBreak), replacing: textView.selectedRange)
    }

    func replaceBlock(_ node: DocNode) {
        guard textView.isEditable else { return }
        guard let p = text.blocks().first(where: { $0.info.id == node.id }), !p.info.isText
        else { return }
        var node = node
        if node.type.isEmpty { node.type = p.info.type }
        let (attachment, info) = DocumentMapper.paragraph(node, depth: p.info.depth)
        let at = NSRange(location: p.range.location, length: 1)
        edits.keepingScroll {
            edits.replace(at, with: attachment)
            edits.setBlock(info, at: at.location, undoable: false)
        }
    }

    func removeBlock(_ id: String) {
        guard textView.isEditable else { return }
        let all = text.paragraphs()
        guard let i = all.firstIndex(where: { $0.info.id == id }), !all[i].info.isText else {
            return
        }
        let last = BlockText.subtree(all, i)
        let caret = text.position(at: textView.selectedRange.location)
        edits.keepingScroll {
            if last + 1 < all.count {
                edits.rewrite(all, i, last, [])
            } else if i > 0 {
                edits.rewrite(all, i - 1, last, [(all[i - 1].info, all[i - 1].text)])
            } else {
                let blank = BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: 0)
                edits.rewrite(all, i, last, [(blank, NSAttributedString())])
            }
        }
        restoreCaret(caret)
    }

    func insertBlock(_ node: DocNode, at location: Int? = nil) {
        guard textView.isEditable else { return }
        let caret = location == nil ? nil : text.position(at: textView.selectedRange.location)
        var (para, info) = text.paragraph(at: location ?? textView.selectedRange.location)
        let (attachment, blockInfo) = DocumentMapper.paragraph(node, depth: info.depth)
        if para.length == 0, info.isText {
            edits.replace(NSRange(location: para.location, length: 0), with: attachment)
        } else {
            let end = NSMaxRange(para)
            let piece = NSMutableAttributedString(string: "\n", attributes: [.leafletBlock: info])
            piece.append(attachment)
            edits.replace(NSRange(location: end, length: 0), with: piece)
            para = NSRange(location: end + 1, length: 0)
        }
        edits.setBlock(blockInfo, at: para.location, undoable: false)
        edits.insertParagraph(
            after: NSRange(location: para.location, length: attachment.length),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
        if let caret { restoreCaret(caret) }
    }

    func shareToVideo(_ id: String, link: String, video: DocNode) {
        guard textView.isEditable,
            let p = text.paragraphs().first(where: { $0.info.id == id }),
            p.info.type == "paragraph",
            p.text.string.trimmingCharacters(in: .whitespaces) == link
        else { return }
        let all = NSRange(location: 0, length: p.text.length)
        var linked = true
        p.text.enumerateAttribute(.leafletHref, in: all) { value, range, _ in
            let words = (p.text.string as NSString).substring(with: range)
            if value as? String != link, !words.trimmingCharacters(in: .whitespaces).isEmpty {
                linked = false
            }
        }
        guard linked else { return }
        let caret = text.position(at: textView.selectedRange.location)
        let (attachment, info) = DocumentMapper.paragraph(video, depth: p.info.depth)
        edits.keepingScroll {
            edits.replace(p.range, with: attachment)
            edits.setBlock(info, at: p.range.location, undoable: false)
            edits.insertParagraph(
                after: NSRange(location: p.range.location, length: attachment.length),
                info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: p.info.depth),
                text: "")
        }
        if caret.id != id { restoreCaret(caret) }
    }

    func insertBlocks(_ nodes: [DocNode], after id: String) {
        guard textView.isEditable, !nodes.isEmpty else { return }
        let caret = text.position(at: textView.selectedRange.location)
        let all = text.paragraphs()
        var at = storage.length
        var depth = 0
        if let i = all.firstIndex(where: { $0.info.id == id }) {
            at = NSMaxRange(all[BlockText.subtree(all, i)].range)
            depth = all[i].info.depth
        }
        let page = DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: nodes), meta: nil)
        let made = NSMutableAttributedString(
            attributedString: DocumentMapper.attributedString(page).text)
        var last: BlockInfo?
        made.enumerateAttribute(.leafletBlock, in: NSRange(location: 0, length: made.length)) {
            value, range, _ in
            guard let info = value as? BlockInfo else { return }
            let shifted = info.with(depth: info.depth + depth)
            made.addAttribute(.leafletBlock, value: shifted, range: range)
            last = shifted
        }
        made.insert(
            NSAttributedString(
                string: "\n", attributes: [.leafletBlock: text.paragraph(at: at).info]),
            at: 0)
        let r = NSRange(location: at, length: made.length)
        edits.keepingScroll {
            edits.replace(NSRange(location: at, length: 0), with: made)
            if let last, NSMaxRange(r) < storage.length {
                storage.addAttribute(
                    .leafletBlock, value: last, range: NSRange(location: NSMaxRange(r), length: 1))
            }
            editor.edited(editor.paragraphs(around: r))
            restoreCaret(caret)
        }
    }

    func restoreCaret(_ caret: BlockText.Position?) {
        guard let caret, let at = text.place(caret) else { return }
        textView.selectedRange = NSRange(location: at, length: 0)
    }
}
