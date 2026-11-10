import UIKit

@MainActor
final class DueDates {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private(set) var dateOffer: String?
    private var dueOpen: String?
    private var roomy: String?
    private var fitting: Task<Void, Never>?
    private var dueAt: Task<Void, Never>?

    init(_ editor: EditorController) { self.editor = editor }

    func updateDateOffer() {
        var offer = dueOpen
        if offer == nil, textView.isEditable, textView.isFirstResponder,
            textView.selectedRange.length == 0
        {
            let (_, info) = text.paragraph(at: textView.selectedRange.location)
            if info.type == "todo_list" { offer = info.id }
        }
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
            let at = min(p.range.location, max(storage.length - 1, 0))
            let para = (storage.string as NSString).paragraphRange(
                for: NSRange(location: at, length: 0))
            guard let start = tlm.location(tlm.documentRange.location, offsetBy: para.location),
                let end = tlm.location(start, offsetBy: para.length),
                let range = NSTextRange(location: start, end: end)
            else { continue }
            tlm.enumerateTextLayoutFragments(from: start, options: []) { f in
                (f as? BlockLayoutFragment)?.dateOffer = offer
                return false
            }
            tlm.invalidateLayout(for: range)
        }
        textView.replaceBlocks()
    }

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
        setTail(range, -DueChip.offerRoom)
    }

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

    func stop() {
        dueAt?.cancel()
        dueAt = nil
    }

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

    func openAtCaret(_ info: BlockInfo) {
        let caret = textView.selectedTextRange.map { textView.caretRect(for: $0.end) }
        open(info, from: chipRect(info) ?? caret ?? .zero)
    }
}
