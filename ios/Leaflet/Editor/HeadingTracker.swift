import UIKit

/// HeadingTracker follows which heading the page is at as it scrolls or
/// changes, as the desktop's outline shows it, and scrolls the page to a
/// block, a heading to the top of the view as the outline goes to it.
@MainActor
final class HeadingTracker {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private var atHeading: String?
    /// jumping is a scroll to a block on its way, which passes headings
    /// the page isn't going to.
    private var jumping = false
    /// jumpTo is the text a jump goes to, until it has landed there;
    /// jumpLandings, how often it was put back there.
    private var jumpTo: Int?
    private var jumpLandings = 0
    /// jumpShort says the jump's text stays below the top, the page
    /// ending first.
    private var jumpShort = false

    /// isJumping says a scroll to a block is on its way: where it lands
    /// holds at any width.
    var isJumping: Bool { jumping }

    init(_ editor: EditorController) { self.editor = editor }

    func didScroll() {
        if !jumping { update() }
    }

    func didEndScrollingAnimation() {
        // Where the text now is, once laid out there, again until it stays.
        let landing = jumpTo != nil
        while landJump() {}
        jumping = false
        if !(landing && jumpShort) { update() }
        // A long jump passes text laid out by estimate: the blocks go
        // again where the text in view now is, not over it.
        textView.replaceBlocks()
    }

    func willBeginDragging() { endJump() }

    /// endJump forgets a jump the page is taken from before it lands, by a
    /// finger or by another scroll: the text it went to is no longer where
    /// the page is going.
    private func endJump() {
        jumping = false
        jumpTo = nil
        jumpLandings = 0
    }

    /// update finds the heading the page is at, and says so if it changed:
    /// the last one whose top is up to just under the top of the view, or
    /// nil above the first.
    func update() {
        guard let tlm = textView.textLayoutManager else { return }
        // A heading counts once its top is within this of the view's top,
        // where going to it from the outline leaves it.
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
            // Back from there, as each frame of a scroll asks: the first
            // heading met is the one the page is at.
            storage.enumerateAttribute(
                .leafletBlock, in: NSRange(location: 0, length: end), options: .reverse
            ) { value, _, stop in
                // A heading folded away is not one the page is at.
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

    /// reveal scrolls to a block and puts the caret at its start. atTop
    /// brings the block to the top of the page, as the outline goes to a
    /// heading, rather than only into view.
    func reveal(blockID: String, atTop: Bool) {
        let all = text.blocks()
        guard var at = all.firstIndex(where: { $0.info.id == blockID }) else { return }
        // Folded away, it is shown by what folds it: the nearest block
        // before it that shows.
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
        // A jump on its way gives way to this scroll, which ends where the
        // block shows, not at the jump's heading.
        endJump()
        textView.scrollRangeToVisible(p.range)
    }

    /// scrollToTop scrolls the text at offset to the top of the view, as
    /// far as the page goes, and reports whether it could.
    private func scrollToTop(_ offset: Int) -> Bool {
        guard let target = topOffset(offset) else { return false }
        jumping = abs(target - textView.contentOffset.y) > 1
        jumpTo = jumping ? offset : nil
        textView.setContentOffset(CGPoint(x: textView.contentOffset.x, y: target), animated: true)
        return true
    }

    /// topOffset is the scroll that puts the text at offset at the top of
    /// the view, as far as the page goes.
    private func topOffset(_ offset: Int) -> CGFloat? {
        guard let tlm = textView.textLayoutManager,
            let start = tlm.location(tlm.documentRange.location, offsetBy: offset),
            let before = NSTextRange(location: tlm.documentRange.location, end: start)
        else { return nil }
        // Laid out down to it, so its place, and the page's height, are
        // known rather than estimated.
        tlm.ensureLayout(for: before)
        textView.layoutIfNeeded()
        guard let fragment = tlm.textLayoutFragment(for: start) else { return nil }
        let inset = textView.adjustedContentInset
        let y = fragment.layoutFragmentFrame.minY + textView.textContainerInset.top - 12 - inset.top
        let bottom = textView.contentSize.height + inset.bottom - textView.bounds.height
        // Near the page's end the text can't come to the top: the heading
        // gone to stays the one the outline shows, not the one above it.
        jumpShort = y > max(bottom, -inset.top) + 2
        return max(-inset.top, min(y, max(bottom, -inset.top)))
    }

    /// landJump puts the text a jump went to at the top once it is there:
    /// the text it passed, laid out as it scrolled into view, may have
    /// moved it, and the page's height it was kept within was an
    /// estimate. It reports whether it moved the page.
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
