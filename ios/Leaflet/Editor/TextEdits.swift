import UIKit

/// TextEdits puts the editor's own changes in the text, the rules', the
/// commands' and the menus', as steps to undo. They all go in through one
/// path (put), which keeps the text view's own steps, as typing, to undo
/// with them, and the blocks as they were.
@MainActor
final class TextEdits {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    /// bypass lets the editor's own insertions through the rules; putting
    /// says text going in has its blocks given after it, which the text
    /// view's change takes nothing from (put).
    var bypass = false
    private(set) var putting = false
    /// inEdit counts the text view's own edits under way, the keyboard's,
    /// which the rules change the text in: the text view takes changes to
    /// the text then as part of its edit (put).
    var inEdit = 0

    init(_ editor: EditorController) { self.editor = editor }

    /// undoing says the text view's undo or redo is under way, which puts
    /// back what was.
    var undoing: Bool {
        textView.undoManager?.isUndoing == true || textView.undoManager?.isRedoing == true
    }

    func insert(_ s: String, replacing range: NSRange) {
        // Outside the text view's edits, as a command or a tap puts text
        // in, a step of the editor's own, which keeps the blocks as they
        // were to undo and redo (put); the text view's would not.
        if inEdit == 0, !undoing {
            replace(
                range, with: NSAttributedString(string: s, attributes: textView.typingAttributes))
            editor.textViewDidChange(textView)
            return
        }
        bypass = true
        if let r = textRange(range) { textView.replace(r, withText: s) }
        bypass = false
    }

    func delete(_ range: NSRange) { insert("", replacing: range) }

    func textRange(_ r: NSRange) -> UITextRange? {
        guard let s = textView.position(from: textView.beginningOfDocument, offset: r.location),
            let e = textView.position(from: s, offset: r.length)
        else { return nil }
        return textView.textRange(from: s, to: e)
    }

