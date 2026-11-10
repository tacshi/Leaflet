import UIKit.UIGestureRecognizerSubclass

final class EditorTextView: UITextView {
    weak var controller: EditorController?
    let blocks = BlockViews()
    var header: UIView? {
        didSet {
            oldValue?.removeFromSuperview()
            if let header { addSubview(header) }
            remeasure()
        }
    }
    var measureHeader: ((CGFloat) -> CGFloat)?
    var footer: UIView? {
        didSet {
            oldValue?.removeFromSuperview()
            if let footer { addSubview(footer) }
            remeasure()
        }
    }
    var measureFooter: ((CGFloat) -> CGFloat)?
    static let readableWidth: CGFloat = 720
    static let narrowest: CGFloat = 120
    private static let leastGlide: CGFloat = 40
    var fold: ClosedRange<CGFloat>? {
        didSet { if fold != oldValue { setNeedsLayout() } }
    }
    private lazy var foldWatch = FoldWatch(view: self, wait: .milliseconds(300)) {
        [weak self] in self?.fold = $0
    }
    private var standing: Bool?
    private var standWait: Task<Void, Never>?
    private var typingAsItClosed = false
    private var hingeClosed = true
    private var pageLeft: (x: CGFloat, window: CGFloat)?
    private var laidOutWidth: CGFloat = 0
    private var laidOutSize = CGSize.zero
    private(set) var widthChanges = 0
    private var topLine: (offset: Int, into: CGFloat)?
    private var settling: Task<Void, Never>?
    private var viewportStale = false
    var refusesFocus = false
    private let emptyMark = EmptyPageMark()

    func relayout() {
        guard let tlm = textLayoutManager else { return }
        tlm.invalidateLayout(for: tlm.documentRange)
        viewportStale = true
        setNeedsLayout()
    }

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

    func replaceBlocks() {
        viewportStale = true
        setNeedsLayout()
    }

