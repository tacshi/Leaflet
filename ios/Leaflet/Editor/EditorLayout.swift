import LeafletCore
import UIKit

@MainActor
final class EditorLayout: NSObject, @preconcurrency NSTextLayoutManagerDelegate,
    @preconcurrency NSTextContentStorageDelegate
{
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private(set) var folded: Set<String> = []
    private var foldsFound = -1
    private var asked: (blocks: String, marks: String)?

    init(_ editor: EditorController) {
        self.editor = editor
        super.init()
    }

    func attach() {
        textView.textLayoutManager?.delegate = self
        if let content = textView.textLayoutManager?.textContentManager as? NSTextContentStorage,
            content.delegate == nil
        {
            content.delegate = self
        }
    }

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

    func textLayoutManager(
        _ textLayoutManager: NSTextLayoutManager,
        textLayoutFragmentFor location: any NSTextLocation,
        in textElement: NSTextElement
    ) -> NSTextLayoutFragment {
        let f = BlockLayoutFragment(textElement: textElement, range: textElement.elementRange)
        f.palette = editor.style.palette
        f.dateOffer = editor.due.dateOffer
        if let range = textElement.elementRange,
            textLayoutManager.offset(
                from: range.endLocation, to: textLayoutManager.documentRange.endLocation)
                == 0
        {
            f.trailing = trailingLook()
        }
        return f
    }

    func redrawEnd() {
        if storage.length == 0 { return textView.setNeedsLayout() }
        guard let tlm = textView.textLayoutManager else { return }
        let ns = storage.string as NSString
        let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
        guard let start = tlm.location(tlm.documentRange.location, offsetBy: last.location),
            let range = NSTextRange(location: start, end: tlm.documentRange.endLocation)
        else { return }
        let look = trailingLook()
        tlm.enumerateTextLayoutFragments(from: start, options: []) { f in
            (f as? BlockLayoutFragment)?.trailing = look
            return false
        }
        tlm.invalidateLayout(for: range)
        textView.setNeedsLayout()
    }

    private func trailingLook() -> BlockLayoutFragment.Trailing? {
        guard let trailing = text.trailing else { return nil }
        return BlockLayoutFragment.Trailing(
            info: trailing, font: editor.style.baseFont(trailing),
            ordinal: trailing.type == "numbered_list"
                ? EditorStyle.ordinal(storage, at: storage.length, trailing) : 1)
    }
}
