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
        Self.clip = Clip(blocks: blocks)
        guard let copyText = editor.copyText else { return }
        Task {
            guard let text = await copyText(blocks), !text.isEmpty else { return }
            UIPasteboard.general.string = text
            Self.clip?.text = text
            Self.clip?.count = UIPasteboard.general.changeCount
        }
    }

    func copiedOut() {
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
        guard let clip = Self.clip, let copied = clip.text, lines(copied) == lines(s),
            clip.count == UIPasteboard.general.changeCount
        else {
            return false
        }
        if let inline = clip.inline {
            let made = NSMutableAttributedString(attributedString: inline)
            made.addAttribute(
                .leafletBlock, value: info, range: NSRange(location: 0, length: made.length))
            edits.replace(range, with: made)
            let r = NSRange(location: range.location, length: made.length)
            textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
            editor.edited(editor.paragraphs(around: r))
            return true
        }
        guard !clip.blocks.isEmpty else { return false }
        pasteBlocks(clip.blocks.map(Self.renewed), replacing: range, para, info)
        return true
    }

    private func pasteBlocks(
        _ blocks: [DocNode], replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) {
        let page = DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil)
        let made = NSMutableAttributedString(
            attributedString: DocumentMapper.attributedString(page).text)
        if !(BlockInfo.textTypes.contains(blocks[0].type)), range.location > para.location {
            made.insert(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]), at: 0)
        }
        edits.replace(range, with: made)
        let r = NSRange(location: range.location, length: made.length)
        textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
        editor.edited(editor.paragraphs(around: r))
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
