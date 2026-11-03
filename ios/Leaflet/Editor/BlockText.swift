import UIKit

/// BlockText is the page's text as its blocks: the text view's storage, a
/// paragraph a block, each carrying its BlockInfo, and the block of the
/// last paragraph, which no character holds once it is empty. It finds
/// the paragraphs and their blocks, keeping an index of them while the
/// text stays as it is, and keeps each paragraph one block as the text
/// changes.
@MainActor
final class BlockText: NSObject {
    let storage: NSTextStorage
    /// trailing is the block of the last paragraph, which no character
    /// holds when it is empty. It is kept while the paragraph has text too
    /// (keepTrailing), and counts only once it is empty.
    var trailing: BlockInfo? {
        didSet { if trailing !== oldValue { index = nil } }
    }
    /// emptied is the block of the page's last paragraph, as an edit
    /// empties it, for the trailing block to keep.
    var emptied: BlockInfo?
    /// index is the paragraphs' blocks and ranges, as blocks() last found
    /// them, until the text changes.
    private var index: [(info: BlockInfo, range: NSRange)]?

    init(_ storage: NSTextStorage) {
        self.storage = storage
        super.init()
        // Every change of the text, its characters or its attributes, the
        // blocks' among them, is processed as it ends, whoever made it:
        // the keyboard, an undo, the editor.
        NotificationCenter.default.addObserver(
            self, selector: #selector(processed),
            name: NSTextStorage.didProcessEditingNotification, object: storage)
    }

    @objc private func processed() { index = nil }

    var length: Int { storage.length }

    /// blocks are the paragraphs' blocks and ranges, without their
    /// separators, as DocumentMapper.blocks has them.
    func blocks() -> [(info: BlockInfo, range: NSRange)] {
        if let index { return index }
        let found = DocumentMapper.blocks(storage, trailing: trailing)
        index = found
        return found
    }

    /// paragraphs are the paragraphs with their text, as
    /// DocumentMapper.paragraphs has them.
    func paragraphs() -> [DocumentMapper.Paragraph] {
        blocks().map {
            DocumentMapper.Paragraph(
                info: $0.info, text: storage.attributedSubstring(from: $0.range), range: $0.range)
        }
    }

    /// paragraph returns the paragraph at location: its text's range,
    /// without the separator, and its block.
    func paragraph(at location: Int) -> (range: NSRange, info: BlockInfo) {
        let ns = storage.string as NSString
        let loc = min(max(location, 0), ns.length)
        var range = ns.paragraphRange(for: NSRange(location: loc, length: 0))
        if loc == ns.length, ns.length > 0, ns.character(at: ns.length - 1) == 0x0A {
            range = NSRange(location: ns.length, length: 0)
        }
        var content = range
        if content.length > 0, ns.character(at: NSMaxRange(content) - 1) == 0x0A {
            content.length -= 1
        }
        let info = range.length > 0 ? blockAt(range.location) : nil
        return (content, info ?? trailing ?? BlockInfo.paragraph())
    }

    /// paragraphs(touching:) are the paragraphs range touches: one it ends
    /// at the start of, it does not.
    func paragraphs(touching sel: NSRange) -> [(range: NSRange, info: BlockInfo)] {
        var out: [(NSRange, BlockInfo)] = []
        var at = sel.location
        repeat {
            let p = paragraph(at: at)
            out.append(p)
            at = NSMaxRange(p.range) + 1
        } while at < NSMaxRange(sel) && at <= storage.length && sel.length > 0
        return out
    }

    func blockAt(_ location: Int) -> BlockInfo? {
        guard location < storage.length else { return nil }
        return storage.attribute(.leafletBlock, at: location, effectiveRange: nil) as? BlockInfo
    }

    /// firstBlock is the first block a character in range holds.
    func firstBlock(in range: NSRange) -> BlockInfo? {
        var found: BlockInfo?
        storage.enumerateAttribute(.leafletBlock, in: range) { v, _, stop in
            if let b = v as? BlockInfo {
                found = b
                stop.pointee = true
            }
        }
        return found
    }

    /// location is where the paragraph of block info is, or nil once no
    /// paragraph is that block.
    func location(of info: BlockInfo) -> Int? {
        let ns = storage.string as NSString
        if trailing === info, ns.length == 0 || ns.character(at: ns.length - 1) == 0x0A {
            return ns.length
        }
        var found: Int?
        storage.enumerateAttribute(.leafletBlock, in: NSRange(location: 0, length: ns.length)) {
            v, r, stop in
            if (v as? BlockInfo) === info {
                found = r.location
                stop.pointee = true
            }
        }
        return found
    }

