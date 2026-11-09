import UIKit

/// DocumentSession passes the page's content between the editor and the
/// core: the content as the page opens, the page's content to the core
/// shortly after each edit, and the core's changes from sync and agents
/// into the text, without losing the caret or the page's place.
@MainActor
final class DocumentSession {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    /// meta is the page's meta as the core last sent it.
    private var meta: JSONValue?
    /// updating says a change from outside is going in: the selection it
    /// moves on the way says nothing of the menu at the caret, whose "/" or
    /// "@" moves with it once it is in.
    private(set) var updating = false
    private var sendTask: Task<Void, Never>?
    /// unsentSince is when the first edit not yet sent was made: typing
    /// that never pauses is still sent, every maxWait.
    private var unsentSince: ContinuousClock.Instant?
    /// pause is how long after the last edit the page is sent, and maxWait
    /// the longest an edit waits while typing goes on: a crash in a long
    /// run of fast typing loses no more.
    private static let pause = Duration.milliseconds(250)
    private static let maxWait = Duration.seconds(2)
    /// held is a change from outside that came while text was being
    /// composed, as Japanese or Chinese is typed: put in then, it would
    /// commit the composition, or move it under the input method. Once the
    /// composition ends, what was typed goes to the core, which merges it
    /// with the change and sends the merge (sendComposed).
    private var held: (content: DocContent, tinted: Set<String>)?
    /// shown are the page's blocks, by ID, as they were last loaded, put
    /// in or sent, each with the BlockInfo its paragraph had: one whose
    /// paragraph changed since is not among them (BlockText.takeTouched).
    /// A change from outside is compared with them, and a send takes them,
    /// rather than reading each block from the text again: on a long page,
    /// that is most of the work.
    private var shown: [String: Shown] = [:]

    private struct Shown {
        let info: BlockInfo
        /// came is the block as it came from outside, which its paragraph
        /// was made from, or nil; node is the block as the text has it,
        /// once read from it, as a send gives it. The text may have it
        /// otherwise than it came: a line break of another kind, a format
        /// it doesn't show.
        var came: DocNode?
        var node: DocNode?
    }

    init(_ editor: EditorController) { self.editor = editor }

    /// kept is what shown keeps of the block of info, while its paragraph
    /// has that block.
    private func kept(_ info: BlockInfo) -> Shown? {
        guard let s = shown[info.id], s.info === info else { return nil }
        return s
    }

    /// node is the block of the paragraph at range, of block info, as the
    /// text has it: as shown keeps it while it is as it was.
    private func node(_ info: BlockInfo, _ range: NSRange) -> DocNode {
        let s = kept(info)
        if let n = s?.node { return n }
        let n = DocumentMapper.node(for: info, text: storage.attributedSubstring(from: range))
        shown[info.id] = Shown(info: info, came: s?.came, node: n)
        return n
    }

    /// same says the paragraph at range, of block info, is the block n: as
    /// it came from outside, if it did, else as the text has it.
    private func same(_ info: BlockInfo, _ range: NSRange, _ n: DocNode) -> Bool {
        if let came = kept(info)?.came { return came == n }
        return node(info, range) == n
    }

    /// forgetTouched drops from shown the blocks whose paragraphs changed
    /// since they were kept.
    private func forgetTouched() {
        for id in text.takeTouched() { shown[id] = nil }
    }

    /// hasPendingEdit says what was typed has not gone to the core yet,
    /// whether it goes soon or waits for a composition: a page closing or
    /// the app leaving the screen sends it all the same.
    var hasPendingEdit: Bool { unsentSince != nil }

    /// composing says text is being composed, as Japanese or Chinese is
    /// typed, which a change from outside waits for.
    var composing: Bool { textView.markedTextRange != nil }

    /// load shows content, as the page opens.
    func load(_ content: DocContent, tinted: Set<String>) {
        meta = content.meta
        let blocks = DocumentMapper.flatten(content)
        let (page, infos, trailing) = DocumentMapper.attributedString(blocks, tinted: tinted)
        text.trailing = trailing
        storage.setAttributedString(page)
        // Each block as deep as it can be, as each change keeps them after.
        _ = text.fitDepths(from: NSRange(location: 0, length: storage.length))
        editor.restyleAll()
        shown = [:]
        for (info, block) in zip(infos, blocks) {
            shown[info.id] = Shown(info: info, came: block.node)
        }
        _ = text.takeTouched()
        // The page as it opens is no step to undo.
        textView.undoManager?.removeAllActions()
        if storage.length == 0 { text.trailing = text.trailing ?? BlockInfo.paragraph() }
        textView.selectedRange = NSRange(location: 0, length: 0)
        editor.format.updateTypingAttributes()
        editor.layout.updateFolds()
        editor.reportBlank()
        editor.due.schedule()
        DispatchQueue.main.async { [weak editor] in editor?.headings.update() }
    }

