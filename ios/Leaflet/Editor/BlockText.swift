import UIKit

@MainActor
final class BlockText: NSObject {
    let storage: NSTextStorage
    var trailing: BlockInfo? {
        didSet {
            guard trailing !== oldValue else { return }
            if !lastHoldsItsBlock { index = nil }
            for id in [oldValue?.id, trailing?.id].compactMap(\.self) { touched.insert(id) }
        }
    }
    var emptied: BlockInfo?

    private var lastHoldsItsBlock: Bool {
        let ns = storage.string as NSString
        guard ns.length > 0, ns.character(at: ns.length - 1) != 0x0A else { return false }
        let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
        return blockAt(last.location) != nil
    }
    private var index: [(info: BlockInfo, range: NSRange)]?
    private var stale: (range: NSRange, moved: Int)?
    private(set) var foldChanges = 0
    private(set) var collapsed = 0
    private var touched = Set<String>()

    init(_ storage: NSTextStorage) {
        self.storage = storage
        super.init()
        NotificationCenter.default.addObserver(
            self, selector: #selector(processed),
            name: NSTextStorage.didProcessEditingNotification, object: storage)
    }

    @objc private func processed() {
        let edited = storage.editedRange
        guard edited.location != NSNotFound else {
            index = nil
            return
        }
        touch(edited)
        guard index != nil else { return }
        let moved = storage.changeInLength
        if let was = stale {
            let start = min(was.range.location, edited.location)
            let end = max(NSMaxRange(was.range), NSMaxRange(edited) - moved) + moved
            stale = (NSRange(location: start, length: end - start), was.moved + moved)
        } else {
            stale = (edited, moved)
        }
    }

