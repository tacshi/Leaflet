import LeafletCore
import UIKit

@MainActor
final class EditorClipboard {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }
    private var edits: TextEdits { editor.edits }

    init(_ editor: EditorController) { self.editor = editor }

    struct Clip {
        var text: String?
        var blocks: [DocNode] = []
        var inline: NSAttributedString?
        var count: Int?
        var out = UIPasteboard.general.changeCount
        let id = UUID()
    }

    private static var clip: Clip?

    func copied() {
        let range = textView.selectedRange
        guard range.length > 0 else { return }
        let piece = storage.attributedSubstring(from: range)
        if !piece.string.contains("\n"), !piece.string.contains("\u{FFFC}") {
            Self.clip = Clip(text: piece.string, inline: piece)
            return
        }
        copy(blocks: DocumentMapper.content(piece, meta: nil).document.children ?? [])
    }

    func copy(blocks: [DocNode]) {
        let clip = Clip(blocks: blocks)
        Self.clip = clip
        guard let copyText = editor.copyText else { return }
        Task {
            guard let text = await copyText(blocks), !text.isEmpty, let now = Self.clip,
                now.id == clip.id, now.out == UIPasteboard.general.changeCount
            else { return }
            UIPasteboard.general.string = text
            Self.clip?.text = text
            Self.clip?.count = UIPasteboard.general.changeCount
        }
    }

    func copiedOut() {
        Self.clip?.out = UIPasteboard.general.changeCount
        if Self.clip?.inline != nil { Self.clip?.count = UIPasteboard.general.changeCount }
    }

    func copyPlainText() {
        let range = textView.selectedRange
        guard textView.isFirstResponder, range.length > 0, let plainCopy = editor.plainCopy else {
            return
        }
        let nodes =
            DocumentMapper.content(storage.attributedSubstring(from: range), meta: nil)
            .document.children ?? []
        Task {
            if let text = await plainCopy(nodes), !text.isEmpty {
                UIPasteboard.general.string = text
            }
        }
    }

    func pastedCopy(
        _ s: String, replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        let lines = { (s: String) in DocumentMapper.lines(s) }
        guard let clip = Self.clip, let copied = clip.text,
            clip.count == UIPasteboard.general.changeCount
        else {
            return false
        }
        if let inline = clip.inline {
            // The system puts spaces around a word it pastes beside others,
            // and line ends around a paragraph that was copied whole: the
            // spaces are kept, and the copy goes in where the caret is.
            guard let around = Self.padding(of: lines(copied), in: lines(s)) else { return false }
            let made = NSMutableAttributedString(attributedString: inline)
            let plain = editor.format.typing
            made.insert(NSAttributedString(string: around.before, attributes: plain), at: 0)
            made.append(NSAttributedString(string: around.after, attributes: plain))
            made.addAttribute(
                .leafletBlock, value: info, range: NSRange(location: 0, length: made.length))
            edits.replace(range, with: made)
            let r = NSRange(location: range.location, length: made.length)
            textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
            editor.edited(editor.paragraphs(around: r))
            return true
        }
        guard !clip.blocks.isEmpty, lines(copied) == lines(s) else { return false }
        pasteBlocks(clip.blocks.map(Self.renewed), replacing: range, para, info)
        return true
    }

    /// The spaces the system put before and after `copied` to make `pasted`,
    /// or nil if `pasted` is anything but `copied` with space or line ends
    /// around it.
    static func padding(of copied: String, in pasted: String) -> (before: String, after: String)? {
        guard !copied.isEmpty, let at = pasted.range(of: copied) else { return nil }
        let (before, after) = (pasted[..<at.lowerBound], pasted[at.upperBound...])
        guard before.allSatisfy(\.isWhitespace), after.allSatisfy(\.isWhitespace) else {
            return nil
        }
        return (String(before.filter { !$0.isNewline }), String(after.filter { !$0.isNewline }))
    }

    private func pasteBlocks(
        _ blocks: [DocNode], replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) {
        let page = DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil)
        var flat = DocumentMapper.flatten(page).map {
            (node: $0.node, depth: $0.depth + info.depth)
        }
        guard let first = flat.first?.node, let last = flat.last?.node else { return }
        let joins = { (type: String) in BlockInfo.textTypes.contains(type) && type != "code" }
        let (end, endInfo) = text.paragraph(at: NSMaxRange(range))
        let tail = NSRange(
            location: NSMaxRange(range), length: NSMaxRange(end) - NSMaxRange(range))
        let atEnd = NSMaxRange(end) == storage.length
        let head = range.location > para.location
        let apart = tail.length > 0 && (!joins(last.type) || !endInfo.isText)
        let kept = head || joins(first.type)
        let many = flat.count > 1 || !joins(first.type)
        if tail.length > 0, !apart, flat.count > 1 {
            var data = info.data
            data["due"] = nil
            data["delta"] = last.data?["delta"]
            flat[flat.count - 1].node.type = info.type
            flat[flat.count - 1].node.data = data
            if !kept, endInfo === info { flat[flat.count - 1].node.id = info.id }
        }
        if !head, joins(first.type) {
            var node = flat[0].node
            node.id = info.id
            if first.type == "paragraph" {
                var data = info.data
                data["delta"] = first.data?["delta"]
                node.type = info.type
                node.data = data
            }
            if many, node.type == "heading", node.data?["collapsed"]?.bool == true {
                node.data?["collapsed"] = .bool(false)
            }
            flat[0].node = node
        }
        let (built, infos, _) = DocumentMapper.attributedString(flat)
        let made = NSMutableAttributedString(attributedString: built)
        if !joins(first.type), head {
            made.insert(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]), at: 0)
        }
        var after: BlockInfo?
        var blank: BlockInfo?
        if apart, let block = infos.last {
            made.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: block]))
            if kept, endInfo === info {
                after = info.setting("due", nil).with(id: BlockInfo.newID())
            }
        } else if tail.length > 0, flat.count > 1 {
            after = infos.last
        } else if tail.length == 0, !joins(last.type), let block = infos.last {
            made.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: block]))
            blank =
                !kept && para.length == 0
                ? info : BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth)
        }
        let caret = range.location + made.length
        var replaced = range
        if let after {
            let rest = NSMutableAttributedString(
                attributedString: storage.attributedSubstring(from: tail))
            rest.addAttribute(
                .leafletBlock, value: after, range: NSRange(location: 0, length: rest.length))
            made.append(rest)
            replaced.length += tail.length
        }
        if let blank, !atEnd {
            made.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: blank]))
            replaced.length += 1
        }
        edits.replace(replaced, with: made)
        if let blank, atEnd {
            text.trailing = blank
            editor.layout.redrawEnd()
        }
        let r = NSRange(location: range.location, length: made.length)
        textView.selectedRange = NSRange(location: caret, length: 0)
        editor.edited(editor.paragraphs(around: r))
        let start = text.paragraph(at: para.location).info
        if head, many, start.type == "heading", start.isCollapsed {
            edits.setBlock(start.setting("collapsed", .bool(false)), at: para.location)
        }
        editor.layout.keepShown(text.paragraph(at: caret).info.id)
    }

    func pasted(
        _ s: String, replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        struct Pasted: Decodable {
            var blocks: [DocNode]?
            var media: DocNode?
            var link: String?
        }
        let empty = para.length == 0 && info.isText && range.length == 0
        var error: NSError?
        let raw = MobilePasteText(s, empty, &error)
        guard error == nil, let p = try? JSONDecoder().decode(Pasted.self, from: Data(raw.utf8))
        else { return false }
        if let blocks = p.blocks, !blocks.isEmpty {
            pasteBlocks(blocks, replacing: range, para, info)
            return true
        }
        if let media = p.media {
            editor.commands.insertBlock(media)
            return true
        }
        guard let link = p.link, !link.isEmpty else { return false }
        if range.length > 0 {
            textView.selectedRange = range
            editor.format.set(.leafletHref, link)
            return true
        }
        var attributes = editor.format.typing
        attributes[.leafletHref] = link
        edits.replace(range, with: NSAttributedString(string: s, attributes: attributes))
        let r = NSRange(location: range.location, length: (s as NSString).length)
        textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
        editor.edited(editor.paragraphs(around: r))
        if empty, MobileShortVideoLink(link) { follow(link, in: info.id) }
        return true
    }

    private func follow(_ link: String, in id: String) {
        Task { [weak editor] in
            let raw = await Task.detached { () -> String? in
                var error: NSError?
                let raw = MobileLinkBlock("video", link, &error)
                return error == nil ? raw : nil
            }.value
            guard let editor, let raw,
                let video = try? JSONDecoder().decode(DocNode.self, from: Data(raw.utf8))
            else { return }
            editor.commands.shareToVideo(id, link: link, video: video)
        }
    }

    func liftCards() -> NSRange? {
        let all = NSRange(location: 0, length: storage.length)
        var cards: [(range: NSRange, card: BlockAttachment)] = []
        storage.enumerateAttribute(.attachment, in: all) { v, r, _ in
            if let card = v as? BlockAttachment { cards.append((r, card)) }
        }
        let ns = storage.string as NSString
        var kept = Set<String>()
        var lift: [(range: NSRange, card: BlockAttachment)] = []
        for (r, card) in cards {
            let info =
                storage.attribute(.leafletBlock, at: r.location, effectiveRange: nil) as? BlockInfo
            let para = ns.paragraphRange(for: NSRange(location: r.location, length: 0))
            var length = para.length
            if length > 0, ns.character(at: NSMaxRange(para) - 1) == 0x0A { length -= 1 }
            if let info, !info.isText, info.type == card.node.type, info.id == card.node.id,
                length == r.length, !kept.contains(info.id)
            {
                kept.insert(info.id)
            } else {
                lift.append((r, card))
            }
        }
        guard let first = lift.first else { return nil }
        for (r, card) in lift.reversed() {
            let ns = storage.string as NSString
            let para = ns.paragraphRange(for: NSRange(location: r.location, length: 0))
            var end = NSMaxRange(para)
            if end > para.location, ns.character(at: end - 1) == 0x0A { end -= 1 }
            let depth =
                (storage.attribute(.leafletBlock, at: r.location, effectiveRange: nil) as? BlockInfo)?
                .depth ?? 0
            var node = card.node
            if kept.contains(node.id) { node = Self.renewed(node) }
            kept.insert(node.id)
            let (t, info) = DocumentMapper.paragraph(node, depth: depth)
            let piece = NSMutableAttributedString()
            if r.location > para.location {
                piece.append(
                    NSAttributedString(
                        string: "\n",
                        attributes: storage.attributes(at: r.location - 1, effectiveRange: nil)))
            }
            piece.append(t)
            if NSMaxRange(r) < end {
                piece.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]))
            }
            edits.replace(r, with: piece)
        }
        return NSRange(
            location: first.range.location, length: storage.length - first.range.location)
    }

    private static func renewed(_ node: DocNode) -> DocNode {
        var n = node
        n.id = BlockInfo.newID()
        n.children = n.children?.map(renewed)
        return n
    }
}