    /// update brings content from outside the editor (sync, an agent, a
    /// merge) into the text: only the blocks that changed, where the
    /// blocks are the same; all of it otherwise. The caret stays in its
    /// block, and the page where it is: an agent writing does not scroll
    /// it, the user does. Each block is compared as a node with the one
    /// the text has (shown), and only those that changed are made into
    /// text: an agent writing in a long page changes a block or two of it
    /// each time. It reports whether content went in: a composition holds
    /// it (held), and the editor goes on showing what it showed, over
    /// which the edits it sends are made.
    @discardableResult
    func update(_ content: DocContent, tinted: Set<String>, byAgent: Bool, notesOf: String? = nil)
        -> Bool
    {
        if composing {
            held = (content, tinted)
            return false
        }
        // A transcript is one step to undo, back to the notes it replaced.
        let notesBefore = notesOf.flatMap { editor.node(ofBlock: $0)?.data }
        held = nil
        defer { editor.reportBlank() }
        updating = true
        defer { updating = false }
        // What was typed is in content, or given up: the page can't be
        // changed. Nothing waits to be sent.
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        meta = content.meta
        // The selection, and the menu's "/" or "@", as places in their
        // blocks, which keep them as the text around them changes.
        let sel = textView.selectedRange
        let start = text.position(at: sel.location)
        let end = text.position(at: NSMaxRange(sel))
        let menu = editor.caretMenu.menu
        let anchor = menu.map { text.position(at: $0.anchor) }
        let offset = textView.contentOffset
        var new = DocumentMapper.flatten(content)
        // A page of no blocks shows one empty paragraph, as its text does.
        if new.isEmpty {
            new = [(DocNode(id: BlockInfo.newID(), type: "paragraph", data: [:], children: nil), 0)]
        }
        forgetTouched()
        let old = text.blocks()
        // The blocks the same at either end, by ID, are compared one by
        // one; those between, which came, went or moved, are replaced at
        // once, and only what is replaced is styled again: the whole page
        // laid out again for each edit an agent makes would hold up
        // typing on a long one.
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
        // A block that only came or went is replaced with the one before
        // it, or after, for neither side of the change to be empty.
        if !sameBlocks, head + tail == old.count || head + tail == new.count {
            if head > 0 { head -= 1 } else { tail -= 1 }
        }
        var changed = false
        // styled are where the text replaced now is, as each replacement
        // before it moves it.
        var styled: [NSRange] = []
        // next is shown as the text will have it: the blocks kept with the
        // BlockInfo they have in it, those put in with their new ones.
        var next: [String: Shown] = [:]
        // trailing is the last block, when its paragraph is put in empty.
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
            // The separator after the paragraph is its block's too.
            let sep = range.location + t.length
            if sep < storage.length, let info = infos.last {
                storage.addAttribute(
                    .leafletBlock, value: info, range: NSRange(location: sep, length: 1))
            }
        }
        func keep(_ i: Int, _ j: Int) {
            let o = old[i]
            let n = new[j]
            // A block nested or taken out keeps its node: its depth is
            // where it is in the tree.
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
            // From the end, for the ranges before each change to stay.
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
        // Changes made to the storage itself are no undo steps: the text
        // view records only typing and its own edits. Those steps were
        // made over text that has moved since, which they no longer fit.
        guard changed else {
            shown = next
            _ = text.takeTouched()
            return true
        }
        // In the page's order, for each item's number to go on from those
        // before it as they now are.
        for r in styled.sorted(by: { $0.location < $1.location }) {
            var span = r
            let fitted = text.fitDepths(from: r)
            for c in fitted.changed { span = NSUnionRange(span, c) }
            editor.style.apply(to: storage, in: span)
            editor.restyleNumbersAfter(span)
        }
        // A block another device deleted stops being heard here too.
        editor.stopPlaying(shown.filter { next[$0.key] == nil }.values.map(\.info))
        shown = next
        _ = text.takeTouched()
        _ = text.keepTrailing()
        editor.layout.redrawEnd()
        textView.undoManager?.removeAllActions()
        // An agent's run is one step to undo, as on the desktop: the core
        // undoes it, its ops taken back over what came since.
        if byAgent, let undo = textView.undoManager, editor.onUndoRun != nil {
            undo.registerUndo(withTarget: editor) { c in c.onUndoRun?() }
        }
        if let notesOf, let notesBefore, let undo = textView.undoManager {
            undo.registerUndo(withTarget: editor) { c in c.setData(notesBefore, ofBlock: notesOf) }
        }
        editor.edits.tellKeyboard()
        if let menu, let anchor {
            // The menu goes on from its "/" or "@" where it now is, or
            // closes once it is gone.
            if let at = text.place(anchor) { menu.anchor = at } else { editor.caretMenu.close() }
        }
        if let s = text.place(start) {
            textView.selectedRange = NSRange(
                location: s, length: max((text.place(end) ?? s) - s, 0))
        } else if let s = text.nearest(start.id, in: old) {
            textView.selectedRange = NSRange(location: s, length: 0)
        }
        // The selection back where it was, the menu goes by it.
        updating = false
        editor.caretMenu.update()
        editor.layout.updateFolds()
        editor.format.updateTypingAttributes()
        editor.due.schedule()
        hold(offset)
        return true
    }

