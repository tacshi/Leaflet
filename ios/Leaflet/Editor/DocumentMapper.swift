import UIKit

/// DocumentMapper turns a page's content into the editor's text, and back.
/// Each block is one paragraph, carrying its BlockInfo; blocks nested in
/// lists follow them, deeper. A block without text (an image, a table) is
/// a paragraph of one attachment that keeps the whole block.
enum DocumentMapper {
    // MARK: Content to text

    /// Block is a block as a paragraph of the editor's text has it, and how
    /// deep it is: a block of text without the blocks nested in it, which
    /// are paragraphs of their own after it.
    typealias Block = (node: DocNode, depth: Int)

    /// flatten is the content's blocks in the order of the editor's
    /// paragraphs. A block of text has its children after it, one deeper:
    /// the core gives the page with those of text that holds no blocks
    /// lifted after it (doc.Content.Lifted). A block without text keeps its
    /// own, a table's cells.
    static func flatten(_ content: DocContent) -> [Block] {
        var out: [Block] = []
        func add(_ nodes: [DocNode], depth: Int) {
            for var node in nodes {
                guard BlockInfo.textTypes.contains(node.type) else {
                    out.append((node, depth))
                    continue
                }
                let children = node.children ?? []
                node.children = nil
                out.append((node, depth))
                add(children, depth: depth + 1)
            }
        }
        add(content.blocks, depth: 0)
        return out
    }

    /// attributedString is the content as the editor's text. Each
    /// paragraph's separator carries its block too, which keeps an empty
    /// block's; the last block, when empty, is returned apart, for the
    /// editor to keep in its typing attributes.
    static func attributedString(_ content: DocContent, tinted: Set<String> = [])
        -> (text: NSAttributedString, trailing: BlockInfo?)
    {
        let (text, _, trailing) = attributedString(flatten(content), tinted: tinted)
        return (text, trailing)
    }

