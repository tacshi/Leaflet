import UIKit

/// DueDates shows the to-dos' due dates as the time goes by: the chip of
/// the to-do at the caret offers one, a tap on a chip opens its date
/// (DuePopover), and the chips are drawn again as the day changes or a
/// time one is due at passes.
@MainActor
final class DueDates {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    /// dateOffer is the to-do whose chip offers a due date: the one the
    /// caret is in, or whose date is open.
    private(set) var dateOffer: String?
    private var dueOpen: String?
    /// roomy is the to-do that took room for its offer, its text too long
    /// to leave any, until the offer moves on.
    private var roomy: String?
    private var fitting: Task<Void, Never>?
    /// dueAt is the refresh of the due dates at the next time a to-do is
    /// due at, which turns it overdue.
    private var dueAt: Task<Void, Never>?

    init(_ editor: EditorController) { self.editor = editor }

    /// updateDateOffer has the to-do at the caret offer a due date, as the
    /// desktop's focused to-do does, drawing the to-dos that change again.
    func updateDateOffer() {
        var offer = dueOpen
        if offer == nil, textView.isEditable, textView.isFirstResponder,
            textView.selectedRange.length == 0
        {
            let (_, info) = text.paragraph(at: textView.selectedRange.location)
            if info.type == "todo_list" { offer = info.id }
        }
        // After what laid the text out as it is now, and after the edit
        // that may have brought us here, which no attribute may change.
        fitting?.cancel()
        fitting = Task { [weak self] in
            guard !Task.isCancelled else { return }
            self?.fitOffer()
        }
        guard offer != dateOffer else { return }
        let old = dateOffer
        dateOffer = offer
        guard let tlm = textView.textLayoutManager else { return }
        for p in text.blocks() where p.info.id == old || p.info.id == offer {
            // A trailing to-do is drawn by the paragraph before it.
            let at = min(p.range.location, max(storage.length - 1, 0))
            let para = (storage.string as NSString).paragraphRange(
                for: NSRange(location: at, length: 0))
            guard let start = tlm.location(tlm.documentRange.location, offsetBy: para.location),
                let end = tlm.location(start, offsetBy: para.length),
                let range = NSTextRange(location: start, end: end)
            else { continue }
            // Its fragment stays as the layout is made again.
            tlm.enumerateTextLayoutFragments(from: start, options: []) { f in
                (f as? BlockLayoutFragment)?.dateOffer = offer
                return false
            }
            tlm.invalidateLayout(for: range)
        }
        // Laid out again in view, for them to draw again.
        textView.replaceBlocks()
    }

    /// fitOffer has the to-do at the caret that offers a date make room for
    /// the offer, where its last line leaves none, and the one that did give
    /// it back as the offer moves on. Only that to-do's lines change, for
    /// as long as it offers one: room left in every to-do would narrow them
    /// all, for a chip that most of them never show.
    private func fitOffer() {
        if let id = roomy, id != dateOffer {
            roomy = nil
            if let p = text.blocks().first(where: { $0.info.id == id }) {
                setTail(p.range, -DueChip.room(p.info))
            }
        }
        guard let id = dateOffer, textView.selectedRange.length == 0 else { return }
        let (range, info) = text.paragraph(at: textView.selectedRange.location)
        guard info.id == id, Due(info) == nil, range.length > 0 else { return }
        if roomy != id {
            guard let tlm = textView.textLayoutManager,
                let start = tlm.location(tlm.documentRange.location, offsetBy: range.location),
                let fragment = tlm.textLayoutFragment(for: start) as? BlockLayoutFragment,
                fragment.offerOverflows
            else { return }
            roomy = id
        }
        // Each time: styling the paragraph as it is typed in gives it back
        // the lines of a to-do with no date.
        setTail(range, -DueChip.offerRoom)
    }