    /// insertParagraph puts a new block after the paragraph para, with
    /// text, and the caret after its text.
    func insertParagraph(after para: NSRange, info: BlockInfo, text s: String) {
        let end = NSMaxRange(para)
        if inEdit == 0, !undoing {
            // Outside the text view's edits, the block goes in with the step
            // that puts it in, the separator after the paragraph with it:
            // given its block after, the step would undo and redo by the ID
            // it is given in place of, which the block before may have too.
            let last = end == storage.length
            let piece = NSMutableAttributedString(
                string: "\n", attributes: [.leafletBlock: text.paragraph(at: para.location).info])
            piece.append(NSAttributedString(string: s, attributes: [.leafletBlock: info]))
            if !last {
                piece.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]))
            }
            replace(NSRange(location: end, length: last ? 0 : 1), with: piece)
            if last {
                text.trailing = info
                editor.layout.redrawEnd()
            }
            editor.edited(NSRange(location: end, length: piece.length))
            textView.selectedRange = NSRange(location: end + 1 + (s as NSString).length, length: 0)
            return
        }
        textView.selectedRange = NSRange(location: end, length: 0)
        insert("\n" + s, replacing: NSRange(location: end, length: 0))
        setBlock(info, at: end + 1)
        textView.selectedRange = NSRange(location: end + 1 + (s as NSString).length, length: 0)
    }

    /// setBlock makes info the block of the paragraph at location, as a
    /// step to undo unless it is only how the page shows, as a fold.
    func setBlock(_ info: BlockInfo, at location: Int, undoable: Bool = true) {
        let (range, old) = text.paragraph(at: location)
        var whole = range
        if NSMaxRange(range) < storage.length { whole.length += 1 }
        if whole.length > 0 {
            storage.addAttribute(.leafletBlock, value: info, range: whole)
        }
        let before = text.trailing
        if NSMaxRange(range) == storage.length { text.trailing = info }
        if text.trailing !== before { editor.layout.redrawEnd() }
        if undoable, old !== info { registerBlockUndo(textView.undoManager, now: info, was: old) }
        // The block is the one given, whatever an edit emptying the
        // paragraph before it left.
        text.emptied = nil
        editor.edited(whole.length > 0 ? whole : NSRange(location: range.location, length: 0))
    }

    /// registerBlockUndo puts block was back in place of now, as a step to
    /// undo, and now on redo. It finds now itself, wherever the text has
    /// moved it since: the text view's own steps undone before it move it.
    private func registerBlockUndo(_ undo: UndoManager?, now: BlockInfo, was: BlockInfo) {
        undo?.registerUndo(withTarget: self) { e in
            guard let at = e.text.location(of: now) else { return }
            e.setBlock(was, at: at, undoable: false)
            e.registerBlockUndo(undo, now: was, was: now)
        }
    }

    /// replace puts text in place of range, as the rules and menus change
    /// the text, as a step to undo: the text view's own steps, as typing,
    /// were made over the text as it was, which undoing this gives back.
    func replace(_ range: NSRange, with s: NSAttributedString) {
        let before = storage.attributedSubstring(from: range)
        let was = text.trailing
        put(s, in: range)
        registerUndo(
            textView.undoManager, range: NSRange(location: range.location, length: s.length),
            before: before, trailing: was)
        tellKeyboard()
    }

    /// put puts text in place of range. Within the text view's own edit,
    /// as a rule takes a key, or as a step is undone or redone, it changes
    /// the text itself. Otherwise, as a command or a sheet changes it, its
    /// characters go in through the text view, which drops every step to
    /// undo, the typing before too, when its text changes under it, then
    /// their blocks and formats: the step that undoes it is the editor's
    /// (replace), with the blocks as they were, so the text view registers
    /// none of its own.
    private func put(_ s: NSAttributedString, in range: NSRange) {
        let undo = textView.undoManager
        guard inEdit == 0, !undoing, let r = textRange(range) else {
            return storage.replaceCharacters(in: range, with: s)
        }
        // The selection where the text changing would leave it.
        var sel = textView.selectedRange
        let moved = s.length - range.length
        if sel.location >= NSMaxRange(range) {
            sel.location += moved
        } else if NSMaxRange(sel) > range.location {
            sel = NSRange(location: range.location + s.length, length: 0)
        }
        let emptied = text.emptied
        putting = true
        bypass = true
        undo?.disableUndoRegistration()
        textView.replace(r, withText: s.string)
        undo?.enableUndoRegistration()
        bypass = false
        putting = false
        text.emptied = emptied
        storage.beginEditing()
        s.enumerateAttributes(in: NSRange(location: 0, length: s.length)) { attributes, run, _ in
            storage.setAttributes(
                attributes,
                range: NSRange(location: range.location + run.location, length: run.length))
        }
        storage.endEditing()
        if NSMaxRange(sel) <= storage.length { textView.selectedRange = sel }
    }

    /// registerUndo puts before back in place of range, which may be of
    /// another length, and the trailing block as it was, and the text there
    /// now back on redo.
    func registerUndo(
        _ undo: UndoManager?, range: NSRange, before: NSAttributedString, trailing was: BlockInfo?
    ) {
        undo?.registerUndo(withTarget: self) { e in
            guard NSMaxRange(range) <= e.storage.length else { return }
            let now = e.storage.attributedSubstring(from: range)
            let trailingNow = e.text.trailing
            e.storage.replaceCharacters(in: range, with: before)
            e.text.trailing = was
            let back = NSRange(location: range.location, length: before.length)
            e.registerUndo(undo, range: back, before: now, trailing: trailingNow)
            if back.length != range.length {
                e.textView.selectedRange = NSRange(location: NSMaxRange(back), length: 0)
            }
            e.tellKeyboard()
            e.editor.layout.redrawEnd()
            e.editor.edited(back)
        }
    }

    /// rewrite puts blocks, each a paragraph, in place of the paragraphs
    /// first through last of all, as one step to undo.
    func rewrite(
        _ all: [DocumentMapper.Paragraph], _ first: Int, _ last: Int,
        _ blocks: [(info: BlockInfo, text: NSAttributedString)]
    ) {
        let start = all[first].range.location
        let toEnd = last == all.count - 1
        let end = toEnd ? storage.length : all[last + 1].range.location
        let out = NSMutableAttributedString()
        for (i, b) in blocks.enumerated() {
            let t = NSMutableAttributedString(attributedString: b.text)
            t.addAttribute(
                .leafletBlock, value: b.info, range: NSRange(location: 0, length: t.length))
            out.append(t)
            // Each paragraph's separator is its block's; the page's last
            // paragraph has none.
            if i < blocks.count - 1 || !toEnd {
                out.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: b.info]))
            }
        }
        replace(NSRange(location: start, length: end - start), with: out)
        if toEnd, let b = blocks.last { text.trailing = b.info }
        editor.layout.redrawEnd()
        editor.edited(NSRange(location: start, length: out.length))
    }

    /// tellKeyboard says the text changed other than by the keyboard's
    /// edits, as the rules and menus change it. Untold, the keyboard goes
    /// on from the text it last knew, taking the line for a word still
    /// being typed, and on Return corrects it to what it guessed.
    func tellKeyboard() {
        textView.inputDelegate?.textWillChange(textView)
        textView.inputDelegate?.textDidChange(textView)
    }

    /// keepingScroll changes a block away from the caret without the text
    /// view scrolling back to the caret, as it does after a change.
    func keepingScroll(_ change: () -> Void) {
        let offset = textView.contentOffset
        change()
        textView.layoutIfNeeded()
        textView.setContentOffset(offset, animated: false)
        DispatchQueue.main.async { [weak textView] in
            textView?.setContentOffset(offset, animated: false)
        }
    }
}