    private func touch(_ range: NSRange) {
        let ns = storage.string as NSString
        let end = min(NSMaxRange(range), ns.length)
        var at = min(range.location, ns.length)
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            if let info = blockAt(para.location) { touched.insert(info.id) }
            at = NSMaxRange(para)
            if at >= end { break }
        }
        if end >= ns.length, let trailing { touched.insert(trailing.id) }
    }

    func takeTouched() -> Set<String> {
        defer { touched = [] }
        return touched
    }

    var length: Int { storage.length }

    func blocks() -> [(info: BlockInfo, range: NSRange)] {
        if let index, stale == nil { return index }
        if var found = index, let stale, storage.length > 0 {
            index = nil
            let (was, now) = refind(&found, stale)
            if was.count != now.count
                || zip(was, now).contains(where: { !Self.foldAlike($0.info, $1.info) })
            {
                foldChanges += 1
            }
            collapsed += now.count(where: \.info.isCollapsed) - was.count(where: \.info.isCollapsed)
            index = found
        } else {
            let found = DocumentMapper.blocks(storage, trailing: trailing)
            foldChanges += 1
            collapsed = found.count(where: \.info.isCollapsed)
            index = found
        }
        stale = nil
        return index ?? []
    }

    private func refind(
        _ index: inout [(info: BlockInfo, range: NSRange)], _ stale: (range: NSRange, moved: Int)
    ) -> (was: [(info: BlockInfo, range: NSRange)], now: [(info: BlockInfo, range: NSRange)]) {
        let ns = storage.string as NSString
        let from = stale.range.location
        let to = NSMaxRange(stale.range) - stale.moved
        func end(_ i: Int) -> Int { NSMaxRange(index[i].range) + (i < index.count - 1 ? 1 : 0) }
        var lo = 0
        var hi = index.count - 1
        while lo < hi {
            let mid = (lo + hi) / 2
            if end(mid) >= from { hi = mid } else { lo = mid + 1 }
        }
        let first = lo
        lo = first
        hi = index.count - 1
        while lo < hi {
            let mid = (lo + hi + 1) / 2
            if index[mid].range.location <= to { lo = mid } else { hi = mid - 1 }
        }
        let last = lo
        let toEnd = last == index.count - 1
        let start = index[first].range.location
        let stop = toEnd ? ns.length : end(last) + stale.moved
        var now: [(info: BlockInfo, range: NSRange)] = []
        var at = start
        while at < stop {
            let range = ns.paragraphRange(for: NSRange(location: at, length: 0))
            var content = range
            if content.length > 0, ns.character(at: NSMaxRange(content) - 1) == 0x0A {
                content.length -= 1
            }
            let info =
                (range.length > 0 ? blockAt(range.location) : nil) ?? trailing
                ?? BlockInfo.paragraph()
            now.append((info, content))
            at = NSMaxRange(range)
        }
        if toEnd, ns.character(at: ns.length - 1) == 0x0A {
            now.append((trailing ?? BlockInfo.paragraph(), NSRange(location: ns.length, length: 0)))
        }
        let was = Array(index[first...last])
        index.replaceSubrange(first...last, with: now)
        if stale.moved != 0 {
            for i in (first + now.count)..<index.count { index[i].range.location += stale.moved }
        }
        return (was, now)
    }

    private static func foldAlike(_ a: BlockInfo, _ b: BlockInfo) -> Bool {
        a === b
            || a.id == b.id && a.type == b.type && a.depth == b.depth && a.level == b.level
                && a.isCollapsed == b.isCollapsed
    }

    func paragraphs() -> [DocumentMapper.Paragraph] {
        blocks().map {
            DocumentMapper.Paragraph(
                info: $0.info, text: storage.attributedSubstring(from: $0.range), range: $0.range)
        }
    }

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

    func widenToMentions(_ range: NSRange) -> NSRange {
        guard range.length > 0 else { return range }
        var out = range
        if let r = mentionRun(at: range.location) { out = NSUnionRange(out, r) }
        if let r = mentionRun(at: NSMaxRange(range) - 1) { out = NSUnionRange(out, r) }
        return out
    }

    typealias Position = (id: String, offset: Int)

    func position(at location: Int) -> Position {
        let (range, info) = paragraph(at: location)
        return (info.id, location - range.location)
    }

    func place(_ position: Position) -> Int? {
        for p in blocks() where p.info.id == position.id {
            return p.range.location + min(position.offset, p.range.length)
        }
        return nil
    }

    func nearest(_ id: String, in old: [(info: BlockInfo, range: NSRange)]) -> Int? {
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

    static func subtree(_ all: [DocumentMapper.Paragraph], _ first: Int) -> Int {
        var last = first
        while last + 1 < all.count, all[last + 1].info.depth > all[first].info.depth { last += 1 }
        return last
    }

    static func index(_ all: [DocumentMapper.Paragraph], at location: Int) -> Int {
        all.lastIndex { $0.range.location <= location } ?? 0
    }

    @discardableResult
    func normalize(around range: NSRange) -> NSRange {
        let ns = storage.string as NSString
        guard ns.length > 0 else { return NSRange(location: 0, length: 0) }
        let start = ns.paragraphRange(
            for: NSRange(location: min(range.location, ns.length - 1), length: 0))
        let endLoc = min(NSMaxRange(range) + 1, ns.length)
        var at =
            start.location > 0
            ? ns.paragraphRange(for: NSRange(location: start.location - 1, length: 0)).location : 0
        let from = at
        var seen = Set<String>()
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            var info =
                blockAt(para.location) ?? firstBlock(in: para) ?? trailing
                ?? BlockInfo.paragraph()
            if seen.contains(info.id) { info = info.newLine() }
            var content = para
            if ns.character(at: NSMaxRange(para) - 1) == 0x0A { content.length -= 1 }
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
        return NSRange(location: from, length: min(at, ns.length) - from)
    }

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
            if let t = trailing, !t.isText { trailing = t.with(type: "paragraph", data: [:]) }
        }
        emptied = nil
        return trailing !== before
    }

    typealias Fit = (was: BlockInfo, now: BlockInfo)

    func fitDepths(from range: NSRange) -> (changed: [NSRange], trailing: Bool, fitted: [Fit]) {
        let ns = storage.string as NSString
        let before = trailing
        var changed: [NSRange] = []
        var fits: [Fit] = []
        var at =
            ns.paragraphRange(for: NSRange(location: min(range.location, ns.length), length: 0))
            .location
        var room = 0
        if at > 0,
            let prev = blockAt(
                ns.paragraphRange(for: NSRange(location: at - 1, length: 0)).location)
        {
            room = prev.depth + (prev.isList ? 1 : 0)
        }
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            guard let info = blockAt(para.location) else {
                return (changed, trailing !== before, fits)
            }
            var fitted = info
            if info.depth > room {
                fitted = info.with(depth: room)
                storage.addAttribute(.leafletBlock, value: fitted, range: para)
                changed.append(para)
                fits.append((info, fitted))
            } else if para.location > NSMaxRange(range) {
                return (changed, trailing !== before, fits)
            }
            room = fitted.depth + (fitted.isList ? 1 : 0)
            at = NSMaxRange(para)
        }
        if ns.length == 0 || ns.character(at: ns.length - 1) == 0x0A, let t = trailing,
            t.depth > room
        {
            let fitted = t.with(depth: room)
            trailing = fitted
            fits.append((t, fitted))
        }
        return (changed, trailing !== before, fits)
    }

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
