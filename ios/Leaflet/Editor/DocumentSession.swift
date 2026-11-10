import UIKit

@MainActor
final class DocumentSession {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private var meta: JSONValue?
    private(set) var updating = false
    private var sendTask: Task<Void, Never>?
    private var unsentSince: ContinuousClock.Instant?
    private static let pause = Duration.milliseconds(250)
    private static let maxWait = Duration.seconds(2)
    private var held: (content: DocContent, tinted: Set<String>)?
    private var shown: [String: Shown] = [:]

    private struct Shown {
        let info: BlockInfo
        var came: DocNode?
        var node: DocNode?
    }

    init(_ editor: EditorController) { self.editor = editor }

    private func kept(_ info: BlockInfo) -> Shown? {
        guard let s = shown[info.id], s.info === info else { return nil }
        return s
    }

    private func node(_ info: BlockInfo, _ range: NSRange) -> DocNode {
        let s = kept(info)
        if let n = s?.node { return n }
        let n = DocumentMapper.node(for: info, text: storage.attributedSubstring(from: range))
        shown[info.id] = Shown(info: info, came: s?.came, node: n)
        return n
    }

    private func same(_ info: BlockInfo, _ range: NSRange, _ n: DocNode) -> Bool {
        if let came = kept(info)?.came { return came == n }
        return node(info, range) == n
    }

    private func forgetTouched() {
        for id in text.takeTouched() { shown[id] = nil }
    }

    var hasPendingEdit: Bool { unsentSince != nil }

    var composing: Bool { textView.markedTextRange != nil }

    func load(_ content: DocContent, tinted: Set<String>) {
        meta = content.meta
        let blocks = DocumentMapper.flatten(content)
        let (page, infos, trailing) = DocumentMapper.attributedString(blocks, tinted: tinted)
        text.trailing = trailing
        storage.setAttributedString(page)
        _ = text.fitDepths(from: NSRange(location: 0, length: storage.length))
        editor.restyleAll()
        shown = [:]
        for (info, block) in zip(infos, blocks) {
            shown[info.id] = Shown(info: info, came: block.node)
        }
        _ = text.takeTouched()
        textView.undoManager?.removeAllActions()
        if storage.length == 0 { text.trailing = text.trailing ?? BlockInfo.paragraph() }
        textView.selectedRange = NSRange(location: 0, length: 0)
        editor.format.updateTypingAttributes()
        editor.layout.updateFolds()
        editor.reportBlank()
        editor.due.schedule()
        DispatchQueue.main.async { [weak editor] in editor?.headings.update() }
    }

    @discardableResult
    func update(_ content: DocContent, tinted: Set<String>, byAgent: Bool, notesOf: String? = nil)
        -> Bool
    {
        if composing {
            held = (content, tinted)
            return false
        }
        let notesBefore = notesOf.flatMap { editor.node(ofBlock: $0)?.data }
        held = nil
        defer { editor.reportBlank() }
        updating = true
        defer { updating = false }
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        meta = content.meta
        let sel = textView.selectedRange
        let start = text.position(at: sel.location)
        let end = text.position(at: NSMaxRange(sel))
        let menu = editor.caretMenu.menu
        let anchor = menu.map { text.position(at: $0.anchor) }
        let offset = textView.contentOffset
        var new = DocumentMapper.flatten(content)
        if new.isEmpty {
            new = [(DocNode(id: BlockInfo.newID(), type: "paragraph", data: [:], children: nil), 0)]
        }
        forgetTouched()
        let old = text.blocks()
        var head = 0
        while head < old.count, head < new.count, old[head].info.id == new[head].node.id {
            head += 1
        }
        var tail = 0
        while tail < old.count - head, tail < new.count - head,
            old[old.count - 1 - tail].info.id == new[new.count - 1 - tail].node.id
        {
            tail += 1
        }
        let sameBlocks = old.count == new.count && head == old.count
        if !sameBlocks, head + tail == old.count || head + tail == new.count {
            if head > 0 { head -= 1 } else { tail -= 1 }
        }
        var changed = false
        var styled: [NSRange] = []
        var next: [String: Shown] = [:]
        var trailing: BlockInfo??
        func put(_ range: NSRange, _ blocks: ArraySlice<DocumentMapper.Block>) {
            changed = true
            let (t, infos, last) = DocumentMapper.attributedString(blocks, tinted: tinted)
            for (info, block) in zip(infos, blocks) {
                next[info.id] = Shown(info: info, came: block.node)
            }
            if blocks.endIndex == new.endIndex { trailing = .some(last) }
            storage.replaceCharacters(in: range, with: t)
            let moved = t.length - range.length
            for i in styled.indices { styled[i].location += moved }
            styled.append(NSRange(location: range.location, length: t.length))
            let sep = range.location + t.length
            if sep < storage.length, let info = infos.last {
                storage.addAttribute(
                    .leafletBlock, value: info, range: NSRange(location: sep, length: 1))
            }
        }
        func keep(_ i: Int, _ j: Int) {
            let o = old[i]
            let n = new[j]
            let same =
                o.info.depth == n.depth && self.same(o.info, o.range, n.node)
                && (o.range.length == 0
                    || (storage.attribute(.leafletTinted, at: o.range.location, effectiveRange: nil)
                        != nil) == tinted.contains(n.node.id))
            if same {
                next[o.info.id] = Shown(info: o.info, came: n.node, node: kept(o.info)?.node)
            } else {
                put(o.range, new[j...j])
            }
        }
        do {
            storage.beginEditing()
            for j in (0..<tail).reversed() {
                keep(old.count - tail + j, new.count - tail + j)
            }
            if !sameBlocks {
                let from = old[head].range.location
                let to = NSMaxRange(old[old.count - tail - 1].range)
                put(NSRange(location: from, length: to - from), new[head..<(new.count - tail)])
            }
            for j in (0..<head).reversed() { keep(j, j) }
            if case .some(let last) = trailing { text.trailing = last }
            storage.endEditing()
        }
        guard changed else {
            shown = next
            _ = text.takeTouched()
            return true
        }
        for r in styled.sorted(by: { $0.location < $1.location }) {
            var span = r
            let fitted = text.fitDepths(from: r)
            for c in fitted.changed { span = NSUnionRange(span, c) }
            editor.style.apply(to: storage, in: span)
            editor.restyleNumbersAfter(span)
        }
        editor.stopPlaying(shown.filter { next[$0.key] == nil }.values.map(\.info))
        shown = next
        _ = text.takeTouched()
        _ = text.keepTrailing()
        editor.layout.redrawEnd()
        textView.undoManager?.removeAllActions()
        if byAgent, let undo = textView.undoManager, editor.onUndoRun != nil {
            undo.registerUndo(withTarget: editor) { c in c.onUndoRun?() }
        }
        if let notesOf, let notesBefore, let undo = textView.undoManager {
            undo.registerUndo(withTarget: editor) { c in c.setData(notesBefore, ofBlock: notesOf) }
        }
        editor.edits.tellKeyboard()
        if let menu, let anchor {
            if let at = text.place(anchor) { menu.anchor = at } else { editor.caretMenu.close() }
        }
        if let s = text.place(start) {
            textView.selectedRange = NSRange(
                location: s, length: max((text.place(end) ?? s) - s, 0))
        } else if let s = text.nearest(start.id, in: old) {
            textView.selectedRange = NSRange(location: s, length: 0)
        }
        updating = false
        editor.caretMenu.update()
        editor.layout.updateFolds()
        editor.format.updateTypingAttributes()
        editor.due.schedule()
        hold(offset)
        return true
    }

