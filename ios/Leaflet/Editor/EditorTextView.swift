import UIKit

/// EditorTextView is the page's text: one TextKit 2 text view for all its
/// blocks, so selection, the keyboard, dictation, Writing Tools and
/// Scribble work across blocks as they do in any text. The title and tags
/// ride on top as its header; on a wide screen the text keeps to a
/// readable width.
final class EditorTextView: UITextView {
    weak var controller: EditorController?
    /// blocks are the views of the blocks without text, laid out with the
    /// text.
    let blocks = BlockViews()
    /// header is the view above the text, which scrolls with it.
    var header: UIView? {
        didSet {
            oldValue?.removeFromSuperview()
            if let header { addSubview(header) }
            setNeedsLayout()
        }
    }
    /// measureHeader returns the header's height at a width: SwiftUI's,
    /// as its hosting controller says, which Auto Layout's fitting doesn't
    /// give a title that wraps.
    var measureHeader: ((CGFloat) -> CGFloat)?
    /// footer is the view after the text, as a blank page's ways to start
    /// it, which scrolls with it; measureFooter its height, 0 for none.
    var footer: UIView? {
        didSet {
            oldValue?.removeFromSuperview()
            if let footer { addSubview(footer) }
            setNeedsLayout()
        }
    }
    var measureFooter: ((CGFloat) -> CGFloat)?
    static let readableWidth: CGFloat = 720
    /// laidOutWidth is the text's width the page was last laid out at.
    private var laidOutWidth: CGFloat = 0
    /// viewportStale says the text in view is to be laid out before the
    /// blocks are placed on it.
    private var viewportStale = false
    /// refusesFocus keeps the focus from coming back to the text while an
    /// embed that had it is taken out (BlockViews.takeOut).
    var refusesFocus = false

    /// relayout lays all of the page out again, as a block's room changed:
    /// the blocks are placed once the text in view is laid out, not on
    /// where it was.
    func relayout() {
        guard let tlm = textLayoutManager else { return }
        tlm.invalidateLayout(for: tlm.documentRange)
        viewportStale = true
        setNeedsLayout()
    }

    /// relayout(block:) lays out again the paragraph of block id, whose
    /// room changed as its view measured itself: the rest of the page
    /// keeps its layout, which laid out again would go back to estimates,
    /// the page's height with them, and the page would jump as it scrolls.
    func relayout(block id: String) {
        guard let tlm = textLayoutManager, let tcm = tlm.textContentManager,
            let range = range(ofBlock: id),
            let start = tcm.location(tcm.documentRange.location, offsetBy: range.location),
            let end = tcm.location(start, offsetBy: range.length),
            let textRange = NSTextRange(location: start, end: end)
        else { return relayout() }
        tlm.invalidateLayout(for: textRange)
        viewportStale = true
        setNeedsLayout()
    }

    /// range is the paragraph of block id in the text, or nil.
    private func range(ofBlock id: String) -> NSRange? {
        var found: NSRange?
        textStorage.enumerateAttribute(
            .leafletBlock, in: NSRange(location: 0, length: textStorage.length)
        ) { v, r, stop in
            if (v as? BlockInfo)?.id == id {
                found = r
                stop.pointee = true
            }
        }
        return found.map { (textStorage.string as NSString).paragraphRange(for: $0) }
    }

    /// replaceBlocks lays out the text in view again and puts the blocks
    /// on it, the rest of the page as it is: after a long jump, the text
    /// it passed was laid out by estimate.
    func replaceBlocks() {
        viewportStale = true
        setNeedsLayout()
    }

    init() {
        super.init(frame: .zero, textContainer: nil)
        alwaysBounceVertical = true
        keyboardDismissMode = .interactive
        textContainer.lineFragmentPadding = 0
        allowsEditingTextAttributes = false
        smartInsertDeleteType = .yes
        // "--" stays two dashes, as on the desktop, for "---" to make a
        // divider and "- " a bullet.
        smartDashesType = .no
        dataDetectorTypes = []
        backgroundColor = .clear
        accessibilityIdentifier = "pageText"
        NotificationCenter.default.addObserver(
            self, selector: #selector(keyboardChanged(_:)),
            name: UIResponder.keyboardWillChangeFrameNotification, object: nil)
        NotificationCenter.default.addObserver(
            self, selector: #selector(keyboardChanged(_:)),
            name: UIResponder.keyboardWillHideNotification, object: nil)
    }

