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

    init(_ editor: EditorController) { self.editor = editor }

    var hasPendingEdit: Bool { sendTask != nil }

    /// load shows content, as the page opens.
    func load(_ content: DocContent, tinted: Set<String>) {
        meta = content.meta
        let (shown, trailing) = DocumentMapper.attributedString(content, tinted: tinted)
        text.trailing = trailing
        storage.setAttributedString(shown)
        editor.restyleAll()
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
    /// it, the user does.
    func update(_ content: DocContent, tinted: Set<String>, byAgent: Bool) {
        if textView.markedTextRange != nil {
            held = (content, tinted)
            return
        }
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
        let (shown, newTrailing) = DocumentMapper.attributedString(content, tinted: tinted)
        let old = text.paragraphs()
        let new = DocumentMapper.paragraphs(shown, trailing: newTrailing)
        // The blocks the same at either end, by ID, are compared one by
        // one; those between, which came, went or moved, are replaced at
        // once, and only what is replaced is styled again: the whole page
        // laid out again for each edit an agent makes would hold up
        // typing on a long one.
        var head = 0
        while head < old.count, head < new.count, old[head].info.id == new[head].info.id {
            head += 1
        }
        var tail = 0
        while tail < old.count - head, tail < new.count - head,
            old[old.count - 1 - tail].info.id == new[new.count - 1 - tail].info.id
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
        func put(_ range: NSRange, _ t: NSAttributedString, last info: BlockInfo) {
            changed = true
            storage.replaceCharacters(in: range, with: t)
            let moved = t.length - range.length
            for i in styled.indices { styled[i].location += moved }
            styled.append(NSRange(location: range.location, length: t.length))
            // The separator after the paragraph is its block's too.
            let sep = range.location + t.length
            if sep < storage.length {
                storage.addAttribute(
                    .leafletBlock, value: info, range: NSRange(location: sep, length: 1))
            }
        }
        func keep(_ o: DocumentMapper.Paragraph, _ n: DocumentMapper.Paragraph) {
            // A block nested or taken out keeps its node: its depth is
            // where it is in the tree.
            let same =
                o.info.depth == n.info.depth
                && DocumentMapper.node(for: o.info, text: o.text)
                    == DocumentMapper.node(for: n.info, text: n.text)
                && (o.text.length == 0 || n.text.length == 0
                    || (o.text.attribute(.leafletTinted, at: 0, effectiveRange: nil) != nil)
                        == (n.text.attribute(.leafletTinted, at: 0, effectiveRange: nil) != nil))
            if !same { put(o.range, n.text, last: n.info) }
        }
        do {
            storage.beginEditing()
            // From the end, for the ranges before each change to stay.
            for j in (0..<tail).reversed() {
                keep(old[old.count - tail + j], new[new.count - tail + j])
            }
            if !sameBlocks {
                let from = old[head].range.location
                let to = NSMaxRange(old[old.count - tail - 1].range)
                let newFrom = new[head].range.location
                let newTo = NSMaxRange(new[new.count - tail - 1].range)
                put(
                    NSRange(location: from, length: to - from),
                    shown.attributedSubstring(
                        from: NSRange(location: newFrom, length: newTo - newFrom)),
                    last: new[new.count - tail - 1].info)
            }
            for j in (0..<head).reversed() { keep(old[j], new[j]) }
            if changed { text.trailing = newTrailing }
            storage.endEditing()
        }
        // Changes made to the storage itself are no undo steps: the text
        // view records only typing and its own edits. Those steps were
        // made over text that has moved since, which they no longer fit.
        guard changed else { return }
        for r in styled {
            editor.style.apply(to: storage, in: r)
            editor.restyleNumbersAfter(r)
        }
        _ = text.keepTrailing()
        editor.layout.redrawEnd()
        textView.undoManager?.removeAllActions()
        // An agent's run is one step to undo, as on the desktop: the core
        // undoes it, its ops taken back over what came since.
        if byAgent, let undo = textView.undoManager, editor.onUndoRun != nil {
            undo.registerUndo(withTarget: editor) { c in c.onUndoRun?() }
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
        textView.layoutIfNeeded()
        scroll(to: offset)
        DispatchQueue.main.async { [weak self] in self?.scroll(to: offset) }
    }

    /// scroll puts the page at offset, as far as it goes, unless the user
    /// is scrolling it.
    private func scroll(to offset: CGPoint) {
        let tv = textView
        guard !tv.isDragging, !tv.isDecelerating else { return }
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
        let now = ContinuousClock.now
        let since = unsentSince ?? now
        unsentSince = since
        // Text being composed is not the page's yet: it goes once it is.
        guard textView.markedTextRange == nil else { return }
        if sendComposed() { return }
        let wait = min(Self.pause, max(since + Self.maxWait - now, .zero))
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
    func sendComposed() -> Bool {
        guard held != nil, textView.markedTextRange == nil else { return false }
        held = nil
        send()
        return true
    }

    /// send passes the page's content now, as the page closes.
    func send() {
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        if text.uniqueIDs() { editor.layout.redrawEnd() }
        editor.onEdit?(DocumentMapper.content(storage, trailing: text.trailing, meta: meta))
    }
}