    func tint(_ ids: Set<String>) {
        let all = text.blocks()
        storage.beginEditing()
        for p in all where p.range.length > 0 {
            let lit =
                storage.attribute(.leafletTinted, at: p.range.location, effectiveRange: nil) != nil
            let want = ids.contains(p.info.id)
            guard lit != want else { continue }
            if want {
                storage.addAttribute(.leafletTinted, value: true, range: p.range)
            } else {
                storage.removeAttribute(.leafletTinted, range: p.range)
            }
            editor.style.apply(to: storage, in: p.range)
        }
        storage.endEditing()
    }

    private func hold(_ offset: CGPoint) {
        let width = textView.widthChanges
        textView.layoutIfNeeded()
        guard textView.widthChanges == width else { return }
        scroll(to: offset)
        DispatchQueue.main.async { [weak self] in
            guard let self, self.textView.widthChanges == width else { return }
            self.scroll(to: offset)
        }
    }

    private func scroll(to offset: CGPoint) {
        let tv = textView
        guard !tv.isDragging, !tv.isDecelerating, !editor.headings.isJumping else { return }
        let bottom = max(
            -tv.adjustedContentInset.top,
            tv.contentSize.height - tv.bounds.height + tv.adjustedContentInset.bottom)
        tv.setContentOffset(CGPoint(x: offset.x, y: min(offset.y, bottom)), animated: false)
    }

    func edited() {
        sendTask?.cancel()
        sendTask = nil
        unsentSince = unsentSince ?? ContinuousClock.now
        guard !composing else { return }
        if sendComposed() { return }
        sendSoon()
    }

    private func sendSoon() {
        let now = ContinuousClock.now
        let wait = min(Self.pause, max((unsentSince ?? now) + Self.maxWait - now, .zero))
        sendTask = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled else { return }
            self?.send()
        }
    }

    private func sendComposed() -> Bool {
        guard held != nil, !composing else { return false }
        held = nil
        send()
        return true
    }

    func composed() {
        guard !sendComposed(), !composing, sendTask == nil, unsentSince != nil else { return }
        sendSoon()
    }

    func send() {
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        if text.uniqueIDs() { editor.layout.redrawEnd() }
        let before = shown
        forgetTouched()
        var next: [String: Shown] = [:]
        let content = DocumentMapper.content(
            storage, blocks: DocumentMapper.blocks(storage, trailing: text.trailing), meta: meta
        ) { info, range in
            let n = node(info, range)
            next[info.id] = Shown(info: info, came: kept(info)?.came, node: n)
            return n
        }
        editor.stopPlaying(before.filter { next[$0.key] == nil }.values.map(\.info))
        shown = next
        editor.onEdit?(content)
    }
}
