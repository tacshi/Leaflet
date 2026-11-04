import LeafletCore
import UIKit

/// EditorLayout lays the page's text out in TextKit: a fragment for each
/// paragraph, which draws its block's marker and card, the last one the
/// empty block after it, and the blocks folded away left out.
@MainActor
final class EditorLayout: NSObject, @preconcurrency NSTextLayoutManagerDelegate,
    @preconcurrency NSTextContentStorageDelegate
{
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    /// folded are the blocks folded away, inside a closed toggle or the
    /// section of a folded heading, which the page leaves out of its
    /// layout: they stay in its text and its content.
    private(set) var folded: Set<String> = []
    /// foldsFound is the change of the blocks folds go by that folded was
    /// found at (BlockText.foldChanges), and asked the blocks the core was
    /// last asked to fold and its answer.
    private var foldsFound = -1
    private var asked: (blocks: String, marks: String)?

    init(_ editor: EditorController) {
        self.editor = editor
        super.init()
    }

    /// attach makes the layout the text view's: it folds the blocks inside
    /// a closed toggle out of the layout.
    func attach() {
        textView.textLayoutManager?.delegate = self
        if let content = textView.textLayoutManager?.textContentManager as? NSTextContentStorage,
            content.delegate == nil
        {
            content.delegate = self
        }
    }

    // MARK: Folding

    /// updateFolds finds the blocks folded away, as a toggle or heading
    /// folds or the blocks change, and lays the page out again if they
    /// did. What a fold hides is as the core's rules have it, the
    /// desktop's (mobile.Folded): the blocks in a closed toggle, and a
    /// folded heading's section, the blocks after it at its depth, and
    /// those in them, up to the next heading of its level or higher. It
    /// looks again only once paragraphs came or went, or a block's type,
    /// depth, level or fold changed, as a key typed in a block changes
    /// none of them; a page with nothing folded, as most are, asks
    /// nothing, and the core is asked again only of blocks that fold
    /// otherwise than those it was last asked of.
    func updateFolds() {
        let all = text.blocks()
        guard text.foldChanges != foldsFound else { return }
        foldsFound = text.foldChanges
        var hidden = Set<String>()
        if text.collapsed > 0 {
            var blocks = ""
            blocks.reserveCapacity(all.count * 20)
            for p in all {
                blocks +=
                    "\(p.info.type),\(p.info.depth),\(p.info.level),\(p.info.isCollapsed ? 1 : 0)\n"
            }
            let marks: String
            if let asked, asked.blocks == blocks {
                marks = asked.marks
            } else {
                marks = MobileFolded(blocks)
                asked = (blocks, marks)
            }
            for (p, mark) in zip(all, marks.utf8) where mark == UInt8(ascii: "1") {
                hidden.insert(p.info.id)
            }
        }
        guard hidden != folded else { return }
        folded = hidden
        textView.relayout()
    }

    /// keepShown unfolds what folds the block of id away, as a new line or
    /// a block moved goes where a toggle or a heading folds: the caret
    /// would be out of sight. What folds a block is the first block before
    /// it that shows, the blocks between folded away with it.
    func keepShown(_ id: String) {
        for _ in 0..<8 {
            updateFolds()
            guard folded.contains(id) else { return }
            let all = text.blocks()
            guard let i = all.firstIndex(where: { $0.info.id == id }),
                let folder = all[..<i].last(where: { !folded.contains($0.info.id) }),
                folder.info.data["collapsed"]?.bool == true
            else { return }
            editor.edits.setBlock(
                folder.info.setting("collapsed", .bool(false)), at: folder.range.location)
        }
    }

    /// foldHere folds or unfolds a toggle or heading of a page that can't
    /// be changed, as info says: here alone, as it saves nothing, for a
    /// section its owner folded to show.
    func foldHere(_ info: BlockInfo, at location: Int) {
        let (range, _) = text.paragraph(at: location)
        var whole = range
        if NSMaxRange(range) < storage.length { whole.length += 1 }
        if whole.length > 0 {
            storage.addAttribute(.leafletBlock, value: info, range: whole)
            editor.style.apply(to: storage, in: whole)
        }
        if NSMaxRange(range) == storage.length {
            text.trailing = info
            redrawEnd()
        }
        updateFolds()
    }

    func textContentManager(
        _ textContentManager: NSTextContentManager, shouldEnumerate textElement: NSTextElement,
        options: NSTextContentManager.EnumerationOptions = []
    ) -> Bool {
        guard !folded.isEmpty, let p = textElement as? NSTextParagraph,
            p.attributedString.length > 0,
            let info = p.attributedString.attribute(.leafletBlock, at: 0, effectiveRange: nil)
                as? BlockInfo
        else { return true }
        return !folded.contains(info.id)
    }

    // MARK: Fragments

    func textLayoutManager(
        _ textLayoutManager: NSTextLayoutManager,
        textLayoutFragmentFor location: any NSTextLocation,
        in textElement: NSTextElement
    ) -> NSTextLayoutFragment {
        let f = BlockLayoutFragment(textElement: textElement, range: textElement.elementRange)
        f.palette = editor.style.palette
        f.dateOffer = editor.due.dateOffer
        // The last paragraph's fragment draws an empty block after it.
        if let range = textElement.elementRange,
            textLayoutManager.offset(
                from: range.endLocation, to: textLayoutManager.documentRange.endLocation)
                == 0
        {
            f.trailing = trailingLook()
        }
        return f
    }

    /// redrawEnd lays the last paragraph out again, whose fragment draws
    /// the empty block after it.
    func redrawEnd() {
        // An empty page's block is drawn by the text view itself.
        if storage.length == 0 { return textView.setNeedsLayout() }
        guard let tlm = textView.textLayoutManager else { return }
        let ns = storage.string as NSString
        let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
        guard let start = tlm.location(tlm.documentRange.location, offsetBy: last.location),
            let range = NSTextRange(location: start, end: tlm.documentRange.endLocation)
        else { return }
        // Its fragment stays as the layout is made again: it draws the
        // block as it now is, and a tap on its marker goes by it.
        let look = trailingLook()
        tlm.enumerateTextLayoutFragments(from: start, options: []) { f in
            (f as? BlockLayoutFragment)?.trailing = look
            return false
        }
        tlm.invalidateLayout(for: range)
        textView.setNeedsLayout()
    }

    /// trailingLook is the empty block the fragment of the page's last
    /// paragraph draws after it: the trailing block.
    private func trailingLook() -> BlockLayoutFragment.Trailing? {
        guard let trailing = text.trailing else { return nil }
        return BlockLayoutFragment.Trailing(
            info: trailing, font: editor.style.baseFont(trailing),
            ordinal: trailing.type == "numbered_list"
                ? EditorStyle.ordinal(storage, at: storage.length, trailing) : 1)
    }
}