    /// mentionRun is the whole of the mention the character at location
    /// is part of, or nil.
    func mentionRun(at location: Int) -> NSRange? {
        guard location < storage.length else { return nil }
        var run = NSRange()
        guard
            storage.attribute(
                .leafletMention, at: location, longestEffectiveRange: &run,
                in: NSRange(location: 0, length: storage.length)) != nil
        else { return nil }
        return run
    }

    /// widenToMentions widens range to the whole of each mention it cuts,
    /// as doc.Delta.Widen does: a mention is taken out or formatted whole.
    func widenToMentions(_ range: NSRange) -> NSRange {
        guard range.length > 0 else { return range }
        var out = range
        if let r = mentionRun(at: range.location) { out = NSUnionRange(out, r) }
        if let r = mentionRun(at: NSMaxRange(range) - 1) { out = NSUnionRange(out, r) }
        return out
    }

    // MARK: Places that stay

    /// Position is a place in the text as its block and the offset in it,
    /// which stays the same place as the text around the block changes.
    typealias Position = (id: String, offset: Int)

    func position(at location: Int) -> Position {
        let (range, info) = paragraph(at: location)
        return (info.id, location - range.location)
    }

    /// place is where position is in the text now, or nil once its block
    /// is gone.
    func place(_ position: Position) -> Int? {
        for p in blocks() where p.info.id == position.id {
            return p.range.location + min(position.offset, p.range.length)
        }
        return nil
    }

    /// nearest is where the caret goes once a change from outside took
    /// away its block, id, which was in the paragraphs before it, old: the
    /// end of the nearest block before it that stayed, or else the start
    /// of the nearest after it, or else the start of the page. Left to
    /// the text view, the caret would go where the change ended, which may
    /// be far from where the user was.
    func nearest(_ id: String, in old: [DocumentMapper.Paragraph]) -> Int? {
        guard let i = old.firstIndex(where: { $0.info.id == id }) else { return nil }
        var now: [String: NSRange] = [:]
        for p in blocks() where now[p.info.id] == nil {
            now[p.info.id] = p.range
        }
        for p in old[..<i].reversed() {
            if let r = now[p.info.id] { return NSMaxRange(r) }
        }
        for p in old[(i + 1)...] {
            if let r = now[p.info.id] { return r.location }
        }
        return 0
    }

    /// subtree is the last of the paragraphs from first that its block
    /// holds, nested in it: the block moves with them.
    static func subtree(_ all: [DocumentMapper.Paragraph], _ first: Int) -> Int {
        var last = first
        while last + 1 < all.count, all[last + 1].info.depth > all[first].info.depth { last += 1 }
        return last
    }

    /// index is the paragraph of all that location is in.
    static func index(_ all: [DocumentMapper.Paragraph], at location: Int) -> Int {
        all.lastIndex { $0.range.location <= location } ?? 0
    }

    // MARK: One block a paragraph

