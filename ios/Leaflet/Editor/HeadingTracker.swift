import UIKit

@MainActor
final class HeadingTracker {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private var atHeading: String?
    private var jumping = false
    private var jumpTo: Int?
    private var jumpLandings = 0
    private var jumpShort = false

    var isJumping: Bool { jumping }

    init(_ editor: EditorController) { self.editor = editor }

    func didScroll() {
        if !jumping { update() }
    }

    func didEndScrollingAnimation() {
        let landing = jumpTo != nil
        while landJump() {}
        jumping = false
        if !(landing && jumpShort) { update() }
        textView.replaceBlocks()
    }

    func willBeginDragging() { endJump() }

    private func endJump() {
        jumping = false
        jumpTo = nil
        jumpLandings = 0
    }

    func update() {
        guard let tlm = textView.textLayoutManager else { return }
        let margin: CGFloat = 24
        let y =
            textView.contentOffset.y + textView.adjustedContentInset.top + margin
            - textView.textContainerInset.top
        var found: String?
        let folded = editor.layout.folded
        if y >= 0, storage.length > 0,
            let fragment = tlm.textLayoutFragment(for: CGPoint(x: 0, y: y))
        {
            let offset = tlm.offset(
                from: tlm.documentRange.location, to: fragment.rangeInElement.location)
            let end = min(offset + 1, storage.length)
            storage.enumerateAttribute(
                .leafletBlock, in: NSRange(location: 0, length: end), options: .reverse
            ) { value, _, stop in
                if let b = value as? BlockInfo, b.type == "heading", !folded.contains(b.id) {
                    found = b.id
                    stop.pointee = true
                }
            }
        }
        guard found != atHeading else { return }
        atHeading = found
        editor.onHeading?(found)
    }

    func reveal(blockID: String, atTop: Bool) {
        let all = text.blocks()
        guard var at = all.firstIndex(where: { $0.info.id == blockID }) else { return }
        let folded = editor.layout.folded
        while at > 0, folded.contains(all[at].info.id) { at -= 1 }
        let p = all[at]
        textView.selectedRange = NSRange(location: p.range.location, length: 0)
        if atTop, scrollToTop(p.range.location) {
            if p.info.type == "heading", atHeading != blockID {
                atHeading = blockID
                editor.onHeading?(blockID)
            }
            return
        }
        endJump()
        textView.scrollRangeToVisible(p.range)
    }

    private func scrollToTop(_ offset: Int) -> Bool {
        guard let target = topOffset(offset) else { return false }
        jumping = abs(target - textView.contentOffset.y) > 1
        jumpTo = jumping ? offset : nil
        textView.setContentOffset(CGPoint(x: textView.contentOffset.x, y: target), animated: true)
        return true
    }

    private func topOffset(_ offset: Int) -> CGFloat? {
        guard let tlm = textView.textLayoutManager,
            let start = tlm.location(tlm.documentRange.location, offsetBy: offset),
            let before = NSTextRange(location: tlm.documentRange.location, end: start)
        else { return nil }
        tlm.ensureLayout(for: before)
        textView.layoutIfNeeded()
        guard let fragment = tlm.textLayoutFragment(for: start) else { return nil }
        let inset = textView.adjustedContentInset
        let y = fragment.layoutFragmentFrame.minY + textView.textContainerInset.top - 12 - inset.top
        let bottom = textView.contentSize.height + inset.bottom - textView.bounds.height
        jumpShort = y > max(bottom, -inset.top) + 2
        return max(-inset.top, min(y, max(bottom, -inset.top)))
    }

    private func landJump() -> Bool {
        guard let offset = jumpTo, let target = topOffset(offset) else { return false }
        jumpLandings += 1
        if jumpLandings >= 4 || abs(target - textView.contentOffset.y) <= 2 {
            jumpTo = nil
            jumpLandings = 0
            return false
        }
        textView.setContentOffset(CGPoint(x: textView.contentOffset.x, y: target), animated: false)
        return true
    }
}