    /// tint shows the blocks an agent run last changed, ids, lit, or no
    /// more: a run's last tint comes after its last change, and an undo
    /// takes it away without one.
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

    /// hold keeps the page scrolled to offset after a change from outside,
    /// which the text view follows by scrolling to the caret, then and
    /// once laid out. A page the user is scrolling goes where they take it.
    private func hold(_ offset: CGPoint) {
        // At another width the same scroll is other text, and the text
        // view keeps the page where it was itself.
        let width = textView.widthChanges
        textView.layoutIfNeeded()
        guard textView.widthChanges == width else { return }
        scroll(to: offset)
        DispatchQueue.main.async { [weak self] in
            guard let self, self.textView.widthChanges == width else { return }
            self.scroll(to: offset)
        }
    }

    /// scroll puts the page at offset, as far as it goes, unless the user
    /// is scrolling it, or it is on its way to a block: put back, it would
    /// never land there.
    private func scroll(to offset: CGPoint) {
        let tv = textView
        guard !tv.isDragging, !tv.isDecelerating, !editor.headings.isJumping else { return }
        let bottom = max(
            -tv.adjustedContentInset.top,
            tv.contentSize.height - tv.bounds.height + tv.adjustedContentInset.bottom)
        tv.setContentOffset(CGPoint(x: offset.x, y: min(offset.y, bottom)), animated: false)
    }

    /// edited sends the page soon after an edit, or at once when a change
    /// from outside waited for the composition the edit ended.
    func edited() {
        sendTask?.cancel()
        sendTask = nil
        unsentSince = unsentSince ?? ContinuousClock.now
        // Text being composed is not the page's yet: it goes once it is.
        guard !composing else { return }
        if sendComposed() { return }
        sendSoon()
    }

    /// sendSoon sends the page after the pause, or sooner when an edit has
    /// waited nearly maxWait already.
    private func sendSoon() {
        let now = ContinuousClock.now
        let wait = min(Self.pause, max((unsentSince ?? now) + Self.maxWait - now, .zero))
        sendTask = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled else { return }
            self?.send()
        }
    }

    /// sendComposed sends the page at once when a change from outside
    /// waited for a composition that has ended (held), and reports whether
    /// it did: the core merges what was typed with the change, and the
    /// editor shows the merge as it comes.
    private func sendComposed() -> Bool {
        guard held != nil, !composing else { return false }
        held = nil
        send()
        return true
    }

    /// composed follows a composition that may have ended with no change
    /// of the text, as the caret moves away from it or the keyboard
    /// confirms it: no edit comes then to send what it kept back.
    func composed() {
        guard !sendComposed(), !composing, sendTask == nil, unsentSince != nil else { return }
        sendSoon()
    }

    /// send passes the page's content now, as the page closes: each block
    /// as shown keeps it, unless its paragraph changed since. The text's
    /// paragraphs are found again, all of them: what the page saves is not
    /// left to an index kept as it changed.
    func send() {
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        if text.uniqueIDs() { editor.layout.redrawEnd() }
        // Before a block that went is forgotten as touched.
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
