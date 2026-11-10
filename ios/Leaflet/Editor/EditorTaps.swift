import LeafletCore
import UIKit

@MainActor
final class EditorTaps: NSObject, UIGestureRecognizerDelegate,
    @preconcurrency UIEditMenuInteractionDelegate, UIContextMenuInteractionDelegate
{
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }
    private var text: BlockText { editor.text }
    private var storage: NSTextStorage { editor.text.storage }

    private lazy var selectionMenu = UIEditMenuInteraction(delegate: self)

    init(_ editor: EditorController) {
        self.editor = editor
        super.init()
    }

    func attach() {
        let tap = UITapGestureRecognizer(target: self, action: #selector(tap(_:)))
        tap.delegate = self
        textView.addGestureRecognizer(tap)
        let press = UILongPressGestureRecognizer(
            target: self, action: #selector(pressSelection(_:)))
        press.delegate = self
        textView.addGestureRecognizer(press)
        textView.addInteraction(selectionMenu)
        textView.addInteraction(UIContextMenuInteraction(delegate: self))
    }

    private func markerHit(_ point: CGPoint) -> (BlockInfo, Int)? {
        guard let tlm = textView.textLayoutManager else { return nil }
        let kinds: Set<String> =
            textView.isEditable
            ? ["todo_list", "toggle_list", "heading", "callout"] : ["toggle_list", "heading"]
        let p = CGPoint(
            x: point.x - textView.textContainerInset.left,
            y: point.y - textView.textContainerInset.top)
        guard let fragment = tlm.textLayoutFragment(for: p) as? BlockLayoutFragment,
            let element = fragment.textElement, let range = element.elementRange
        else { return nil }
        let location = tlm.offset(from: tlm.documentRange.location, to: range.location)
        let (_, info) = text.paragraph(at: location)
        var targets = [(info, location, fragment.markerRect(info))]
        if let trailing = fragment.trailing, let baseline = fragment.trailingBaseline {
            targets.insert(
                (
                    trailing.info, storage.length,
                    fragment.markerRect(trailing.info, baseline: baseline, font: trailing.font)
                ), at: 0)
        }
        let origin = fragment.layoutFragmentFrame.origin
        for (info, at, rect) in targets where kinds.contains(info.type) {
            var hit = rect.offsetBy(dx: origin.x, dy: origin.y).insetBy(dx: -8, dy: -6)
            hit.size.width = max(0, min(hit.maxX, EditorStyle.leading(info)) - hit.minX)
            if hit.contains(p) { return (info, at) }
        }
        return nil
    }

    private func linkHit(_ point: CGPoint, command: Bool) -> URL? {
        guard textView.isEditable, let position = textView.closestPosition(to: point) else {
            return nil
        }
        let offset = textView.offset(from: textView.beginningOfDocument, to: position)
        for at in [offset, offset - 1] where at >= 0 && at < storage.length {
            guard let range = editor.edits.textRange(NSRange(location: at, length: 1)),
                textView.firstRect(for: range).insetBy(dx: -2, dy: -2).contains(point),
                let url = EditorStyle.linkTarget(storage.attributes(at: at, effectiveRange: nil))
            else { continue }
            let page = url.scheme == "leaflet"
            return page || command || !textView.isFirstResponder ? url : nil
        }
        return nil
    }

    private func codeBarHit(_ point: CGPoint) -> (BlockInfo, Int, copy: Bool)? {
        guard let tlm = textView.textLayoutManager else { return nil }
        let p = CGPoint(
            x: point.x - textView.textContainerInset.left,
            y: point.y - textView.textContainerInset.top)
        for dy: CGFloat in [0, BlockLayoutFragment.codeBar] {
            guard
                let fragment = tlm.textLayoutFragment(for: CGPoint(x: p.x, y: p.y + dy))
                    as? BlockLayoutFragment,
                let range = fragment.textElement?.elementRange
            else { continue }
            let location = tlm.offset(from: tlm.documentRange.location, to: range.location)
            let (_, info) = text.paragraph(at: location)
            guard info.type == "code" else { continue }
            let card = fragment.codeCard.offsetBy(
                dx: fragment.layoutFragmentFrame.minX, dy: fragment.layoutFragmentFrame.minY)
            let parts = BlockLayoutFragment.codeBarParts(card)
            if parts.copy.insetBy(dx: -6, dy: -4).contains(p) { return (info, location, true) }
            if textView.isEditable, parts.language.insetBy(dx: -6, dy: -4).contains(p) {
                return (info, location, false)
            }
        }
        return nil
    }

    private func dueChipHit(_ point: CGPoint) -> (BlockInfo, CGRect)? {
        guard textView.isEditable, let tlm = textView.textLayoutManager else { return nil }
        let inset = CGPoint(x: textView.textContainerInset.left, y: textView.textContainerInset.top)
        let p = CGPoint(x: point.x - inset.x, y: point.y - inset.y)
        guard let fragment = tlm.textLayoutFragment(for: p) as? BlockLayoutFragment else {
            return nil
        }
        let origin = fragment.layoutFragmentFrame.origin
        for chip in fragment.dueChips() {
            let rect = chip.rect.offsetBy(dx: origin.x + inset.x, dy: origin.y + inset.y)
            if rect.insetBy(dx: -8, dy: -6).contains(point) { return (chip.info, rect) }
        }
        return nil
    }

    private func inSelection(_ point: CGPoint) -> Bool {
        guard collapses(at: point), let range = textView.selectedTextRange else { return false }
        return textView.selectionRects(for: range).contains { $0.rect.contains(point) }
    }

    private func collapses(at point: CGPoint) -> Bool {
        guard textView.selectedRange.length > 0, textView.isFirstResponder else { return false }
        for v in [textView.header, textView.footer] {
            if let v, !v.isHidden, v.frame.contains(point) { return false }
        }
        return true
    }

    func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        let at = g.location(in: textView)
        if g is UILongPressGestureRecognizer { return inSelection(at) }
        return markerHit(at) != nil || codeBarHit(at) != nil || dueChipHit(at) != nil
            || linkHit(at, command: g.modifierFlags.contains(.command)) != nil
            || collapses(at: at)
    }

    func gestureRecognizer(
        _ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer
    ) -> Bool {
        return textView.selectedRange.length > 0
            && (g is UILongPressGestureRecognizer
                ? other is UILongPressGestureRecognizer : other is UITapGestureRecognizer)
            && other.view?.isDescendant(of: textView) == true
    }

    func gestureRecognizer(
        _ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer
    ) -> Bool {
        false
    }

    @objc private func tap(_ g: UITapGestureRecognizer) {
        let at = g.location(in: textView)
        if let (info, location, copy) = codeBarHit(at) {
            return tapCodeBar(info, at: location, copy: copy, from: at)
        }
        if let (info, rect) = dueChipHit(at) {
            return editor.due.open(info, from: rect)
        }
        if let (info, location) = markerHit(at) {
            return editor.edits.keepingScroll { tapMarker(info, at: location) }
        }
        if let url = linkHit(at, command: g.modifierFlags.contains(.command)) {
            return editor.open(url)
        }
        if collapses(at: at), let position = textView.closestPosition(to: at) {
            let location = textView.offset(from: textView.beginningOfDocument, to: position)
            textView.selectedRange = NSRange(location: location, length: 0)
        }
    }

    func tapMarker(_ info: BlockInfo, at location: Int) {
        let edits = editor.edits
        if info.type == "todo_list" {
            edits.setBlock(info.setting("checked", .bool(!info.checked)), at: location)
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
        } else if info.type == "callout" {
            let next = MobileNextTone(info.data["tone"]?.string ?? "")
            edits.setBlock(info.setting("tone", .string(next)), at: location)
        } else if !textView.isEditable {
            editor.layout.foldHere(
                info.setting("collapsed", .bool(!(info.data["collapsed"]?.bool ?? false))),
                at: location)
        } else {
            edits.setBlock(
                info.setting("collapsed", .bool(!(info.data["collapsed"]?.bool ?? false))),
                at: location, undoable: false)
            let caret = textView.selectedRange.location
            if caret < storage.length,
                let inside = storage.attribute(.leafletBlock, at: caret, effectiveRange: nil)
                    as? BlockInfo,
                editor.layout.folded.contains(inside.id)
            {
                let (line, _) = text.paragraph(at: location)
                textView.selectedRange = NSRange(location: NSMaxRange(line), length: 0)
            }
        }
    }

    func tapCodeBar(_ info: BlockInfo, at location: Int, copy: Bool, from point: CGPoint) {
        let (para, _) = text.paragraph(at: location)
        if copy {
            UIPasteboard.general.string = (storage.string as NSString).substring(with: para)
                .replacingOccurrences(of: String(lineBreak), with: "\n")
            UINotificationFeedbackGenerator().notificationOccurred(.success)
            return
        }
        guard textView.isEditable else { return }
        let current = info.data["language"]?.string ?? ""
        let sheet = UIAlertController(
            title: L.editorCodeLanguage(name: CodeLanguage.name(current)), message: nil,
            preferredStyle: .actionSheet)
        for language in CodeLanguage.all {
            let action = UIAlertAction(title: language.name, style: .default) { [weak self] _ in
                guard let self else { return }
                let (_, now) = self.text.paragraph(at: location)
                guard now.id == info.id else { return }
                self.editor.edits.keepingScroll {
                    self.editor.edits.setBlock(
                        now.setting("language", .string(language.id)), at: location)
                }
            }
            sheet.addAction(action)
            if language.id == current { sheet.preferredAction = action }
        }
        sheet.addAction(UIAlertAction(title: L.commonCancel, style: .cancel))
        sheet.popoverPresentationController?.sourceView = textView
        sheet.popoverPresentationController?.sourceRect = CGRect(origin: point, size: .zero)
        textView.window?.rootViewController?.topmost.present(sheet, animated: true)
    }

    @objc private func pressSelection(_ g: UILongPressGestureRecognizer) {
        guard g.state == .ended else { return }
        selectionMenu.presentEditMenu(
            with: UIEditMenuConfiguration(identifier: nil, sourcePoint: g.location(in: textView)))
    }

    func editMenuInteraction(
        _ interaction: UIEditMenuInteraction, menuFor configuration: UIEditMenuConfiguration,
        suggestedActions: [UIMenuElement]
    ) -> UIMenu? {
        return editor.menus.editMenu(for: textView.selectedRange, suggested: suggestedActions)
    }

    func editMenuInteraction(
        _ interaction: UIEditMenuInteraction, targetRectFor configuration: UIEditMenuConfiguration
    ) -> CGRect {
        guard let range = textView.selectedTextRange else { return .null }
        return textView.firstRect(for: range)
    }

    func contextMenuInteraction(
        _ interaction: UIContextMenuInteraction,
        configurationForMenuAtLocation location: CGPoint
    ) -> UIContextMenuConfiguration? {
        guard textView.isEditable, let (node, _) = textView.blocks.menuHit(location) else {
            return nil
        }
        return UIContextMenuConfiguration(identifier: node.id as NSString, previewProvider: nil) {
            [weak self] _ in
            let title = node.type == "chart" ? L.editorEditData : L.commonEdit
            return UIMenu(children: [
                UIAction(title: title, image: UIImage(systemName: "pencil")) { _ in
                    self?.editor.onEditBlock?(node)
                }
            ])
        }
    }

    func contextMenuInteraction(
        _ interaction: UIContextMenuInteraction,
        configuration: UIContextMenuConfiguration,
        highlightPreviewForItemWithIdentifier identifier: any NSCopying
    ) -> UITargetedPreview? {
        blockPreview(configuration)
    }

    func contextMenuInteraction(
        _ interaction: UIContextMenuInteraction,
        configuration: UIContextMenuConfiguration,
        dismissalPreviewForItemWithIdentifier identifier: any NSCopying
    ) -> UITargetedPreview? {
        blockPreview(configuration)
    }

    private func blockPreview(_ configuration: UIContextMenuConfiguration) -> UITargetedPreview? {
        guard let id = configuration.identifier as? String,
            let view = textView.blocks.shownView(id)
        else { return nil }
        let p = UIPreviewParameters()
        p.visiblePath = UIBezierPath(roundedRect: view.bounds, cornerRadius: 10)
        return UITargetedPreview(view: view, parameters: p)
    }
}
