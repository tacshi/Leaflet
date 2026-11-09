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
            remeasure()
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
            remeasure()
        }
    }
    var measureFooter: ((CGFloat) -> CGFloat)?
    static let readableWidth: CGFloat = 720
    /// narrowest is the least width the page lays out at.
    static let narrowest: CGFloat = 120
    /// leastGlide is the least the page glides by with no animation going
    /// on around it.
    private static let leastGlide: CGFloat = 40
    /// fold is where a half folded display folds down the view, from left
    /// to right, or nil: the text keeps to one side of it.
    var fold: ClosedRange<CGFloat>? {
        didSet { if fold != oldValue { setNeedsLayout() } }
    }
    private lazy var foldWatch = FoldWatch(view: self, wait: .milliseconds(300)) {
        [weak self] in self?.fold = $0
    }
    /// standing says the display is half folded across the view, as a
    /// laptop stands, or nil until the hinge has said; standWait waits to
    /// see that it stays.
    private var standing: Bool?
    private var standWait: Task<Void, Never>?
    /// typingAsItClosed says the text had the keyboard as the display
    /// closed; hingeClosed that the hinge last said it was closed, which
    /// it goes on saying while typing on the display that took over.
    private var typingAsItClosed = false
    private var hingeClosed = true
    /// pageLeft is where the text's left edge was last laid out in the
    /// window, and how wide the window was.
    private var pageLeft: (x: CGFloat, window: CGFloat)?
    /// laidOutWidth is the text's width the page was last laid out at.
    private var laidOutWidth: CGFloat = 0
    /// laidOutSize is the view's size as last laid out.
    private var laidOutSize = CGSize.zero
    /// widthChanges counts the widths the page was laid out at again: a
    /// scroll kept from before one no longer says where the text is.
    private(set) var widthChanges = 0
    /// topLine is the line at the top of the view: where its text starts,
    /// and how far into it the view is, as a part of its height, which
    /// stays the same place as the page wraps again. It is nil at the
    /// page's top.
    private var topLine: (offset: Int, into: CGFloat)?
    /// settling goes back to the line the page was at once a resize has
    /// stopped: TextKit corrects its estimates for a moment after, which
    /// moves the page. topLine stays as it was until then.
    private var settling: Task<Void, Never>?
    /// viewportStale says the text in view is to be laid out before the
    /// blocks are placed on it.
    private var viewportStale = false
    /// refusesFocus keeps the focus from coming back to the text while an
    /// embed that had it is taken out (BlockViews.takeOut).
    var refusesFocus = false
    /// emptyMark draws the block of an empty page, a list's bullet or a
    /// quote's bar, as the page's text has no paragraph to draw it.
    private let emptyMark = EmptyPageMark()

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
        // Links keep the style's look: the text view's own would recolor
        // them.
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

    /// sides are the room left of the text and right of it in a view of
    /// width: the text keeps to a readable width in the middle of the
    /// view. A fold across it with room for text on either side changes
    /// nothing, for the page to have both halves as it has open; with room
    /// on one side only, as beside the panel, the text keeps to that side.
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

    /// displayClosed notes the text had the keyboard, which the display
    /// closing takes from it, to have it back on the display that takes
    /// over.
    func displayClosed() {
        if isFirstResponder { typingAsItClosed = true }
    }

    /// followStanding brings up the keyboard as the display comes to stand
    /// as a laptop does, its lower half to type on, and only then: hidden,
    /// it stays hidden, and a page opened in that pose is only opened.
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

    /// takeKeyboard brings up the keyboard for the page, the page staying
    /// where it is: the caret goes to the text in view if it isn't there.
    /// With a sheet over the page, a keyboard in use elsewhere, or a page
    /// that can't be changed, there is nothing to do.
    func takeKeyboard() {
        guard canTakeKeyboard, !isFirstResponder, !keyboardIsUp else { return }
        let view = bounds.inset(by: adjustedContentInset)
        if let topLine, let at = selectedTextRange?.end, !view.contains(caretRect(for: at).origin) {
            selectedRange = NSRange(location: min(topLine.offset, textStorage.length), length: 0)
        }
        _ = becomeFirstResponder()
    }

    /// canTakeKeyboard says the page can be typed in and nothing is over it.
    private var canTakeKeyboard: Bool {
        isEditable && window?.windowScene?.activationState == .foregroundActive
            && window?.rootViewController?.presentedViewController == nil
    }

    /// keyboardIsUp says a keyboard, or its shortcuts bar, shows.
    private var keyboardIsUp: Bool {
        guard let end = Self.keyboardEnd, let screen = window?.windowScene?.screen,
            end.screen == nil || end.screen == screen
        else { return false }
        return end.frame.height > 0 && end.frame.minY < screen.bounds.height
    }

    /// standsAcross says the display is half folded from side to side of
    /// the view.
    private var standsAcross: Bool {
        guard #available(iOS 27.1, *) else { return false }
        return reservedRegions(kind: .division).contains { $0.frame.width > $0.frame.height }
    }

    /// keyboardEnd is where the keyboard last said it ends up, on the
    /// screen it said if it did, or nil with none: every page's, for one
    /// that comes or resizes with no word from the keyboard.
    private static var keyboardEnd: (frame: CGRect, screen: UIScreen?)?

    /// keyboardChanged keeps the text clear of a keyboard, or its floating
    /// shortcuts bar, where it covers the text view, and of nothing else:
    /// the page runs on under them.
    @objc private func keyboardChanged(_ note: Notification) {
        guard let end = note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect
        else { return }
        let hidden = note.name == UIResponder.keyboardWillHideNotification
        Self.keyboardEnd = hidden ? nil : (end, note.object as? UIScreen)
        clearKeyboard()
    }

    /// clearKeyboard makes room for the keyboard where it now covers the
    /// text view.
    private func clearKeyboard() {
        guard let screen = window?.windowScene?.screen else { return }
        var covered: CGFloat = 0
        // A keyboard on another screen, or the other display, covers
        // nothing here.
        if let end = Self.keyboardEnd, end.screen == nil || end.screen == screen {
            // The keyboard's frame is the screen's: a window on iPad, in
            // Stage Manager or Split View, is only part of it.
            let keyboard = convert(end.frame, from: screen.coordinateSpace)
            covered = max(0, bounds.maxY - keyboard.minY)
        }
        guard contentInset.bottom != covered else { return }
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

    // A rotation that takes the bar by the camera to the other side ends
    // with a size UIKit lays nothing out for, which would leave the title
    // and the blocks at the size before it.
    override var frame: CGRect {
        didSet { if frame.size != oldValue.size { setNeedsLayout() } }
    }

    override func layoutSubviews() {
        // A column shown or hidden gives this view its new place at once.
        // What is laid out here would then take an animation going on from
        // where it was, going past where the page is going and coming
        // back; and with none going on, as an inspector closes, the page
        // would be there before the column has moved. All of it goes there
        // at once instead, and the view glides there from where it was.
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
        // A column dragged wider moves the page by a little on each frame,
        // which it follows as it goes.
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
        // A split view opening gives its column next to no width for a
        // frame. Laid out at it, the title measures as a tower, and keeps
        // that height at the width that follows.
        guard bounds.width >= Self.narrowest else { return }
        // A window resized may move the page against a keyboard that
        // stays, which says nothing.
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
        // At a new width (a column shown or hidden, the window resized)
        // all of the page is laid out again: TextKit keeps paragraphs out
        // of view as they were, with blocks sized for the old width, which
        // then overlap the text.
        let width = bounds.width - left - right
        if abs(width - laidOutWidth) > 0.5, let tlm = textLayoutManager {
            let resized = laidOutWidth > 0
            laidOutWidth = width
            widthChanges += 1
            // The text view follows its width in its own time, which can
            // be after this pass and any other: laid out now, the text
            // would wrap again then, and the blocks stay sized for this.
            textContainer.size.width = width
            tlm.invalidateLayout(for: tlm.documentRange)
            viewportStale = true
            if resized {
                keepPlace()
                // The title, a text field, measures as it last laid out
                // until it has laid out at the new width.
                Task { @MainActor [weak self] in self?.remeasure() }
            }
        }
        // TextKit lays out the text in view as the text view's canvas, a
        // view inside it, lays out, which UIKit does only after this. Text
        // laid out there for the first time, as it comes into view, moves
        // what follows from where its estimate had it: laid out now, the
        // blocks go where the text is, not where it was, and stay there
        // once the page is still.
        for view in subviews
        where view !== header && view !== footer && view !== emptyMark && !blocks.owns(view) {
            view.layoutIfNeeded()
        }
        if viewportStale, let tlm = textLayoutManager {
            viewportStale = false
            tlm.textViewportLayoutController.layoutViewport()
        }
        // Each scroll and layout puts the blocks in view where they are.
        blocks.layout(in: self)
        if textStorage.length == 0, let (info, font, palette) = controller?.emptyPageBlock {
            if emptyMark.superview !== self { insertSubview(emptyMark, at: 0) }
            // In the text's own space, which the scroll offsets the bounds
            // from.
            emptyMark.frame = CGRect(origin: .zero, size: bounds.size)
            emptyMark.mark = (info, font, palette, inset)
            // Where the caret stands once the empty line is laid out, as
            // it is by the time the mark draws.
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
            // Under the text's last line.
            let end = caretRect(for: endOfDocument).maxY
            footer.frame = CGRect(
                x: left, y: end + 16, width: bounds.width - left - right, height: footerHeight)
            footer.isHidden = footerHeight < 1
        }
        if settling == nil { takeTopLine() }
    }

    /// takeTopLine notes the line at the top of the view, here and not as
    /// the page scrolls: a resize scrolls it too, before it lays out.
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

    /// keepPlace keeps the text the page was at at the top of the view as
    /// the page wraps again, as a resize goes, and once more when it has
    /// stopped: at another width the same scroll is other text.
    private func keepPlace() {
        guard controller?.headings.isJumping != true, !isDragging, !isDecelerating else { return }
        settling?.cancel()
        // By hand, a window goes through a width on each frame: going there
        // for each would lay out all the text above it each time.
        if window?.windowScene?.effectiveGeometry.isInteractivelyResizing != true { goToTopLine() }
        settling = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(150))
            guard let self, !Task.isCancelled else { return }
            self.settling = nil
            self.settle()
        }
    }

    /// settle goes back to the line the page was at, as far as the page
    /// goes, as its height is known by now, and gives the keyboard back to
    /// the text that had it as the display closed, the caret in view.
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

    /// goToTopLine scrolls the line the page was at back to the top of the
    /// view, laid out down to its paragraph's end so that its place is
    /// known rather than estimated.
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

    /// Measured is the header's or the footer's height at a width, measured
    /// as it would take ideal on its own.
    private struct Measured {
        let width: CGFloat
        let ideal: CGSize
        let height: CGFloat
    }
    /// headerSize and footerSize are the header's and the footer's heights
    /// as last measured, kept while the width and what they show stay: the
    /// text view lays out on each frame it scrolls, and SwiftUI measuring
    /// them each time slows it.
    private var headerSize: Measured?
    private var footerSize: Measured?

    /// height is view's height at width, as measure says, or as last
    /// measured: its own ideal size, which its hosting controller keeps as
    /// what it shows changes, is as it was then. It is nil without measure.
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

    /// leadingCardSpace is the spacing before the text's first block when it
    /// is a card, a callout or code, which draws above its text: TextKit
    /// gives a text's first paragraph none, and the card would run into the
    /// header.
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

    /// remeasure has the header and the footer measured again as they lay
    /// out next, as what they show changed.
    func remeasure() {
        headerSize = nil
        footerSize = nil
        setNeedsLayout()
    }

    /// The text takes the keyboard by a tap, not by the focus system's
    /// moves: with a hardware keyboard the system would give the keyboard
    /// back to the text it last focused as views of the page appear, away
    /// from the title being edited in the header.
    override var canBecomeFocused: Bool { false }

    // MARK: Keyboard

    override var keyCommands: [UIKeyCommand]? {
        // The title, in the header, has these too, up its responder chain:
        // they are the text's alone.
        guard !(header?.holdsFirstResponder ?? false) else { return super.keyCommands }
        // Text being composed has the keys: Tab, Return, the arrows and
        // Escape choose among its candidates, or end it.
        guard markedTextRange == nil else { return super.keyCommands }
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
            // A line break within the block, as on the desktop.
            key("\r", .shift, #selector(softBreak), ""),
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

    /// Copy and Cut take the selection as the system does, and the editor
    /// keeps it as Leaflet's own copy, which pastes back as it was.
    override func copy(_ sender: Any?) {
        controller?.copied()
        super.copy(sender)
        controller?.copiedOut()
    }

    override func cut(_ sender: Any?) {
        controller?.copied()
        super.cut(sender)
        controller?.copiedOut()
    }

    // The system's format commands (⌘B, ⌘I, ⌘U) set the block's formats,
    // which the style then shows, rather than the fonts themselves.
    override func toggleBoldface(_ sender: Any?) { controller?.toggleInline(.leafletBold) }
    override func toggleItalics(_ sender: Any?) { controller?.toggleInline(.leafletItalic) }
    override func toggleUnderline(_ sender: Any?) { controller?.toggleInline(.leafletUnderline) }

    /// canPerformAction offers the format commands while the text can be
    /// changed: UIKit offers them only to a text view that edits its
    /// attributes itself, which this one leaves to the editor.
    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        switch action {
        case #selector(toggleBoldface(_:)), #selector(toggleItalics(_:)),
            #selector(toggleUnderline(_:)):
            return isEditable && isFirstResponder
        default:
            return super.canPerformAction(action, withSender: sender)
        }
    }

    /// deleteBackward follows Backspace at the start of the page too, which
    /// UIKit doesn't ask about, there being nothing before it to delete.
    override func deleteBackward() {
        if controller?.backspaceAtPageStart() == true { return }
        super.deleteBackward()
    }

    /// setMarkedText starts a composition by a block without text in a new
    /// paragraph after it, as text typed there goes.
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

    /// unmarkText ends a composition, which may leave the text as it was:
    /// the editor hears of no change then.
    override func unmarkText() {
        super.unmarkText()
        controller?.composed()
    }

    override var isEditable: Bool {
        didSet { if isEditable != oldValue { blocks.editable = isEditable } }
    }

    // VoiceOver can't reach the views and markers in the text: what a tap
    // on them does is offered as actions at the caret.
    override var accessibilityCustomActions: [UIAccessibilityCustomAction]? {
        get {
            (controller?.accessibilityActions() ?? []) + (super.accessibilityCustomActions ?? [])
        }
        set { super.accessibilityCustomActions = newValue }
    }
}

/// EmptyPageMark draws an empty page's block, as BlockLayoutFragment draws
/// a paragraph's, on the line the caret stands on.
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
    /// holdsFirstResponder reports whether the view or one in it has the
    /// keyboard.
    var holdsFirstResponder: Bool {
        isFirstResponder || subviews.contains { $0.holdsFirstResponder }
    }
}