    /// keyboardChanged keeps the text clear of a keyboard, or its floating
    /// shortcuts bar, where it covers the text view, and of nothing else:
    /// the page runs on under them.
    @objc private func keyboardChanged(_ note: Notification) {
        guard let window,
            let end = note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect
        else { return }
        let keyboard = convert(window.convert(end, from: nil), from: window)
        let hidden = note.name == UIResponder.keyboardWillHideNotification
        let visibleBottom = bounds.maxY
        let covered = hidden ? 0 : max(0, visibleBottom - keyboard.minY)
        contentInset.bottom = covered
        verticalScrollIndicatorInsets.bottom = covered
    }

    required init?(coder: NSCoder) { fatalError("not coded") }

    /// becomeFirstResponder takes the focus, unless it is being refused.
    override func becomeFirstResponder() -> Bool {
        refusesFocus ? false : super.becomeFirstResponder()
    }

    /// caretRect is the caret as tall as the text it stands in, on its
    /// baseline: a line's own height takes in the room between lines.
    override func caretRect(for position: UITextPosition) -> CGRect {
        let rect = super.caretRect(for: position)
        let storage = textStorage
        let offset = self.offset(from: beginningOfDocument, to: position)
        guard storage.length > 0, let tlm = textLayoutManager else { return rect }
        // The text the caret follows sets its size, or, at a paragraph's
        // start, the text it leads.
        let text = storage.string as NSString
        var at = min(offset, storage.length - 1)
        if offset > 0, offset - 1 < text.length, text.character(at: offset - 1) != 0x0A {
            at = offset - 1
        }
        guard text.character(at: at) != 0xFFFC,
            let font = storage.attribute(.font, at: at, effectiveRange: nil) as? UIFont
        else { return rect }
        let height = font.ascender - font.descender
        guard rect.height > height + 1,
            let location = tlm.location(tlm.documentRange.location, offsetBy: offset),
            let fragment = tlm.textLayoutFragment(for: location)
        else { return rect }
        let top = fragment.layoutFragmentFrame.minY + textContainerInset.top
        let mid = rect.midY - top
        guard
            let line = fragment.textLineFragments.first(where: {
                $0.typographicBounds.minY <= mid && mid <= $0.typographicBounds.maxY
            })
        else { return rect }
        let baseline = top + line.typographicBounds.minY + line.glyphOrigin.y
        return CGRect(x: rect.minX, y: baseline - font.ascender, width: rect.width, height: height)
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        let side = max(20, (bounds.width - Self.readableWidth) / 2)
        var top: CGFloat = 12
        if let header {
            let width = bounds.width - side * 2
            let size = CGSize(width: width, height: measureHeader?(width) ?? header.bounds.height)
            header.frame = CGRect(
                x: side, y: 12, width: bounds.width - side * 2, height: size.height)
            top = header.frame.maxY + 8
        }
        let footerHeight = footer == nil ? 0 : measureFooter?(bounds.width - side * 2) ?? 0
        let inset = UIEdgeInsets(
            top: top, left: side, bottom: max(120, footerHeight + 40), right: side)
        if textContainerInset != inset { textContainerInset = inset }
        // At a new width (a column shown or hidden, the window resized)
        // all of the page is laid out again: TextKit keeps paragraphs out
        // of view as they were, with blocks sized for the old width, which
        // then overlap the text.
        let width = bounds.width - side * 2
        if abs(width - laidOutWidth) > 0.5, let tlm = textLayoutManager {
            laidOutWidth = width
            tlm.invalidateLayout(for: tlm.documentRange)
            viewportStale = true
        }
        if viewportStale, let tlm = textLayoutManager {
            viewportStale = false
            tlm.textViewportLayoutController.layoutViewport()
        }
        // Each scroll and layout puts the blocks in view where they are.
        blocks.layout(in: self)
        if let footer {
            // Under the text's last line.
            let end = caretRect(for: endOfDocument).maxY
            footer.frame = CGRect(
                x: side, y: end + 16, width: bounds.width - side * 2, height: footerHeight)
            footer.isHidden = footerHeight < 1
        }
    }

    /// The text takes the keyboard by a tap, not by the focus system's
    /// moves: with a hardware keyboard the system would give the keyboard
    /// back to the text it last focused as views of the page appear, away
    /// from the title being edited in the header.
    override var canBecomeFocused: Bool { false }

