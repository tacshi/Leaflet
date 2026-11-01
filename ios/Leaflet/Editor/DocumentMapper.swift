import UIKit

/// DocumentMapper turns a page's content into the editor's text, and back.
/// Each block is one paragraph, carrying its BlockInfo; blocks nested in
/// lists follow them, deeper. A block without text (an image, a table) is
/// a paragraph of one attachment that keeps the whole block.
enum DocumentMapper {
    // MARK: Content to text

    /// attributedString is the content as the editor's text. Each
    /// paragraph's separator carries its block too, which keeps an empty
    /// block's; the last block, when empty, is returned apart, for the
    /// editor to keep in its typing attributes.
    static func attributedString(_ content: DocContent, tinted: Set<String> = [])
        -> (text: NSAttributedString, trailing: BlockInfo?)
    {
        let out = NSMutableAttributedString()
        var last: BlockInfo?
        var lastEmpty = false
        func add(_ nodes: [DocNode], depth: Int) {
            for node in nodes {
                if let last {
                    out.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: last]))
                }
                let start = out.length
                let (para, info) = paragraph(node, depth: depth)
                out.append(para)
                last = info
                lastEmpty = para.length == 0
                if tinted.contains(node.id), out.length > start {
                    out.addAttribute(
                        .leafletTinted, value: true,
                        range: NSRange(location: start, length: out.length - start))
                }
                if BlockInfo.nestable.contains(node.type) {
                    add(node.children ?? [], depth: depth + 1)
                }
            }
        }
        add(content.blocks, depth: 0)
        return (out, lastEmpty ? last : nil)
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
            let shown = text.replacingOccurrences(of: "\n", with: String(lineBreak))
            s.append(NSAttributedString(string: shown, attributes: inline(op["attributes"])))
        }
        s.addAttribute(.leafletBlock, value: info, range: NSRange(location: 0, length: s.length))
        return (s, info)
    }

    static func inline(_ attributes: JSONValue?) -> [NSAttributedString.Key: Any] {
        guard let a = attributes?.object else { return [:] }
        var out: [NSAttributedString.Key: Any] = [:]
        if a["bold"]?.bool == true { out[.leafletBold] = true }
        if a["italic"]?.bool == true { out[.leafletItalic] = true }
        if a["underline"]?.bool == true { out[.underlineStyle] = NSUnderlineStyle.single.rawValue }
        if a["strikethrough"]?.bool == true {
            out[.strikethroughStyle] = NSUnderlineStyle.single.rawValue
        }
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
        let ns = s.string as NSString
        var out: [Paragraph] = []
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
            out.append(
                Paragraph(info: info, text: s.attributedSubstring(from: content), range: content))
            at = NSMaxRange(range)
        } while at < ns.length
        if ns.length > 0, ns.character(at: ns.length - 1) == 0x0A {
            // Text ending in a separator has an empty paragraph after it.
            out.append(
                Paragraph(
                    info: trailing ?? BlockInfo.paragraph(), text: NSAttributedString(),
                    range: NSRange(location: ns.length, length: 0)))
        }
        return out
    }

    /// content returns the page's content as the editor's text has it,
    /// with meta as the page last had it: the core stamps the blocks that
    /// changed.
    static func content(_ s: NSAttributedString, trailing: BlockInfo? = nil, meta: JSONValue?)
        -> DocContent
    {
        var roots: [DocNode] = []
        // chain is the path to the last block at each depth so far.
        var chain: [(depth: Int, path: [Int])] = []
        var seen = Set<String>()
        for p in paragraphs(s, trailing: trailing) {
            var info = p.info
            if seen.contains(info.id) { info = info.with(id: BlockInfo.newID()) }
            seen.insert(info.id)
            let node = node(for: info, text: p.text)
            while let top = chain.last, top.depth >= info.depth { chain.removeLast() }
            // A nested block goes in the block before it that holds blocks;
            // one deeper than that can be goes as deep as it can.
            if let parent = chain.last, BlockInfo.nestable.contains(nodeAt(roots, parent.path).type)
            {
                insert(node, into: &roots, at: parent.path)
                let index = (nodeAt(roots, parent.path).children?.count ?? 1) - 1
                chain.append((parent.depth + 1, parent.path + [index]))
            } else {
                roots.append(node)
                chain = [(0, [roots.count - 1])]
            }
        }
        return DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: roots), meta: meta)
    }

    private static func nodeAt(_ roots: [DocNode], _ path: [Int]) -> DocNode {
        var n = roots[path[0]]
        for i in path.dropFirst() { n = n.children![i] }
        return n
    }

    private static func insert(_ child: DocNode, into roots: inout [DocNode], at path: [Int]) {
        func go(_ nodes: inout [DocNode], _ rest: ArraySlice<Int>) {
            let i = rest.first!
            if rest.count == 1 {
                nodes[i].children = (nodes[i].children ?? []) + [child]
            } else {
                var kids = nodes[i].children ?? []
                go(&kids, rest.dropFirst())
                nodes[i].children = kids
            }
        }
        go(&roots, path[...])
    }

    static func node(for info: BlockInfo, text: NSAttributedString) -> DocNode {
        if !info.isText,
            let attachment = text.length > 0
                ? text.attribute(.attachment, at: 0, effectiveRange: nil) as? BlockAttachment : nil
        {
            var n = attachment.node
            n.id = info.id
            // A table's cells and the like stay as they were.
            return n
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
            if attrs[.underlineStyle] != nil { a["underline"] = .bool(true) }
            if attrs[.strikethroughStyle] != nil { a["strikethrough"] = .bool(true) }
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
