import UIKit

@MainActor
final class TextEdits {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    var bypass = false
    private(set) var putting = false
    var inEdit = 0

    init(_ editor: EditorController) { self.editor = editor }

    var undoing: Bool {
        textView.undoManager?.isUndoing == true || textView.undoManager?.isRedoing == true
    }

    func insert(_ s: String, replacing range: NSRange) {
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

    func insertParagraph(after para: NSRange, info: BlockInfo, text s: String) {
        let end = NSMaxRange(para)
        if inEdit == 0, !undoing {
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
        text.emptied = nil
        editor.edited(whole.length > 0 ? whole : NSRange(location: range.location, length: 0))
    }

    private func registerBlockUndo(_ undo: UndoManager?, now: BlockInfo, was: BlockInfo) {
        undo?.registerUndo(withTarget: self) { [weak undo] e in
            guard let at = e.text.location(of: now) else { return }
            e.setBlock(was, at: at, undoable: false)
            e.registerBlockUndo(undo, now: was, was: now)
        }
    }

    func replace(_ range: NSRange, with s: NSAttributedString) {
        let before = storage.attributedSubstring(from: range)
        let was = text.trailing
        put(s, in: range)
        registerUndo(
            textView.undoManager, range: NSRange(location: range.location, length: s.length),
            before: before, trailing: was)
        tellKeyboard()
    }

    private func put(_ s: NSAttributedString, in range: NSRange) {
        let undo = textView.undoManager
        guard inEdit == 0, !undoing, let r = textRange(range) else {
            return storage.replaceCharacters(in: range, with: s)
        }
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

    func registerUndo(
        _ undo: UndoManager?, range: NSRange, before: NSAttributedString, trailing was: BlockInfo?
    ) {
        undo?.registerUndo(withTarget: self) { [weak undo] e in
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
            if i < blocks.count - 1 || !toEnd {
                out.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: b.info]))
            }
        }
        replace(NSRange(location: start, length: end - start), with: out)
        if toEnd, let b = blocks.last { text.trailing = b.info }
        editor.layout.redrawEnd()
        editor.edited(NSRange(location: start, length: out.length))
    }

    func tellKeyboard() {
        textView.inputDelegate?.textWillChange(textView)
        textView.inputDelegate?.textDidChange(textView)
    }

    func keepingScroll(_ change: () -> Void) {
        let offset = textView.contentOffset
        let width = textView.widthChanges
        change()
        textView.layoutIfNeeded()
        scrollBack(to: offset, atWidth: width)
        DispatchQueue.main.async { [weak self] in self?.scrollBack(to: offset, atWidth: width) }
    }

    private func scrollBack(to offset: CGPoint, atWidth width: Int) {
        guard textView.widthChanges == width, !textView.isDragging, !textView.isDecelerating,
            !editor.headings.isJumping
        else { return }
        textView.setContentOffset(offset, animated: false)
    }
}
