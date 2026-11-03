import LeafletCore
import UIKit

/// EditorClipboard copies and pastes as the desktop's editor does: Copy
/// keeps Leaflet's own copy of what it took, which pastes back as the
/// blocks it was, and puts their Markdown on the clipboard for other apps;
/// text pasted from elsewhere goes in as the blocks its lines say, and a
/// link as its link, its video or its picture.
@MainActor
final class EditorClipboard {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }
    private var edits: TextEdits { editor.edits }

    init(_ editor: EditorController) { self.editor = editor }

    /// Clip is what Copy or Cut last took in an editor, as the desktop's
    /// editor keeps its own copy: the text it left on the clipboard, and
    /// the blocks it stands for, whole or cut to the selection, or the
    /// formatted text of a selection within a block. count is the
    /// clipboard's changeCount once it was there, which another app's copy
    /// since, of the same text maybe, changes.
    struct Clip {
        var text: String?
        var blocks: [DocNode] = []
        var inline: NSAttributedString?
        var count: Int?
    }

    /// clip is Leaflet's own copy, as the app's editors share it.
    private static var clip: Clip?

    /// copied keeps what the system's Copy or Cut takes of the selection
    /// as Leaflet's own copy, before it goes: text within a block, which
    /// pastes back with its formats, or blocks, which go on the clipboard
    /// as their Markdown, as the desktop's Copy puts them, for other apps,
    /// and paste back here as the blocks they were, their kind and nesting
    /// too, which the system's plain text has lost.
    func copied() {
        let range = textView.selectedRange
        guard range.length > 0 else { return }
        let piece = storage.attributedSubstring(from: range)
        if !piece.string.contains("\n"), !piece.string.contains("\u{FFFC}") {
            Self.clip = Clip(text: piece.string, inline: piece)
            return
        }
        let blocks = DocumentMapper.content(piece, meta: nil).document.children ?? []
        let clip = Clip(blocks: blocks)
        Self.clip = clip
        guard let copyText = editor.copyText else { return }
        Task {
            guard let text = await copyText(blocks), !text.isEmpty else { return }
            UIPasteboard.general.string = text
            Self.clip?.text = text
            Self.clip?.count = UIPasteboard.general.changeCount
        }
    }

    /// copiedOut marks the clip as on the clipboard, as the system's Copy
    /// or Cut has put the text within a block there.
    func copiedOut() {
        if Self.clip?.inline != nil { Self.clip?.count = UIPasteboard.general.changeCount }
    }

    /// copyPlainText copies the selection as text alone, without its
    /// formats, as the desktop's Copy as Plain Text: a line per block, for
    /// apps that would paste the formats the system's Copy keeps.
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

    /// pastedCopy pastes Leaflet's own copy, when text is what it left on
    /// the clipboard, and reports whether it did: blocks as the blocks they
    /// were, with IDs of their own; text from within a block with its
    /// formats, in the block it goes in.
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

    /// pasteBlocks puts blocks in place of range: the first joining the
    /// line it goes in, as the desktop pastes them, a block without text
    /// on a line of its own.
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

    /// pasted puts text pasted, or written by dictation, in place of range
    /// as the desktop's editor pastes it (mobile.PasteText), and reports
    /// whether it did: lines as the blocks their Markdown says, the first
    /// joining the line it goes in; a video's or a picture's link on an
    /// empty line as that block; one link on text chosen as its link, and
    /// at the caret as a link to itself. Other text goes in as typed.
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
        return true
    }

    /// liftCards gives each block card pasted or dropped into the page a
    /// paragraph and a block of its own, and returns the range from the
    /// first it moved to the end of the page, or nil when none needed it.
    /// UIKit puts a card where the caret is, in a block of text, which
    /// writes its text without it, so it would be saved as nothing; and a
    /// copy of a card the page has, with the same IDs, its cells' too,
    /// which then get IDs of their own.
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
        // From the end, so the ranges before stay where they are, each a
        // step to undo before the paste: left out, the paste undone would
        // take out what is no longer where it put it.
        for (r, card) in lift.reversed() {
            let ns = storage.string as NSString
            let para = ns.paragraphRange(for: NSRange(location: r.location, length: 0))
            var end = NSMaxRange(para)
            if end > para.location, ns.character(at: end - 1) == 0x0A { end -= 1 }
            let depth =
                (storage.attribute(.leafletBlock, at: r.location, effectiveRange: nil) as? BlockInfo)?
                .depth ?? 0
            // A card moved keeps its IDs; a copy takes new ones.
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

    /// renewed is node with new IDs, its children's too, as a copy of it.
    private static func renewed(_ node: DocNode) -> DocNode {
        var n = node
        n.id = BlockInfo.newID()
        n.children = n.children?.map(renewed)
        return n
    }
}