    /// normalize keeps each paragraph in and near range one block: the
    /// block of its first character, and one apart from the paragraphs
    /// before it, as lines pasted in all take the block they went in.
    func normalize(around range: NSRange) {
        let ns = storage.string as NSString
        guard ns.length > 0 else { return }
        let start = ns.paragraphRange(
            for: NSRange(location: min(range.location, ns.length - 1), length: 0))
        let endLoc = min(NSMaxRange(range) + 1, ns.length)
        var at =
            start.location > 0
            ? ns.paragraphRange(for: NSRange(location: start.location - 1, length: 0)).location : 0
        var seen = Set<String>()
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            // Text typed into an empty block may have no block yet: the
            // rest of the paragraph, its separator, or the trailing block
            // holds the paragraph's.
            var info =
                blockAt(para.location) ?? firstBlock(in: para) ?? trailing
                ?? BlockInfo.paragraph()
            if seen.contains(info.id) { info = info.newLine() }
            var content = para
            if ns.character(at: NSMaxRange(para) - 1) == 0x0A { content.length -= 1 }
            // A block without text whose attachment went is an empty
            // paragraph.
            if !info.isText,
                content.length == 0
                    || storage.attribute(.attachment, at: para.location, effectiveRange: nil) == nil
            {
                info = info.with(type: "paragraph", data: [:])
            }
            var differs = false
            storage.enumerateAttribute(.leafletBlock, in: para) { v, _, stop in
                if (v as? BlockInfo) !== info {
                    differs = true
                    stop.pointee = true
                }
            }
            if differs { storage.addAttribute(.leafletBlock, value: info, range: para) }
            seen.insert(info.id)
            at = NSMaxRange(para)
            if at > endLoc && !differs { break }
        }
        if at >= ns.length, let trailing, seen.contains(trailing.id),
            ns.character(at: ns.length - 1) == 0x0A
        {
            self.trailing = trailing.newLine()
        }
    }

    /// keepTrailing keeps the page's last paragraph a block of its own,
    /// and reports whether its block changed. Once empty, no character
    /// holds it, and the trailing block does: the last paragraph's block,
    /// kept while it has text too, for an edit the editor isn't told of,
    /// as the text view's own undo, to leave it that block; or, as an edit
    /// empties it, the block it was then.
    func keepTrailing() -> Bool {
        let ns = storage.string as NSString
        let before = trailing
        if ns.length > 0, ns.character(at: ns.length - 1) != 0x0A {
            let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
            trailing = blockAt(last.location) ?? trailing
        } else {
            if let emptied {
                trailing = emptied
            } else if trailing == nil {
                trailing =
                    ns.length > 0 ? blockAt(ns.length - 1)?.newLine() : BlockInfo.paragraph()
            }
            // An empty block without text, its attachment gone, as the
            // page's last image deleted, is an empty paragraph, as
            // normalize has it: it would be saved as the block it was.
            if let t = trailing, !t.isText { trailing = t.with(type: "paragraph", data: [:]) }
        }
        emptied = nil
        return trailing !== before
    }

    /// fitDepths keeps each block as deep as the page's content has it, as
    /// DocumentMapper.content reads the text: a block goes in the block
    /// before it that holds blocks, one deeper, or at the top. A block
    /// shown deeper, as one whose list item became text, would be saved
    /// where it shows not. It returns the paragraphs it changed, and
    /// whether the trailing block changed.
    func fitDepths() -> (changed: [NSRange], trailing: Bool) {
        let ns = storage.string as NSString
        let before = trailing
        var holders: [Int] = []
        var at = 0
        var changed: [NSRange] = []
        repeat {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            let last = para.length == 0 || NSMaxRange(para) == ns.length
            guard let info = para.length > 0 ? blockAt(para.location) : trailing else { break }
            while let top = holders.last, top >= info.depth { holders.removeLast() }
            let depth = holders.last.map { $0 + 1 } ?? 0
            var fitted = info
            if depth != info.depth {
                fitted = info.with(depth: depth)
                if para.length > 0 {
                    storage.addAttribute(.leafletBlock, value: fitted, range: para)
                    changed.append(para)
                } else {
                    trailing = fitted
                }
            }
            if fitted.isList { holders.append(depth) }
            at = NSMaxRange(para)
            // The empty paragraph after the last separator is the
            // trailing block's.
            if last, para.length > 0, ns.character(at: ns.length - 1) == 0x0A,
                let t = trailing
            {
                while let top = holders.last, top >= t.depth { holders.removeLast() }
                let d = holders.last.map { $0 + 1 } ?? 0
                if d != t.depth { trailing = t.with(depth: d) }
            }
        } while at < ns.length
        return (changed, trailing !== before)
    }

    /// uniqueIDs gives a block whose ID a block before it has a new one, in
    /// the text, and reports whether the trailing block was one:
    /// DocumentMapper.content would give it another each time the page is
    /// sent, a new block to the core each time.
    func uniqueIDs() -> Bool {
        var seen = Set<String>()
        var trailingChanged = false
        for p in blocks() {
            guard seen.contains(p.info.id) else {
                seen.insert(p.info.id)
                continue
            }
            let info = p.info.with(id: BlockInfo.newID())
            seen.insert(info.id)
            // The separator after the paragraph is its block's too.
            var whole = p.range
            if NSMaxRange(whole) < storage.length { whole.length += 1 }
            if whole.length > 0 {
                storage.addAttribute(.leafletBlock, value: info, range: whole)
            } else {
                trailing = info
                trailingChanged = true
            }
        }
        return trailingChanged
    }
}