    // MARK: Keyboard

    override var keyCommands: [UIKeyCommand]? {
        if controller?.menu != nil {
            // The menu at the caret takes the arrows and Escape while it
            // shows; Return and Tab come as text, which it takes too.
            return [
                key(UIKeyCommand.inputUpArrow, [], #selector(menuUp), ""),
                key(UIKeyCommand.inputDownArrow, [], #selector(menuDown), ""),
                key(UIKeyCommand.inputEscape, [], #selector(menuClose), L.commonClose),
            ] + (super.keyCommands ?? [])
        }
        var commands: [UIKeyCommand] = [
            key("\t", [], #selector(indent), LI.indent),
            key("\t", .shift, #selector(outdent), LI.outdent),
        ]
        // Escape ends a selection, as on the desktop.
        if selectedRange.length > 0 {
            commands.append(key(UIKeyCommand.inputEscape, [], #selector(collapseSelection), ""))
        }
        // On iPad these are in the menu bar's Format menu; an iPhone with
        // a keyboard has no menu bar, so the text view takes them.
        if traitCollection.userInterfaceIdiom == .phone {
            commands += [
                key("e", .command, #selector(toggleCode), L.editorInlineCode),
                key("x", [.command, .shift], #selector(toggleStrike), LI.strikethrough),
                key("k", .command, #selector(editLink), L.editorLink),
                key(
                    UIKeyCommand.inputUpArrow, [.alternate, .shift], #selector(moveBlockUp),
                    LI.moveBlockUp),
                key(
                    UIKeyCommand.inputDownArrow, [.alternate, .shift], #selector(moveBlockDown),
                    LI.moveBlockDown),
                key("0", [.command, .alternate], #selector(makeText), L.hostSlashText),
                key("1", [.command, .alternate], #selector(makeHeading1), L.hostSlashHeading1),
                key("2", [.command, .alternate], #selector(makeHeading2), L.hostSlashHeading2),
                key("3", [.command, .alternate], #selector(makeHeading3), L.hostSlashHeading3),
                key("l", [.command, .shift], #selector(makeTodo), L.hostSlashTodo),
            ]
        }
        commands.append(contentsOf: super.keyCommands ?? [])
        return commands
    }

    private func key(
        _ input: String, _ flags: UIKeyModifierFlags, _ action: Selector, _ title: String
    ) -> UIKeyCommand {
        let c = UIKeyCommand(title: title, action: action, input: input, modifierFlags: flags)
        c.wantsPriorityOverSystemBehavior = true
        return c
    }

    @objc func menuUp() { controller?.moveInMenu(-1) }
    @objc func menuDown() { controller?.moveInMenu(1) }
    @objc func menuClose() { controller?.closeMenu() }
    @objc func collapseSelection() {
        selectedRange = NSRange(location: NSMaxRange(selectedRange), length: 0)
    }
    @objc func indent() { controller?.indent(by: 1) }
    @objc func outdent() { controller?.indent(by: -1) }
    @objc func toggleCode() { controller?.toggleInline(.leafletCode) }
    @objc func toggleStrike() { controller?.toggleInline(.strikethroughStyle) }
    @objc func editLink() { controller?.editLink() }
    @objc func moveBlockUp() { controller?.moveBlock(by: -1) }
    @objc func moveBlockDown() { controller?.moveBlock(by: 1) }
    @objc func makeText() { controller?.setType("paragraph") }
    @objc func makeHeading1() { controller?.setType("heading", ["level": .int(1)]) }
    @objc func makeHeading2() { controller?.setType("heading", ["level": .int(2)]) }
    @objc func makeHeading3() { controller?.setType("heading", ["level": .int(3)]) }
    @objc func makeTodo() { controller?.setType("todo_list", ["checked": .bool(false)]) }

    // The system's format commands (⌘B, ⌘I, ⌘U) set the block's formats,
    // which the style then shows, rather than the fonts themselves.
    override func toggleBoldface(_ sender: Any?) { controller?.toggleInline(.leafletBold) }
    override func toggleItalics(_ sender: Any?) { controller?.toggleInline(.leafletItalic) }
    override func toggleUnderline(_ sender: Any?) { controller?.toggleInline(.underlineStyle) }
}