    /// setTail has the lines of the paragraph at range end tail from the
    /// page's edge. It is how the text is laid out, not what it says: the
    /// page has not changed.
    private func setTail(_ range: NSRange, _ tail: CGFloat) {
        let para = (storage.string as NSString).paragraphRange(for: range)
        guard para.length > 0,
            let now = storage.attribute(.paragraphStyle, at: para.location, effectiveRange: nil)
                as? NSParagraphStyle,
            now.tailIndent != tail, let style = now.mutableCopy() as? NSMutableParagraphStyle
        else { return }
        style.tailIndent = tail
        storage.addAttribute(.paragraphStyle, value: style, range: para)
    }

    /// refresh draws the to-dos' due dates again, as the day changes, a
    /// time one is due at passes, or the app comes back: what a chip says,
    /// Today or Tomorrow, its room and whether it is overdue are as of when
    /// it was drawn.
    func refresh() {
        guard let tlm = textView.textLayoutManager else { return }
        let ns = storage.string as NSString
        for p in text.blocks() where Due(p.info) != nil {
            guard p.range.length > 0 else {
                editor.layout.redrawEnd()
                continue
            }
            let para = ns.paragraphRange(for: p.range)
            editor.style.apply(to: storage, in: para)
            guard let start = tlm.location(tlm.documentRange.location, offsetBy: para.location),
                let end = tlm.location(start, offsetBy: para.length),
                let range = NSTextRange(location: start, end: end)
            else { continue }
            tlm.invalidateLayout(for: range)
        }
        textView.setNeedsLayout()
        schedule()
    }

    /// schedule has the due dates drawn again at the next time a to-do of
    /// the page is due at.
    func schedule() {
        dueAt?.cancel()
        dueAt = nil
        let now = Date.now
        let next = text.blocks()
            .compactMap { Due($0.info) }.filter { $0.timed && $0.date > now }.map(\.date).min()
        guard let next else { return }
        dueAt = Task { [weak self] in
            try? await Task.sleep(for: .seconds(next.timeIntervalSinceNow + 1))
            guard !Task.isCancelled else { return }
            self?.refresh()
        }
    }

    /// stop stops the refresh to come, as the page closes.
    func stop() {
        dueAt?.cancel()
        dueAt = nil
    }

    /// chipRect is where a to-do's chip is, in the text view, if shown.
    func chipRect(_ info: BlockInfo) -> CGRect? {
        guard let tlm = textView.textLayoutManager else { return nil }
        let inset = CGPoint(x: textView.textContainerInset.left, y: textView.textContainerInset.top)
        var found: CGRect?
        tlm.enumerateTextLayoutFragments(from: tlm.documentRange.location, options: []) { f in
            guard let f = f as? BlockLayoutFragment else { return true }
            if let chip = f.dueChips().first(where: { $0.info.id == info.id }) {
                let origin = f.layoutFragmentFrame.origin
                found = chip.rect.offsetBy(dx: origin.x + inset.x, dy: origin.y + inset.y)
                return false
            }
            return true
        }
        return found
    }

    /// open opens a to-do's due date from rect, which the to-do takes as it
    /// closes.
    func open(_ info: BlockInfo, from rect: CGRect) {
        let id = info.id
        let popover = DuePopover(Due(info)) { [weak self] due, changed in
            guard let self else { return }
            dueOpen = nil
            if changed,
                let p = text.blocks().first(where: { $0.info.id == id }),
                p.info.type == "todo_list"
            {
                editor.edits.keepingScroll {
                    self.editor.edits.setBlock(
                        p.info.setting("due", due?.value), at: p.range.location)
                }
                schedule()
            }
            updateDateOffer()
        }
        popover.popoverPresentationController?.sourceView = textView
        popover.popoverPresentationController?.sourceRect = rect
        popover.popoverPresentationController?.permittedArrowDirections = [.up, .down]
        dueOpen = id
        updateDateOffer()
        textView.window?.rootViewController?.topmost.present(popover, animated: true)
    }

    /// openAtCaret opens the due date of the to-do at the caret, from its
    /// chip, or from the caret where it has no room for one.
    func openAtCaret(_ info: BlockInfo) {
        let caret = textView.selectedTextRange.map { textView.caretRect(for: $0.end) }
        open(info, from: chipRect(info) ?? caret ?? .zero)
    }
}