    init() {
        super.init(frame: .zero, textContainer: nil)
        addGestureRecognizer(finger)
        alwaysBounceVertical = true
        keyboardDismissMode = .interactive
        textContainer.lineFragmentPadding = 0
        allowsEditingTextAttributes = false
        smartInsertDeleteType = .yes
        smartDashesType = .no
        dataDetectorTypes = []
        linkTextAttributes = [:]
        blocks.editable = isEditable
        backgroundColor = .clear
        accessibilityIdentifier = "pageText"
        if #available(iOS 27.1, *) {
            addInteraction(
                UIHingeInteraction { [weak self] _, update in
                    guard let self else { return }
                    let closed = update.hinge?.status == .closed
                    if closed, !hingeClosed { displayClosed() }
                    if update.hinge != nil { hingeClosed = closed }
                    followStanding()
                    foldWatch.hingeMoved()
                })
        }
        NotificationCenter.default.addObserver(
            self, selector: #selector(keyboardChanged(_:)),
            name: UIResponder.keyboardWillChangeFrameNotification, object: nil)
        NotificationCenter.default.addObserver(
            self, selector: #selector(keyboardChanged(_:)),
            name: UIResponder.keyboardWillHideNotification, object: nil)
    }

    static func sides(width: CGFloat, fold: ClosedRange<CGFloat>?) -> (
        left: CGFloat, right: CGFloat
    ) {
        let side = max(20, (width - readableWidth) / 2)
        guard let fold, fold.lowerBound < width - side, fold.upperBound > side else {
            return (side, side)
        }
        let before = fold.lowerBound
        let after = width - fold.upperBound
        let room = max(before, after) - 40
        guard room >= 280, min(before, after) - 40 < 280 else { return (side, side) }
        let text = min(room, readableWidth)
        let margin = (max(before, after) - text) / 2
        return before >= after
            ? (margin, width - before + margin) : (fold.upperBound + margin, margin)
    }

    func displayClosed() {
        if isFirstResponder { typingAsItClosed = true }
    }

    private func followStanding() {
        let was = standing
        standing = standsAcross
        guard standing == true else {
            standWait?.cancel()
            standWait = nil
            return
        }
        guard was == false else { return }
        standWait = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(300))
            guard let self, !Task.isCancelled else { return }
            self.standWait = nil
            self.takeKeyboard()
        }
    }

    func takeKeyboard() {
        guard canTakeKeyboard, !isFirstResponder, !keyboardIsUp else { return }
        let view = bounds.inset(by: adjustedContentInset)
        if let topLine, let at = selectedTextRange?.end, !view.contains(caretRect(for: at).origin) {
            selectedRange = NSRange(location: min(topLine.offset, textStorage.length), length: 0)
        }
        _ = becomeFirstResponder()
    }

    private var canTakeKeyboard: Bool {
        isEditable && window?.windowScene?.activationState == .foregroundActive
            && window?.rootViewController?.presentedViewController == nil
    }

    private var keyboardIsUp: Bool {
        guard let end = Self.keyboardEnd, let screen = window?.windowScene?.screen,
            end.screen == nil || end.screen == screen
        else { return false }
        return end.frame.height > 0 && end.frame.minY < screen.bounds.height
    }

    private var standsAcross: Bool {
        guard #available(iOS 27.1, *) else { return false }
        return reservedRegions(kind: .division).contains { $0.frame.width > $0.frame.height }
    }

    private static var keyboardEnd: (frame: CGRect, screen: UIScreen?)?

    @objc private func keyboardChanged(_ note: Notification) {
        guard let end = note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect
        else { return }
        let hidden = note.name == UIResponder.keyboardWillHideNotification
        Self.keyboardEnd = hidden ? nil : (end, note.object as? UIScreen)
        clearKeyboard()
    }

    private func clearKeyboard() {
        guard let screen = window?.windowScene?.screen else { return }
        var covered: CGFloat = 0
        if let end = Self.keyboardEnd, end.screen == nil || end.screen == screen {
            // An iPad's floating keyboard, narrower than the screen and
            // off its bottom edge, covers no strip along the bottom to keep
            // clear of. Any other counts as before.
            let floating =
                end.frame.width < screen.bounds.width - 1
                && end.frame.maxY < screen.bounds.maxY - 1
            let keyboard = convert(end.frame, from: screen.coordinateSpace)
            covered = floating ? 0 : max(0, bounds.maxY - keyboard.minY)
        }
        guard contentInset.bottom != covered else { return }
        contentInset.bottom = covered
        verticalScrollIndicatorInsets.bottom = covered
    }

    required init?(coder: NSCoder) { fatalError("not coded") }

    override func becomeFirstResponder() -> Bool {
        refusesFocus ? false : super.becomeFirstResponder()
    }

    override func caretRect(for position: UITextPosition) -> CGRect {
        let rect = super.caretRect(for: position)
        let storage = textStorage
        let offset = self.offset(from: beginningOfDocument, to: position)
        guard storage.length > 0, let tlm = textLayoutManager else { return rect }
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

    override var frame: CGRect {
        didSet { if frame.size != oldValue.size { setNeedsLayout() } }
    }

    /// The fastest the page moves under a selection handle held at its edge,
    /// in points a second. The system speeds up without end the longer the
    /// handle is held there, soon past anything a finger can stop on a line.
    private static let fastestUnderHandle: CGFloat = 260
    private let finger = TouchWatch()
    private var movedAt: CFTimeInterval = 0

    private var extendingSelection: Bool {
        finger.down && selectedRange.length > 0 && !isDragging && !isDecelerating
    }

    override var contentOffset: CGPoint {
        get { super.contentOffset }
        set {
            let was = super.contentOffset
            let now = CACurrentMediaTime()
            defer { movedAt = now }
            guard extendingSelection, newValue.x == was.x else {
                super.contentOffset = newValue
                return
            }
            let since = min(max(now - movedAt, 1.0 / 120), 1.0 / 30)
            let most = Self.fastestUnderHandle * since
            let step = min(max(newValue.y - was.y, -most), most)
            super.contentOffset = CGPoint(x: newValue.x, y: was.y + step)
        }
    }

    override func layoutSubviews() {
        let animated = UIView.inheritedAnimationDuration > 0
        guard bounds.width != laidOutSize.width, let was = pageLeft,
            was.window == window?.bounds.width ?? 0,
            window?.windowScene?.effectiveGeometry.isInteractivelyResizing != true
        else {
            layOutPage()
            return
        }
        UIView.performWithoutAnimation { layOutPage() }
        guard let now = pageLeft else { return }
        let distance = was.x - now.x
        guard abs(distance) > (animated ? 0.5 : Self.leastGlide) else { return }
        UIView.performWithoutAnimation {
            transform = CGAffineTransform(translationX: distance, y: 0)
        }
        if animated {
            transform = .identity
        } else {
            UIView.animate(springDuration: 0.4, bounce: 0, options: .allowUserInteraction) {
                self.transform = .identity
            }
        }
    }

    private func layOutPage() {
        super.layoutSubviews()
        guard bounds.width >= Self.narrowest else { return }
        if bounds.size != laidOutSize {
            laidOutSize = bounds.size
            clearKeyboard()
            foldWatch.resized()
        }
        let (left, right) = Self.sides(width: bounds.width, fold: fold)
        pageLeft = (convert(CGPoint(x: left, y: 0), to: nil).x, window?.bounds.width ?? 0)
        var top: CGFloat = 12
        if let header {
            let width = bounds.width - left - right
            let height =
                height(of: header, width: width, measure: measureHeader, &headerSize)
                ?? header.bounds.height
            header.frame = CGRect(x: left, y: 12, width: width, height: height)
            top = header.frame.maxY + 8
        }
        top += leadingCardSpace
        let footerHeight =
            footer.flatMap {
                height(
                    of: $0, width: bounds.width - left - right, measure: measureFooter, &footerSize)
            } ?? 0
        let inset = UIEdgeInsets(
            top: top, left: left, bottom: max(120, footerHeight + 40), right: right)
        if textContainerInset != inset { textContainerInset = inset }
        let width = bounds.width - left - right
        if abs(width - laidOutWidth) > 0.5, let tlm = textLayoutManager {
            let resized = laidOutWidth > 0
            laidOutWidth = width
            widthChanges += 1
            textContainer.size.width = width
            tlm.invalidateLayout(for: tlm.documentRange)
            viewportStale = true
            if resized {
                keepPlace()
                Task { @MainActor [weak self] in self?.remeasure() }
            }
        }
        for view in subviews
        where view !== header && view !== footer && view !== emptyMark && !blocks.owns(view) {
            view.layoutIfNeeded()
        }
        if viewportStale, let tlm = textLayoutManager {
            viewportStale = false
            tlm.textViewportLayoutController.layoutViewport()
        }
        blocks.layout(in: self)
        if textStorage.length == 0, let (info, font, palette) = controller?.emptyPageBlock {
            if emptyMark.superview !== self { insertSubview(emptyMark, at: 0) }
            emptyMark.frame = CGRect(origin: .zero, size: bounds.size)
            emptyMark.mark = (info, font, palette, inset)
            emptyMark.caret = { [weak self] in
                guard let self else { return .zero }
                return self.caretRect(for: self.beginningOfDocument)
            }
            emptyMark.isHidden = false
            emptyMark.setNeedsDisplay()
        } else if !emptyMark.isHidden {
            emptyMark.isHidden = true
        }
        if let footer {
            let end = caretRect(for: endOfDocument).maxY
            footer.frame = CGRect(
                x: left, y: end + 16, width: bounds.width - left - right, height: footerHeight)
            footer.isHidden = footerHeight < 1
        }
        if settling == nil { takeTopLine() }
    }

    private func takeTopLine() {
        topLine = nil
        guard let tlm = textLayoutManager, textStorage.length > 0 else { return }
        let y = contentOffset.y + adjustedContentInset.top - textContainerInset.top
        guard y > 0, let fragment = tlm.textLayoutFragment(for: CGPoint(x: 0, y: y)) else { return }
        let top = y - fragment.layoutFragmentFrame.minY
        let lines = fragment.textLineFragments
        guard
            let line = lines.first(where: { $0.typographicBounds.maxY > top }) ?? lines.last
        else { return }
        let box = line.typographicBounds
        let start = tlm.offset(
            from: tlm.documentRange.location, to: fragment.rangeInElement.location)
        topLine = (
            start + line.characterRange.location,
            box.height > 0 ? (top - box.minY) / box.height : 0
        )
    }

    private func keepPlace() {
        guard controller?.headings.isJumping != true, !isDragging, !isDecelerating else { return }
        settling?.cancel()
        if window?.windowScene?.effectiveGeometry.isInteractivelyResizing != true { goToTopLine() }
        settling = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(150))
            guard let self, !Task.isCancelled else { return }
            self.settling = nil
            self.settle()
        }
    }

    private func settle() {
        guard !isDragging, !isDecelerating, controller?.headings.isJumping != true else { return }
        if topLine != nil {
            goToTopLine()
            let bottom = max(
                -adjustedContentInset.top,
                contentSize.height - bounds.height + adjustedContentInset.bottom)
            if contentOffset.y > bottom { contentOffset.y = bottom }
        }
        if typingAsItClosed {
            typingAsItClosed = false
            if canTakeKeyboard { _ = becomeFirstResponder() }
        }
        if isFirstResponder { scrollRangeToVisible(selectedRange) }
    }

    private func goToTopLine() {
        guard let (offset, into) = topLine, let tlm = textLayoutManager,
            offset <= textStorage.length
        else { return }
        let page = tlm.documentRange.location
        let paragraph = (textStorage.string as NSString).paragraphRange(
            for: NSRange(location: offset, length: 0))
        guard let at = tlm.location(page, offsetBy: offset),
            let end = tlm.location(page, offsetBy: NSMaxRange(paragraph)),
            let before = NSTextRange(location: page, end: end)
        else { return }
        tlm.ensureLayout(for: before)
        guard let fragment = tlm.textLayoutFragment(for: at) else { return }
        let inFragment = offset - tlm.offset(from: page, to: fragment.rangeInElement.location)
        let lines = fragment.textLineFragments
        guard
            let line = lines.last(where: { $0.characterRange.location <= inFragment })
                ?? lines.first
        else { return }
        let box = line.typographicBounds
        let y = fragment.layoutFragmentFrame.minY + box.minY + into * box.height
        contentOffset.y = y + textContainerInset.top - adjustedContentInset.top
    }

    private struct Measured {
        let width: CGFloat
        let ideal: CGSize
        let height: CGFloat
    }
    private var headerSize: Measured?
    private var footerSize: Measured?

    private func height(
        of view: UIView, width: CGFloat, measure: ((CGFloat) -> CGFloat)?,
        _ kept: inout Measured?
    ) -> CGFloat? {
        guard let measure else { return nil }
        let ideal = view.intrinsicContentSize
        if let kept, kept.width == width, kept.ideal == ideal { return kept.height }
        let height = measure(width)
        kept = Measured(width: width, ideal: ideal, height: height)
        return height
    }

    private var leadingCardSpace: CGFloat {
        guard textStorage.length > 0,
            let info = textStorage.attribute(.leafletBlock, at: 0, effectiveRange: nil)
                as? BlockInfo,
            info.type == "callout" || info.type == "code",
            let style = textStorage.attribute(.paragraphStyle, at: 0, effectiveRange: nil)
                as? NSParagraphStyle
        else { return 0 }
        return style.paragraphSpacingBefore
    }

    func remeasure() {
        headerSize = nil
        footerSize = nil
        setNeedsLayout()
    }

    override var canBecomeFocused: Bool { false }

    override var keyCommands: [UIKeyCommand]? {
        guard !(header?.holdsFirstResponder ?? false) else { return super.keyCommands }
        guard markedTextRange == nil else { return super.keyCommands }
        if controller?.menu != nil {
            return [
                key(UIKeyCommand.inputUpArrow, [], #selector(menuUp), ""),
                key(UIKeyCommand.inputDownArrow, [], #selector(menuDown), ""),
                key(UIKeyCommand.inputEscape, [], #selector(menuClose), L.commonClose),
            ] + (super.keyCommands ?? [])
        }
        var commands: [UIKeyCommand] = [
            key("\t", [], #selector(indent), LI.indent),
            key("\t", .shift, #selector(outdent), LI.outdent),
            key("\r", .shift, #selector(softBreak), ""),
        ]
        if selectedRange.length > 0 {
            commands.append(key(UIKeyCommand.inputEscape, [], #selector(collapseSelection), ""))
        }
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
                key(
                    "c", [.command, .alternate, .shift], #selector(copyPlainText),
                    L.editorCopyPlainText),
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
    @objc func softBreak() { controller?.softBreak() }
    @objc func outdent() { controller?.indent(by: -1) }
    @objc func toggleCode() { controller?.toggleInline(.leafletCode) }
    @objc func toggleStrike() { controller?.toggleInline(.leafletStrike) }
    @objc func editLink() { controller?.editLink() }
    @objc func moveBlockUp() { controller?.moveBlock(by: -1) }
    @objc func moveBlockDown() { controller?.moveBlock(by: 1) }
    @objc func makeText() { controller?.setType("paragraph") }
    @objc func makeHeading1() { controller?.setType("heading", ["level": .int(1)]) }
    @objc func makeHeading2() { controller?.setType("heading", ["level": .int(2)]) }
    @objc func makeHeading3() { controller?.setType("heading", ["level": .int(3)]) }
    @objc func makeTodo() { controller?.setType("todo_list", ["checked": .bool(false)]) }
    @objc func copyPlainText() { controller?.copyPlainText() }

    override func copy(_ sender: Any?) {
        controller?.copied()
        super.copy(sender)
        controller?.copiedOut()
    }

    /// True from a Paste until its text is put in: text typed, dictated or
    /// picked from the emoji keyboard that reads the same as what was last
    /// copied here is no paste, and takes none of the copy's formatting.
    /// The text of a Paste comes after `paste(_:)` has returned, once the
    /// pasteboard has been read, so the mark waits for it, for a moment.
    var isPasting: Bool { pasteStarted.map { Date().timeIntervalSince($0) < 2 } ?? false }
    private var pasteStarted: Date?

    override func paste(_ sender: Any?) {
        pasteStarted = Date()
        super.paste(sender)
    }

    func pasteTaken() { pasteStarted = nil }

    func whilePasting(_ run: () -> Void) {
        pasteStarted = Date()
        defer { pasteStarted = nil }
        run()
    }

    override func cut(_ sender: Any?) {
        controller?.copied()
        super.cut(sender)
        controller?.copiedOut()
    }

    override func toggleBoldface(_ sender: Any?) { controller?.toggleInline(.leafletBold) }
    override func toggleItalics(_ sender: Any?) { controller?.toggleInline(.leafletItalic) }
    override func toggleUnderline(_ sender: Any?) { controller?.toggleInline(.leafletUnderline) }

    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        switch action {
        case #selector(toggleBoldface(_:)), #selector(toggleItalics(_:)),
            #selector(toggleUnderline(_:)):
            return isEditable && isFirstResponder
        default:
            return super.canPerformAction(action, withSender: sender)
        }
    }

    override func deleteBackward() {
        if controller?.backspaceAtPageStart() == true { return }
        super.deleteBackward()
    }

    override func setMarkedText(_ markedText: String?, selectedRange: NSRange) {
        if markedTextRange == nil, !(markedText ?? "").isEmpty { controller?.roomToCompose() }
        super.setMarkedText(markedText, selectedRange: selectedRange)
    }

    override func setAttributedMarkedText(
        _ markedText: NSAttributedString?, selectedRange: NSRange
    ) {
        if markedTextRange == nil, (markedText?.length ?? 0) > 0 { controller?.roomToCompose() }
        super.setAttributedMarkedText(markedText, selectedRange: selectedRange)
    }

    override func unmarkText() {
        super.unmarkText()
        controller?.composed()
    }

    override var isEditable: Bool {
        didSet { if isEditable != oldValue { blocks.editable = isEditable } }
    }

    override var accessibilityCustomActions: [UIAccessibilityCustomAction]? {
        get {
            (controller?.accessibilityActions() ?? []) + (super.accessibilityCustomActions ?? [])
        }
        set { super.accessibilityCustomActions = newValue }
    }
}

private final class EmptyPageMark: UIView {
    var mark: (info: BlockInfo, font: UIFont, palette: Palette, inset: UIEdgeInsets)?
    var caret: () -> CGRect = { .zero }

    override init(frame: CGRect) {
        super.init(frame: frame)
        isOpaque = false
        backgroundColor = .clear
        isUserInteractionEnabled = false
    }

    required init?(coder: NSCoder) { fatalError("not coded") }

    override func draw(_ rect: CGRect) {
        guard let mark, let ctx = UIGraphicsGetCurrentContext() else { return }
        let top = mark.inset.top
        let caret = caret()
        BlockLayoutFragment.drawDecoration(
            mark.info, mark.palette, in: ctx, at: CGPoint(x: mark.inset.left, y: top), left: 0,
            width: bounds.width - mark.inset.left - mark.inset.right,
            line: BlockLayoutFragment.Line(
                baseline: caret.maxY + mark.font.descender - top, font: mark.font, ordinal: 1,
                top: caret.minY - top, bottom: caret.maxY - top))
    }
}

extension UIView {
    var holdsFirstResponder: Bool {
        isFirstResponder || subviews.contains { $0.holdsFirstResponder }
    }
}

/// Tells whether a finger is on the view, and takes no touch from anything.
private final class TouchWatch: UIGestureRecognizer, UIGestureRecognizerDelegate {
    private var touches = 0
    var down: Bool { touches > 0 }

    init() {
        super.init(target: nil, action: nil)
        cancelsTouchesInView = false
        delaysTouchesBegan = false
        delaysTouchesEnded = false
        delegate = self
    }

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
        self.touches += touches.count
    }

    override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) {
        lifted(touches.count)
    }

    override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent) {
        lifted(touches.count)
    }

    private func lifted(_ count: Int) {
        touches = max(touches - count, 0)
        if touches == 0 { state = .failed }
    }

    override func reset() { touches = 0 }

    override func canPrevent(_ other: UIGestureRecognizer) -> Bool { false }

    override func canBePrevented(by other: UIGestureRecognizer) -> Bool { false }

    func gestureRecognizer(
        _ one: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer
    ) -> Bool { true }
}