    /// attributedString is blocks, as flatten has them, as the editor's
    /// paragraphs, apart by their separators, and each block's BlockInfo,
    /// in order: a part of the page, as a change from outside puts in the
    /// blocks it changed alone. The last block, when empty, is returned
    /// apart too.
    static func attributedString(_ blocks: some Collection<Block>, tinted: Set<String> = [])
        -> (text: NSAttributedString, infos: [BlockInfo], trailing: BlockInfo?)
    {
        let out = NSMutableAttributedString()
        var infos: [BlockInfo] = []
        infos.reserveCapacity(blocks.count)
        var lastEmpty = false
        for (node, depth) in blocks {
            if let last = infos.last {
                out.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: last]))
            }
            let start = out.length
            let (para, info) = paragraph(node, depth: depth)
            out.append(para)
            infos.append(info)
            lastEmpty = para.length == 0
            if tinted.contains(node.id), out.length > start {
                out.addAttribute(
                    .leafletTinted, value: true,
                    range: NSRange(location: start, length: out.length - start))
            }
        }
        return (out, infos, lastEmpty ? infos.last : nil)
    }

    /// paragraph is a block as its paragraph, without the separator, and
    /// its BlockInfo.
    static func paragraph(_ node: DocNode, depth: Int) -> (NSAttributedString, BlockInfo) {
        var data = node.data ?? [:]
        let delta = data.removeValue(forKey: "delta")
        let info = BlockInfo(id: node.id, type: node.type, data: data, depth: depth)
        guard BlockInfo.textTypes.contains(node.type) else {
            let attachment = BlockAttachment(node: node)
            let s = NSMutableAttributedString(attachment: attachment)
            s.addAttribute(
                .leafletBlock, value: info, range: NSRange(location: 0, length: s.length))
            return (s, info)
        }
        let s = NSMutableAttributedString()
        for op in delta?.array ?? [] {
            guard let text = op["insert"]?.string else { continue }
            let shown = lines(text).replacingOccurrences(of: "\n", with: String(lineBreak))
            s.append(NSAttributedString(string: shown, attributes: inline(op["attributes"])))
        }
        s.addAttribute(.leafletBlock, value: info, range: NSRange(location: 0, length: s.length))
        return (s, info)
    }

    /// lines is text with each of the breaks TextKit ends a paragraph at,
    /// "\r\n", "\r", U+2029 and U+0085, made "\n": a block's text from
    /// another app or device may hold them, and shown as they are, they
    /// would split the block in two, one more each time the page is saved.
    static func lines(_ text: String) -> String {
        text.replacingOccurrences(of: "\r\n", with: "\n")
            .replacingOccurrences(of: "\r", with: "\n")
            .replacingOccurrences(of: "\u{2029}", with: "\n")
            .replacingOccurrences(of: "\u{85}", with: "\n")
    }

    static func inline(_ attributes: JSONValue?) -> [NSAttributedString.Key: Any] {
        guard let a = attributes?.object else { return [:] }
        var out: [NSAttributedString.Key: Any] = [:]
        if a["bold"]?.bool == true { out[.leafletBold] = true }
        if a["italic"]?.bool == true { out[.leafletItalic] = true }
        if a["underline"]?.bool == true { out[.leafletUnderline] = true }
        if a["strikethrough"]?.bool == true { out[.leafletStrike] = true }
        if a["code"]?.bool == true { out[.leafletCode] = true }
        if let href = a["href"]?.string, !href.isEmpty { out[.leafletHref] = href }
        if let color = a["color"]?.string { out[.leafletColor] = color }
        if let bg = a["background"]?.string { out[.leafletBackground] = bg }
        if let m = a["mention"]?.object {
            out[.leafletMention] = Mention(
                kind: m["kind"]?.string ?? "", id: m["id"]?.string ?? "",
                name: m["name"]?.string ?? "")
        }
        return out
    }

    // MARK: Text to content

    /// Paragraph is one paragraph of the editor's text: its block and its
    /// text, without the separator.
    struct Paragraph {
        var info: BlockInfo
        var text: NSAttributedString
        var range: NSRange
    }

    /// paragraphs splits the editor's text into its paragraphs. The last,
    /// when empty, is trailing's block; a paragraph without a block gets
    /// one.
    static func paragraphs(_ s: NSAttributedString, trailing: BlockInfo? = nil) -> [Paragraph] {
        blocks(s, trailing: trailing).map {
            Paragraph(info: $0.info, text: s.attributedSubstring(from: $0.range), range: $0.range)
        }
    }

    /// blocks are the paragraphs' blocks and ranges alone, as paragraphs
    /// has them, without copying their text: for what goes through the
    /// whole page as each key is typed.
    static func blocks(_ s: NSAttributedString, trailing: BlockInfo? = nil) -> [(
        info: BlockInfo, range: NSRange
    )] {
        let ns = s.string as NSString
        var out: [(info: BlockInfo, range: NSRange)] = []
        var at = 0
        repeat {
            let range = ns.paragraphRange(for: NSRange(location: at, length: 0))
            var content = range
            if content.length > 0, ns.character(at: NSMaxRange(content) - 1) == 0x0A {
                content.length -= 1
            }
            let info =
                (range.length > 0
                ? s.attribute(.leafletBlock, at: range.location, effectiveRange: nil) : nil)
                as? BlockInfo ?? trailing ?? BlockInfo.paragraph()
            out.append((info, content))
            at = NSMaxRange(range)
        } while at < ns.length
        if ns.length > 0, ns.character(at: ns.length - 1) == 0x0A {
            // Text ending in a separator has an empty paragraph after it.
            out.append((trailing ?? BlockInfo.paragraph(), NSRange(location: ns.length, length: 0)))
        }
        return out
    }

    /// content returns the page's content as the editor's text has it,
    /// with meta as the page last had it: the core stamps the blocks that
    /// changed.
    static func content(_ s: NSAttributedString, trailing: BlockInfo? = nil, meta: JSONValue?)
        -> DocContent
    {
        content(s, blocks: blocks(s, trailing: trailing), meta: meta) { info, range in
            node(for: info, text: s.attributedSubstring(from: range))
        }
    }

    /// content returns the page's content as the editor's text s has it,
    /// its paragraphs as blocks has them, each block as made gives it from
    /// its paragraph's block and text, as node(for:text:) does.
    static func content(
        _ s: NSAttributedString, blocks: [(info: BlockInfo, range: NSRange)], meta: JSONValue?,
        made: (BlockInfo, NSRange) -> DocNode
    ) -> DocContent {
        var roots: [Built] = []
        // chain is the last block that holds blocks at each depth so far,
        // and the depth it is at.
        var chain: [(depth: Int, built: Built)] = []
        var seen = Set<String>()
        for p in blocks {
            var info = p.info
            let node: DocNode
            if seen.contains(info.id) {
                info = info.with(id: BlockInfo.newID())
                node = Self.node(for: info, text: s.attributedSubstring(from: p.range))
            } else {
                node = made(info, p.range)
            }
            seen.insert(info.id)
            let built = Built(node)
            while let top = chain.last, top.depth >= info.depth { chain.removeLast() }
            // A nested block goes in the block before it that holds blocks;
            // one deeper than that can be goes as deep as it can.
            let depth: Int
            if let parent = chain.last {
                parent.built.children.append(built)
                depth = parent.depth + 1
            } else {
                roots.append(built)
                depth = 0
            }
            if BlockInfo.nestable.contains(node.type) { chain.append((depth, built)) }
        }
        return DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: roots.map(\.node)),
            meta: meta)
    }

    /// Built is a block of the content being made, the blocks in it added
    /// as they come and put in it once all are (node): kept as values, each
    /// block added to one would copy those before it, and the blocks it is
    /// in, a page of many blocks in one list as many times over.
    private final class Built {
        private let made: DocNode
        var children: [Built] = []

        init(_ node: DocNode) { made = node }

        var node: DocNode {
            guard !children.isEmpty else { return made }
            var n = made
            n.children = children.map(\.node)
            return n
        }
    }

    static func node(for info: BlockInfo, text: NSAttributedString) -> DocNode {
        if !info.isText {
            if let attachment = text.length > 0
                ? text.attribute(.attachment, at: 0, effectiveRange: nil) as? BlockAttachment : nil
            {
                var n = attachment.node
                n.id = info.id
                // A table's cells and the like stay as they were.
                return n
            }
            // A block without text whose attachment went is a paragraph, as
            // the editor shows it: the block it was is gone.
            return DocNode(
                id: info.id, type: "paragraph", data: ["delta": delta(text)], children: nil)
        }
        var data = info.data
        data["delta"] = delta(text)
        return DocNode(id: info.id, type: info.type, data: data, children: nil)
    }

    /// delta is a paragraph's text as the core's delta JSON.
    static func delta(_ text: NSAttributedString) -> JSONValue {
        var ops: [JSONValue] = []
        text.enumerateAttributes(in: NSRange(location: 0, length: text.length)) { attrs, range, _ in
            var s = (text.string as NSString).substring(with: range)
            s = s.replacingOccurrences(of: String(lineBreak), with: "\n")
                .replacingOccurrences(of: "\u{FFFC}", with: "")
            guard !s.isEmpty else { return }
            var a: [String: JSONValue] = [:]
            if attrs[.leafletBold] as? Bool == true { a["bold"] = .bool(true) }
            if attrs[.leafletItalic] as? Bool == true { a["italic"] = .bool(true) }
            if attrs[.leafletUnderline] as? Bool == true { a["underline"] = .bool(true) }
            if attrs[.leafletStrike] as? Bool == true { a["strikethrough"] = .bool(true) }
            if attrs[.leafletCode] as? Bool == true { a["code"] = .bool(true) }
            if let href = attrs[.leafletHref] as? String { a["href"] = .string(href) }
            if let c = attrs[.leafletColor] as? String { a["color"] = .string(c) }
            if let c = attrs[.leafletBackground] as? String { a["background"] = .string(c) }
            if let m = attrs[.leafletMention] as? Mention {
                a["mention"] = .object([
                    "kind": .string(m.kind), "id": .string(m.id), "name": .string(m.name),
                ])
            }
            var op: [String: JSONValue] = ["insert": .string(s)]
            if !a.isEmpty { op["attributes"] = .object(a) }
            // Runs that read back alike join, as the core's Compact does.
            if case .object(var last) = ops.last ?? .null,
                last["attributes"] == op["attributes"],
                let prev = last["insert"]?.string
            {
                last["insert"] = .string(prev + s)
                ops[ops.count - 1] = .object(last)
            } else {
                ops.append(.object(op))
            }
        }
        return .array(ops)
    }
}
