import LeafletCore
import UIKit

/// EditorController edits a page in an EditorTextView: it keeps each
/// paragraph one block, follows the editing rules of the desktop's editor
/// (Enter, Backspace, Tab, Markdown shortcuts), and passes the page's
/// content to the core shortly after each edit, and the core's changes
/// from sync and agents into the text without losing the caret.
@MainActor
final class EditorController: NSObject, UITextViewDelegate,
    @preconcurrency NSTextLayoutManagerDelegate,
    @preconcurrency NSTextContentStorageDelegate,
    UIGestureRecognizerDelegate, @preconcurrency UIEditMenuInteractionDelegate,
    UIContextMenuInteractionDelegate
{
    let textView = EditorTextView()
    /// selectionMenu shows the selection's menu on a long press in it.
    private lazy var selectionMenu = UIEditMenuInteraction(delegate: self)
    var style: EditorStyle {
        didSet {
            BlockLook.palette = style.palette
            BlockLook.textSize = style.textSize
            // A theme changed draws the page again in it, its text and its
            // blocks: those drawn before keep the colors they were made
            // in. iOS changes the appearance for a moment too, as it takes
            // the app's pictures for the app switcher.
            let restyled = oldValue.palette.key != style.palette.key
            if restyled { textView.blocks.reset() }
            if restyled || oldValue.textSize != style.textSize {
                restyleAll()
                textView.relayout()
            }
            // A block the top of the view cuts is not in the first pass's
            // text: the next one puts it back.
            if restyled {
                DispatchQueue.main.async { [weak self] in self?.textView.replaceBlocks() }
            }
        }
    }
    /// onEdit takes the page's content after the user's edits.
    var onEdit: ((DocContent) -> Void)?
    /// onComment starts a comment on a block, quoting the text chosen.
    var onComment: ((_ blockID: String, _ quote: String) -> Void)?
    /// onAsk asks an agent about the blocks chosen and the text in them:
    /// the one chosen in "@", or nil for the default.
    var onAsk: ((_ focus: [String], _ selection: String, _ agent: AgentState.Option?) -> Void)?
    /// onAddAgent opens Settings to add an agent provider.
    var onAddAgent: (() -> Void)?
    /// onHeading says which heading the page is at as it scrolls or
    /// changes, as the desktop's outline shows it: the last one whose top
    /// is up to just under the top of the view, or nil above the first.
    var onHeading: ((String?) -> Void)?
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
    /// onEditing says the text took the keyboard, or gave it up.
    var onEditing: ((Bool) -> Void)?
    /// onBlank says the page became blank, one empty paragraph, or no
    /// longer is: a blank page offers ways to start it.
    var onBlank: ((Bool) -> Void)? {
        didSet { onBlank?(isBlank) }
    }
    private var wasBlank: Bool?
    /// onEditBlock opens a block's editor, from its long press's menu.
    var onEditBlock: (@MainActor (DocNode) -> Void)? {
        didSet { textView.blocks.edit = onEditBlock }
    }

    /// pageLinks open the pages subpages' cards show.
    var pageLinks: PageLinks? {
        didSet { textView.blocks.links = pageLinks }
    }
    /// meta is the page's meta as the core last sent it.
    private var meta: JSONValue?
    /// trailing is the block of the last paragraph when it is empty: no
    /// character holds it.
    private var trailing: BlockInfo?
    /// bypass lets the controller's own insertions through the rules.
    private var bypass = false
    private var sendTask: Task<Void, Never>?
    /// typing are the block and formats what is typed next takes. The
    /// text view's own typing attributes drop the editor's keys, so the
    /// editor sets them on typed text itself.
    private var typing: [NSAttributedString.Key: Any] = [:]
    /// typed is where text went in that the rules let through, for
    /// textViewDidChange to give it the typing attributes.
    private var typed: NSRange?
    /// plainAt is the caret after a format typed as Markdown, where typing
    /// goes on without it.
    private var plainAt: Int?
    /// menu is the menu at the caret while "/" or "@" is being typed, and
    /// opening is where one was just typed, for textViewDidChange to open.
    private(set) var menu: CaretMenu?
    private var opening: (Int, Character)?
    /// createSubpage makes a page in this one, for the / menu's Subpage,
    /// and returns its ID.
    var createSubpage: (() async -> String?)?
    /// menuItems gives the menu for "/" or "@" what it offers, as the
    /// core's JSON (CoreClient.menuItems), once it has it.
    var menuItems: ((Character, @escaping @MainActor (String) -> Void) -> Void)?
    /// agents are the agents "@" can ask, for the one picked.
    var agents: (() -> [AgentState.Option])?
    /// author is the user's name, which a chart made here carries.
    var author: (() -> String)?
    /// onMenuItem does a "/" item the page does itself, a template, a file
    /// or a recording, at the block the "/" was in.
    var onMenuItem: ((_ key: String, _ blockID: String) -> Void)?

    /// formatKeys are the inline formats typing carries on.
    private static let formatKeys: [NSAttributedString.Key] = [
        .leafletBold, .leafletItalic, .leafletCode, .underlineStyle, .strikethroughStyle,
        .leafletColor, .leafletBackground,
    ]

    init(style: EditorStyle) {
        self.style = style
        BlockLook.palette = style.palette
        BlockLook.textSize = style.textSize
        super.init()
        textView.controller = self
        textView.delegate = self
        textView.textLayoutManager?.delegate = self
        // It folds the blocks inside a closed toggle out of the layout.
        if let content = textView.textLayoutManager?.textContentManager as? NSTextContentStorage,
            content.delegate == nil
        {
            content.delegate = self
        }
        let tap = UITapGestureRecognizer(target: self, action: #selector(tapMarker(_:)))
        tap.delegate = self
        textView.addGestureRecognizer(tap)
        // A tap ends a selection; a long press in it shows its menu.
        let press = UILongPressGestureRecognizer(
            target: self, action: #selector(pressSelection(_:)))
        press.delegate = self
        textView.addGestureRecognizer(press)
        textView.addInteraction(selectionMenu)
        // A long press on a chart, a formula, a diagram or an embed offers
        // to edit it, where the desktop shows Edit on hover.
        textView.addInteraction(UIContextMenuInteraction(delegate: self))
        installKeyboardBar()
    }

    private var storage: NSTextStorage { textView.textStorage }

    // MARK: Content in and out

    /// load shows content, as the page opens.
    func load(_ content: DocContent, tinted: Set<String>) {
        meta = content.meta
        let (text, trailing) = DocumentMapper.attributedString(content, tinted: tinted)
        self.trailing = trailing
        storage.setAttributedString(text)
        restyleAll()
        // The page as it opens is no step to undo.
        textView.undoManager?.removeAllActions()
        if storage.length == 0 { self.trailing = self.trailing ?? BlockInfo.paragraph() }
        textView.selectedRange = NSRange(location: 0, length: 0)
        updateTypingAttributes()
        updateFolds()
        reportBlank()
        DispatchQueue.main.async { [weak self] in self?.updateHeading() }
    }

    /// isBlank reports whether the page is one empty paragraph.
    var isBlank: Bool { storage.length == 0 && (trailing?.type ?? "paragraph") == "paragraph" }

    private func reportBlank() {
        let blank = isBlank
        guard blank != wasBlank else { return }
        wasBlank = blank
        onBlank?(blank)
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        if !jumping { updateHeading() }
    }

    func scrollViewDidEndScrollingAnimation(_ scrollView: UIScrollView) {
        // Where the text now is, once laid out there, again until it stays.
        let landing = jumpTo != nil
        while landJump() {}
        jumping = false
        if !(landing && jumpShort) { updateHeading() }
        // A long jump passes text laid out by estimate: the blocks go
        // again where the text in view now is, not over it.
        textView.replaceBlocks()
    }

    func scrollViewWillBeginDragging(_ scrollView: UIScrollView) {
        jumping = false
        jumpTo = nil
        jumpLandings = 0
    }

    /// updateHeading finds the heading the page is at, and says so if it
    /// changed.
    func updateHeading() {
        guard let tlm = textView.textLayoutManager else { return }
        // A heading counts once its top is within this of the view's top,
        // where going to it from the outline leaves it.
        let margin: CGFloat = 24
        let y =
            textView.contentOffset.y + textView.adjustedContentInset.top + margin
            - textView.textContainerInset.top
        var found: String?
        if y >= 0, storage.length > 0,
            let fragment = tlm.textLayoutFragment(for: CGPoint(x: 0, y: y))
        {
            let offset = tlm.offset(
                from: tlm.documentRange.location, to: fragment.rangeInElement.location)
            let end = min(offset + 1, storage.length)
            storage.enumerateAttribute(.leafletBlock, in: NSRange(location: 0, length: end)) {
                value, _, _ in
                if let b = value as? BlockInfo, b.type == "heading" { found = b.id }
            }
        }
        guard found != atHeading else { return }
        atHeading = found
        onHeading?(found)
    }

    /// update brings content from outside the editor (sync, an agent, a
    /// merge) into the text: only the blocks that changed, where the
    /// blocks are the same; all of it otherwise. The caret stays in its
    /// block, and the page where it is: an agent writing does not scroll
    /// it, the user does.
    func update(_ content: DocContent, tinted: Set<String>) {
        defer { reportBlank() }
        sendTask?.cancel()
        meta = content.meta
        let caret = caretPosition()
        let offset = textView.contentOffset
        let (text, newTrailing) = DocumentMapper.attributedString(content, tinted: tinted)
        let old = DocumentMapper.paragraphs(storage, trailing: trailing)
        let new = DocumentMapper.paragraphs(text, trailing: newTrailing)
        var changed = false
        do {
            storage.beginEditing()
            if old.map(\.info.id) == new.map(\.info.id) {
                for (o, n) in zip(old, new).reversed() {
                    let same =
                        DocumentMapper.node(for: o.info, text: o.text)
                        == DocumentMapper.node(for: n.info, text: n.text)
                        && (o.text.length == 0 || n.text.length == 0
                            || (o.text.attribute(.leafletTinted, at: 0, effectiveRange: nil) != nil)
                                == (n.text.attribute(.leafletTinted, at: 0, effectiveRange: nil)
                                    != nil))
                    if same { continue }
                    changed = true
                    storage.replaceCharacters(in: o.range, with: n.text)
                    // The separator after the paragraph is its block's too.
                    let sep = o.range.location + n.text.length
                    if sep < storage.length {
                        storage.addAttribute(
                            .leafletBlock, value: n.info, range: NSRange(location: sep, length: 1))
                    }
                }
            } else {
                storage.setAttributedString(text)
                changed = true
            }
            trailing = newTrailing
            storage.endEditing()
            restyleAll()
        }
        // Changes made to the storage itself are no undo steps: the text
        // view records only typing and its own edits. Those steps were
        // made over text that has moved since, which they no longer fit.
        if changed { textView.undoManager?.removeAllActions() }
        restoreCaret(caret)
        updateFolds()
        if changed { hold(offset) }
    }

    /// hold keeps the page scrolled to offset after a change from outside,
    /// which the text view follows by scrolling to the caret, then and
    /// once laid out. A page the user is scrolling goes where they take it.
    private func hold(_ offset: CGPoint) {
        textView.layoutIfNeeded()
        scroll(to: offset)
        DispatchQueue.main.async { [weak self] in self?.scroll(to: offset) }
    }

    /// scroll puts the page at offset, as far as it goes, unless the user
    /// is scrolling it.
    private func scroll(to offset: CGPoint) {
        let tv = textView
        guard !tv.isDragging, !tv.isDecelerating else { return }
        let bottom = max(
            -tv.adjustedContentInset.top,
            tv.contentSize.height - tv.bounds.height + tv.adjustedContentInset.bottom)
        tv.setContentOffset(CGPoint(x: offset.x, y: min(offset.y, bottom)), animated: false)
    }

    // MARK: Folding

    /// folded are the blocks folded away, inside a closed toggle or the
    /// section of a folded heading, which the page leaves out of its
    /// layout: they stay in its text and its content.
    private var folded: Set<String> = []
    /// dateOffer is the to-do whose chip offers a due date: the one the
    /// caret is in, or whose date is open.
    private var dateOffer: String?
    private var dueOpen: String?

    /// updateFolds finds the blocks folded away, as a toggle or heading
    /// folds or the blocks change, and lays the page out again if they
    /// did. A folded heading's section is as the desktop's: the blocks
    /// after it at its depth, and those in them, up to the next heading
    /// of its level or higher.
    private func updateFolds() {
        var hidden = Set<String>()
        var toggle: Int?
        var heading: (depth: Int, level: Int)?
        for p in DocumentMapper.paragraphs(storage, trailing: trailing) {
            let info = p.info
            if let h = heading,
                info.depth < h.depth
                    || (info.depth == h.depth && info.type == "heading" && info.level <= h.level)
            {
                heading = nil
            }
            if let t = toggle, info.depth <= t { toggle = nil }
            if heading != nil || toggle != nil {
                hidden.insert(info.id)
                continue
            }
            if info.data["collapsed"]?.bool == true {
                if info.type == "toggle_list" { toggle = info.depth }
                if info.type == "heading" { heading = (info.depth, info.level) }
            }
        }
        guard hidden != folded else { return }
        folded = hidden
        textView.relayout()
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

    /// edited follows each change of the text: it keeps each paragraph
    /// one block, shows the paragraphs changed, and sends the page soon.
    private func edited(_ range: NSRange) {
        normalize(around: range)
        style.apply(to: storage, in: range)
        restyleNumbersAfter(range)
        updateTypingAttributes()
        updateFolds()
        reportBlank()
        sendTask?.cancel()
        sendTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(250))
            guard !Task.isCancelled else { return }
            self?.send()
        }
    }

    /// agentMenu asks an agent about the text chosen, or comments on it.
    private func agentMenu() -> UIMenu {
        UIMenu(
            options: .displayInline,
            children: [
                UIAction(title: L.editorAskAgent, image: UIImage(systemName: "sparkles")) {
                    [weak self] _ in
                    guard let self else { return }
                    self.onAsk?(self.focusBlockIDs(), self.selectedText(), nil)
                },
                UIAction(title: L.panelTabComments, image: UIImage(systemName: "text.bubble")) {
                    [weak self] _ in
                    guard let self else { return }
                    let (_, info) = self.paragraph(at: self.textView.selectedRange.location)
                    self.onComment?(info.id, self.selectedText())
                },
            ])
    }

    /// selectedText is the text chosen, its line breaks as newlines.
    func selectedText() -> String {
        (storage.string as NSString).substring(with: textView.selectedRange)
            .replacingOccurrences(of: String(lineBreak), with: "\n")
            .replacingOccurrences(of: "\u{FFFC}", with: "")
    }

    /// focusBlockIDs are the blocks the selection touches, or the caret's.
    func focusBlockIDs() -> [String] {
        var ids: [String] = []
        for (_, info) in selectedParagraphs() where !ids.contains(info.id) { ids.append(info.id) }
        return ids
    }

    /// reveal scrolls to a block and puts the caret at its start.
    /// atTop brings the block to the top of the page, as the outline goes
    /// to a heading, rather than only into view.
    /// setData gives a block without text new data, as its editor saved
    /// it, in its place.
    func setData(_ data: [String: JSONValue], ofBlock id: String) {
        replaceBlock(DocNode(id: id, type: "", data: data, children: nil))
    }

    /// replaceBlock puts node in place of the block without text of its
    /// ID, as a table's sheet gives it with its cells; a node of no type
    /// keeps the block's.
    func replaceBlock(_ node: DocNode) {
        let all = DocumentMapper.paragraphs(storage, trailing: trailing)
        guard let p = all.first(where: { $0.info.id == node.id }), !p.info.isText else { return }
        var node = node
        if node.type.isEmpty { node.type = p.info.type }
        let (attachment, info) = DocumentMapper.paragraph(node, depth: p.info.depth)
        let at = NSRange(location: p.range.location, length: 1)
        keepingScroll {
            bypass = true
            storage.replaceCharacters(in: at, with: attachment)
            bypass = false
            setBlock(info, at: at.location)
            edited(at)
        }
    }

    func reveal(blockID: String, atTop: Bool = false) {
        let all = DocumentMapper.paragraphs(storage, trailing: trailing)
        guard var at = all.firstIndex(where: { $0.info.id == blockID }) else { return }
        // Folded away, it is shown by what folds it: the nearest block
        // before it that shows.
        while at > 0, folded.contains(all[at].info.id) { at -= 1 }
        for p in [all[at]] {
            textView.selectedRange = NSRange(location: p.range.location, length: 0)
            if atTop, scrollToTop(p.range.location) {
                if p.info.type == "heading", atHeading != blockID {
                    atHeading = blockID
                    onHeading?(blockID)
                }
                return
            }
            textView.scrollRangeToVisible(p.range)
            return
        }
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

    /// focusStart puts the caret at the start of the page's text.
    func focusStart() {
        _ = textView.becomeFirstResponder()
        textView.selectedRange = NSRange(location: 0, length: 0)
        // Again on the next turn, should what had the keyboard take it
        // back meanwhile.
        DispatchQueue.main.async { [weak self] in
            guard let self, !self.textView.isFirstResponder else { return }
            _ = self.textView.becomeFirstResponder()
            self.textView.selectedRange = NSRange(location: 0, length: 0)
        }
    }

    /// send passes the page's content now, as the page closes.
    func send() {
        sendTask?.cancel()
        sendTask = nil
        onEdit?(DocumentMapper.content(storage, trailing: trailing, meta: meta))
    }

    var hasPendingEdit: Bool { sendTask != nil }

    private func restyleAll() {
        style.apply(to: storage, in: NSRange(location: 0, length: storage.length))
    }

    private func restyleNumbersAfter(_ range: NSRange) {
        // A numbered item's number follows those before it.
        let end = min(NSMaxRange(range), storage.length)
        let rest = NSRange(location: end, length: storage.length - end)
        guard rest.length > 0 else { return }
        var at = rest.location
        let ns = storage.string as NSString
        while at < storage.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            guard let info = blockAt(para.location), info.type == "numbered_list" || info.depth > 0
            else {
                break
            }
            style.apply(to: storage, in: para)
            at = NSMaxRange(para)
        }
    }

    // MARK: Paragraphs

    /// paragraph returns the paragraph at location: its text's range,
    /// without the separator, and its block.
    func paragraph(at location: Int) -> (range: NSRange, info: BlockInfo) {
        let ns = storage.string as NSString
        let loc = min(max(location, 0), ns.length)
        var range = ns.paragraphRange(for: NSRange(location: loc, length: 0))
        if loc == ns.length, ns.length > 0, ns.character(at: ns.length - 1) == 0x0A {
            range = NSRange(location: ns.length, length: 0)
        }
        var content = range
        if content.length > 0, ns.character(at: NSMaxRange(content) - 1) == 0x0A {
            content.length -= 1
        }
        let info = range.length > 0 ? blockAt(range.location) : nil
        return (content, info ?? trailing ?? BlockInfo.paragraph())
    }

    private func blockAt(_ location: Int) -> BlockInfo? {
        guard location < storage.length else { return nil }
        return storage.attribute(.leafletBlock, at: location, effectiveRange: nil) as? BlockInfo
    }

    /// setBlock makes info the block of the paragraph at location.
    func setBlock(_ info: BlockInfo, at location: Int) {
        let (range, _) = paragraph(at: location)
        var whole = range
        if NSMaxRange(range) < storage.length { whole.length += 1 }
        if whole.length > 0 {
            storage.addAttribute(.leafletBlock, value: info, range: whole)
        }
        let before = trailing
        if NSMaxRange(range) == storage.length { trailing = range.length == 0 ? info : nil }
        if whole.length == 0 { trailing = info }
        if trailing !== before { redrawEnd() }
        edited(whole.length > 0 ? whole : NSRange(location: range.location, length: 0))
    }

    /// redrawEnd lays the last paragraph out again, whose fragment draws
    /// the empty block after it.
    private func redrawEnd() {
        guard let tlm = textView.textLayoutManager, storage.length > 0 else { return }
        let ns = storage.string as NSString
        let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
        guard let start = tlm.location(tlm.documentRange.location, offsetBy: last.location),
            let range = NSTextRange(location: start, end: tlm.documentRange.endLocation)
        else { return }
        tlm.invalidateLayout(for: range)
        textView.setNeedsLayout()
    }

    /// normalize keeps each paragraph near range one block: the block of
    /// its first character, and one apart from the paragraph before it.
    private func normalize(around range: NSRange) {
        let ns = storage.string as NSString
        guard ns.length > 0 else { return }
        let start = ns.paragraphRange(
            for: NSRange(location: min(range.location, ns.length - 1), length: 0))
        let endLoc = min(NSMaxRange(range) + 1, ns.length)
        var at =
            start.location > 0
            ? ns.paragraphRange(for: NSRange(location: start.location - 1, length: 0)).location : 0
        var previous: BlockInfo?
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            // Text typed into an empty block may have no block yet: the
            // separator, or the trailing block, holds the paragraph's.
            let last = NSMaxRange(para) - 1
            var info =
                blockAt(para.location)
                ?? (ns.character(at: last) == 0x0A ? blockAt(last) : trailing)
                ?? BlockInfo.paragraph()
            if info === previous { info = derived(info) }
            var content = para
            if ns.character(at: NSMaxRange(para) - 1) == 0x0A { content.length -= 1 }
            // A block without text whose attachment went is an empty
            // paragraph.
            if !info.isText,
                content.length == 0
                    || storage.attribute(.attachment, at: para.location, effectiveRange: nil) == nil
            {
                info = info.with(type: "paragraph", data: [:])
            }
            var differs = false
            storage.enumerateAttribute(.leafletBlock, in: para) { v, _, stop in
                if (v as? BlockInfo) !== info {
                    differs = true
                    stop.pointee = true
                }
            }
            if differs { storage.addAttribute(.leafletBlock, value: info, range: para) }
            previous = info
            at = NSMaxRange(para)
            if at > endLoc && !differs { break }
        }
        if let trailing, trailing === previous, ns.character(at: ns.length - 1) == 0x0A {
            self.trailing = derived(trailing)
        }
    }

    /// derived is the block a new paragraph after info's gets: the same
    /// kind of list, quote or callout, and text after a heading.
    private func derived(_ info: BlockInfo) -> BlockInfo {
        switch info.type {
        case "heading", "code":
            return BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth)
        case "todo_list":
            return BlockInfo(
                id: BlockInfo.newID(), type: "todo_list", data: ["checked": .bool(false)],
                depth: info.depth)
        case "toggle_list":
            return BlockInfo(
                id: BlockInfo.newID(), type: "toggle_list", data: [:], depth: info.depth)
        default:
            var d = info.data
            d["due"] = nil
            return BlockInfo(
                id: BlockInfo.newID(), type: info.isText ? info.type : "paragraph",
                data: info.isText ? d : [:], depth: info.depth)
        }
    }

    private func updateTypingAttributes() {
        let sel = textView.selectedRange
        let (range, info) = paragraph(at: sel.location)
        typing = [.leafletBlock: info]
        let plain = sel.location == plainAt
        if !plain { plainAt = nil }
        if sel.location > range.location, !plain {
            // Typing carries on the formats of the text before the caret,
            // in its block; links and mentions stay what they are.
            let before = storage.attributes(at: sel.location - 1, effectiveRange: nil)
            for k in Self.formatKeys { typing[k] = before[k] }
        }
        var attrs = textView.typingAttributes
        for k in Self.formatKeys + [
            .leafletHref, .leafletMention, .leafletTinted, .backgroundColor,
        ] {
            attrs[k] = nil
        }
        attrs.merge(typing) { $1 }
        attrs[.paragraphStyle] = style.paragraphStyle(info)
        attrs[.font] = style.baseFont(info)
        attrs[.foregroundColor] = UIColor(style.palette.text)
        textView.typingAttributes = attrs
    }

    // MARK: Caret

    private func caretPosition() -> (id: String, offset: Int)? {
        let sel = textView.selectedRange
        let (range, info) = paragraph(at: sel.location)
        return (info.id, sel.location - range.location)
    }

    private func restoreCaret(_ caret: (id: String, offset: Int)?) {
        guard let caret else { return }
        for p in DocumentMapper.paragraphs(storage, trailing: trailing) where p.info.id == caret.id
        {
            textView.selectedRange = NSRange(
                location: p.range.location + min(caret.offset, p.range.length), length: 0)
            return
        }
    }

    // MARK: Rules

    func textView(
        _ textView: UITextView, shouldChangeTextIn range: NSRange, replacementText text: String
    ) -> Bool {
        if bypass { return true }
        let allowed = follow(range, text)
        // A rule changed the text itself, in place of the keyboard's edit.
        if !allowed { tellKeyboard() }
        return allowed
    }

    /// tellKeyboard says the text changed other than by the keyboard's
    /// edits, as the rules and menus change it. Untold, the keyboard goes
    /// on from the text it last knew, taking the line for a word still
    /// being typed, and on Return corrects it to what it guessed.
    private func tellKeyboard() {
        textView.inputDelegate?.textWillChange(textView)
        textView.inputDelegate?.textDidChange(textView)
    }

    /// follow applies the editing rules to an edit the keyboard is making,
    /// and reports whether to let it through as it is.
    private func follow(_ range: NSRange, _ text: String) -> Bool {
        if let menu, text == "\n" || text == "\t" {
            if let item = menu.current { pick(item) } else { closeMenu() }
            return false
        }
        let (para, info) = paragraph(at: range.location)
        switch text {
        case "\n":
            return enter(range, para, info)
        case "":
            // Backspace at a block's start deletes the separator before
            // it: the rules go by the block the caret is in.
            if range.length == 1, (storage.string as NSString).character(at: range.location) == 0x0A
            {
                let (caretPara, caretInfo) = paragraph(at: NSMaxRange(range))
                if caretPara.location == NSMaxRange(range) {
                    return backspaceAtStart(caretPara, caretInfo)
                }
            }
            return true
        case " ", "\u{3000}":
            return !markdownShortcut(range, para, info, typing: text)
        case "-":
            return !dividerShortcut(range, para, info)
        case "\t":
            indent(by: 1)
            return false
        default:
            if !info.isText {
                // Text typed by a block without text goes in a new block
                // after it.
                insertParagraph(after: para, info: BlockInfo.paragraph(), text: text)
                return false
            }
            typed = NSRange(location: range.location, length: (text as NSString).length)
            if text == "/" || text == "@", info.type != "code",
                startsWord(at: range.location, in: para)
            {
                opening = (range.location, Character(text))
            }
            return true
        }
    }

    private func enter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) -> Bool {
        let caret = range.location
        if info.type == "code" {
            let ns = storage.string as NSString
            let atEnd = caret == NSMaxRange(para)
            if atEnd, para.length > 0, ns.character(at: caret - 1) == 0x2028 {
                // Enter on an empty last line leaves the code.
                bypass = true
                storage.replaceCharacters(in: NSRange(location: caret - 1, length: 1), with: "")
                bypass = false
                insertParagraph(
                    after: NSRange(location: para.location, length: para.length - 1),
                    info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth),
                    text: "")
                return false
            }
            insert(String(lineBreak), replacing: range)
            return false
        }
        if !info.isText {
            insertParagraph(
                after: para,
                info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth),
                text: "")
            return false
        }
        if para.length == 0 && range.length == 0 {
            if info.depth > 0 {
                setBlock(info.with(depth: info.depth - 1), at: para.location)
                return false
            }
            if info.type != "paragraph" {
                setBlock(info.with(type: "paragraph", data: [:]), at: para.location)
                return false
            }
        }
        // Split the block: the text after the caret goes in a new one.
        let atStart = caret == para.location && para.length > 0
        insert("\n", replacing: range)
        let next = textView.selectedRange.location
        if atStart {
            // Enter at the start makes an empty block before the text,
            // which keeps its block.
            let before =
                info.type == "heading"
                ? BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth)
                : derived(info)
            storage.addAttribute(
                .leafletBlock, value: before, range: NSRange(location: para.location, length: 1))
            setBlock(info, at: next)
        } else {
            setBlock(derived(info), at: next)
        }
        return false
    }

    private func backspaceAtStart(_ para: NSRange, _ info: BlockInfo) -> Bool {
        if info.isText, info.type != "paragraph" {
            // A list item, quote or heading turns into text first.
            if info.isList, info.depth > 0 {
                setBlock(info.with(depth: info.depth - 1), at: para.location)
            } else {
                setBlock(info.with(type: "paragraph", data: [:]), at: para.location)
            }
            return false
        }
        if info.depth > 0 {
            setBlock(info.with(depth: info.depth - 1), at: para.location)
            return false
        }
        guard para.location > 0 else { return false }
        let (prev, prevInfo) = paragraph(at: para.location - 1)
        if !prevInfo.isText {
            if para.length == 0 {
                // An empty block after an image goes.
                delete(NSRange(location: prev.location + prev.length, length: 1))
            } else {
                // The image is chosen first, to be deleted with another
                // Backspace.
                textView.selectedRange = prev
            }
            return false
        }
        if !info.isText {
            textView.selectedRange = NSRange(location: NSMaxRange(prev), length: 0)
            return false
        }
        return true
    }

    /// markdownShortcut turns "#", "-", "1.", "[]", ">", "```" and the like,
    /// as a space is typed after them at a block's start, into the block
    /// they stand for, as the core's rules have it, the desktop's.
    private func markdownShortcut(
        _ range: NSRange, _ para: NSRange, _ info: BlockInfo, typing space: String
    ) -> Bool {
        guard range.length == 0 else { return false }
        let prefix = (storage.string as NSString).substring(
            with: NSRange(location: para.location, length: range.location - para.location))
        let raw = MobileMarkdownShortcut(info.type, prefix + space)
        guard let made = try? JSONDecoder().decode(MenuBlock.self, from: Data(raw.utf8)),
            let type = made.type
        else { return false }
        delete(NSRange(location: para.location, length: prefix.utf16.count))
        setBlock(info.with(type: type, data: made.data ?? [:]), at: para.location)
        return true
    }

    /// dividerShortcut turns a paragraph of "--" and a third "-" into a
    /// divider, as the core's rules have it.
    private func dividerShortcut(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) -> Bool {
        guard range.length == 0, range.location == NSMaxRange(para),
            MobileDividerShortcut(
                info.type, (storage.string as NSString).substring(with: para) + "-")
        else { return false }
        delete(para)
        let divider = DocNode(id: BlockInfo.newID(), type: "divider", data: nil, children: nil)
        let (attachment, dInfo) = DocumentMapper.paragraph(divider, depth: info.depth)
        bypass = true
        storage.replaceCharacters(in: NSRange(location: para.location, length: 0), with: attachment)
        bypass = false
        setBlock(dInfo, at: para.location)
        insertParagraph(
            after: NSRange(location: para.location, length: 1),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
        return true
    }

    // MARK: Changing the text

    private func insert(_ text: String, replacing range: NSRange) {
        bypass = true
        if let r = textRange(range) { textView.replace(r, withText: text) }
        bypass = false
    }

    private func delete(_ range: NSRange) { insert("", replacing: range) }

    private func textRange(_ r: NSRange) -> UITextRange? {
        guard let s = textView.position(from: textView.beginningOfDocument, offset: r.location),
            let e = textView.position(from: s, offset: r.length)
        else { return nil }
        return textView.textRange(from: s, to: e)
    }

    /// insertParagraph puts a new block after the paragraph para, with
    /// text, and the caret after its text.
    private func insertParagraph(after para: NSRange, info: BlockInfo, text: String) {
        let end = NSMaxRange(para)
        textView.selectedRange = NSRange(location: end, length: 0)
        insert("\n" + text, replacing: NSRange(location: end, length: 0))
        setBlock(info, at: end + 1)
        textView.selectedRange = NSRange(location: end + 1 + (text as NSString).length, length: 0)
    }

    func textViewDidBeginEditing(_ textView: UITextView) {
        onEditing?(true)
        updateDateOffer()
    }

    func textViewDidEndEditing(_ textView: UITextView) {
        onEditing?(false)
        updateDateOffer()
    }

    func textViewDidChange(_ textView: UITextView) {
        defer { updateHeading() }
        if let r = typed, NSMaxRange(r) <= storage.length {
            for k in Self.formatKeys where typing[k] == nil { storage.removeAttribute(k, range: r) }
            storage.removeAttribute(.leafletHref, range: r)
            storage.removeAttribute(.leafletMention, range: r)
            storage.addAttributes(typing, range: r)
        }
        if let r = typed, r.length == 1, textView.markedTextRange == nil {
            _ = inlineShortcut(NSMaxRange(r))
        }
        typed = nil
        defer {
            if let (at, trigger) = opening {
                opening = nil
                openMenu(at: at, trigger: trigger)
            } else {
                updateMenu()
            }
        }
        let sel = textView.selectedRange
        let (para, _) = paragraph(at: sel.location)
        edited(NSRange(location: para.location, length: max(para.length, 1)))
    }

    /// inlineShortcut turns **bold**, *italic*, `code` and ~~strike~~ typed
    /// in full, its closing marker ending at end, into their formats, as the
    /// core's rules have it, the desktop's. It reports whether it did.
    private func inlineShortcut(_ end: Int) -> Bool {
        let (para, info) = paragraph(at: end)
        guard info.isText, info.type != "code", end <= NSMaxRange(para) else { return false }
        let upTo = NSRange(location: para.location, length: end - para.location)
        // The mentions' runs, for a marker in one to be no marker.
        var mentions: [[Int]] = []
        storage.enumerateAttribute(.leafletMention, in: upTo) { v, r, _ in
            if v != nil {
                mentions.append([r.location - para.location, NSMaxRange(r) - para.location])
            }
        }
        let runs = (try? JSONEncoder().encode(mentions)).map { String(decoding: $0, as: UTF8.self) }
        let raw = MobileInlineShortcut(
            (storage.string as NSString).substring(with: upTo), runs ?? "[]")
        guard let m = try? JSONDecoder().decode(InlineMatch.self, from: Data(raw.utf8)) else {
            return false
        }
        let (key, value): (NSAttributedString.Key, Any) =
            switch m.format {
            case "bold": (.leafletBold, true)
            case "strike": (.strikethroughStyle, NSUnderlineStyle.single.rawValue)
            case "code": (.leafletCode, true)
            default: (.leafletItalic, true)
            }
        let at = para.location
        let whole = NSRange(location: at + m.open, length: m.end - m.open)
        let before = storage.attributedSubstring(from: whole)
        let undo = textView.undoManager
        bypass = true
        storage.beginEditing()
        storage.replaceCharacters(
            in: NSRange(location: at + m.close, length: m.end - m.close), with: "")
        storage.addAttribute(
            key, value: value, range: NSRange(location: at + m.inner, length: m.close - m.inner))
        storage.replaceCharacters(
            in: NSRange(location: at + m.open, length: m.inner - m.open), with: "")
        storage.endEditing()
        bypass = false
        let after = NSRange(location: whole.location, length: m.close - m.inner)
        registerUndo(undo, range: after, before: before)
        // What is typed after the closing marker is plain again.
        plainAt = NSMaxRange(after)
        textView.selectedRange = NSRange(location: NSMaxRange(after), length: 0)
        tellKeyboard()
        return true
    }

    /// InlineMatch is a marker pair the core's InlineShortcut found, in
    /// UTF-16 offsets in the block: the opening marker from open to inner,
    /// the text to format to close, and the closing marker to end.
    private struct InlineMatch: Decodable {
        var open: Int
        var inner: Int
        var close: Int
        var end: Int
        var format: String
    }

    func textViewDidChangeSelection(_ textView: UITextView) {
        updateTypingAttributes()
        updateMenu()
        updateDateOffer()
    }

    // MARK: The menu at the caret

    /// startsWord reports whether location is where a word can start: a
    /// block's start, or after a space or Chinese or Japanese text, as the
    /// core's rules have it, the desktop's.
    private func startsWord(at location: Int, in para: NSRange) -> Bool {
        guard location > para.location else { return true }
        let ns = storage.string as NSString
        let prev = ns.rangeOfComposedCharacterSequence(at: location - 1)
        return MobileOpensMenu(ns.substring(with: prev))
    }

    private func openMenu(at anchor: Int, trigger: Character) {
        closeMenu()
        let m = CaretMenu(anchor: anchor, trigger: trigger, palette: style.palette)
        m.onPick = { [weak self] item in self?.pick(item) }
        menu = m
        textView.reloadInputViews()
        menuItems?(trigger) { [weak self, weak m] items in
            guard let self, let m, self.menu === m else { return }
            m.load(items)
            self.updateMenu()
        }
        updateMenu()
    }

    /// updateMenu filters the menu by what is typed after its "/" or "@",
    /// and closes it once the caret leaves that word.
    private func updateMenu() {
        guard let menu else { return }
        let ns = storage.string as NSString
        let caret = textView.selectedRange
        guard caret.length == 0, menu.anchor < ns.length, caret.location > menu.anchor,
            Character(UnicodeScalar(ns.character(at: menu.anchor)) ?? " ") == menu.trigger
        else { return closeMenu() }
        let query = ns.substring(
            with: NSRange(location: menu.anchor + 1, length: caret.location - menu.anchor - 1))
        if query.contains("\n") || query.contains(String(lineBreak)) || query.count > 30
            || query.hasPrefix(" ")
        {
            return closeMenu()
        }
        menu.filter(query)
        if menu.items.isEmpty, query.hasSuffix(" ") { return closeMenu() }
        guard let end = textView.selectedTextRange?.end else { return }
        menu.place(in: textView, caret: textView.caretRect(for: end))
    }

    func closeMenu() {
        guard let menu else { return }
        menu.close()
        self.menu = nil
        textView.reloadInputViews()
    }

    func moveInMenu(_ step: Int) { menu?.move(step) }

    /// pick does what the menu's item says, in place of the "/" or "@" and
    /// what was typed after it.
    private func pick(_ item: MenuItem) {
        guard let menu else { return }
        let caret = textView.selectedRange.location
        closeMenu()
        delete(NSRange(location: menu.anchor, length: max(caret - menu.anchor, 1)))
        textView.selectedRange = NSRange(location: menu.anchor, length: 0)
        defer { tellKeyboard() }
        if menu.trigger == "@" {
            mention(item, at: menu.anchor)
            return
        }
        var error: NSError?
        let raw = MobileMenuBlock(item.key, author?() ?? "", &error)
        let made = try? JSONDecoder().decode(MenuBlock.self, from: Data(raw.utf8))
        if let type = made?.type {
            let (para, info) = paragraph(at: menu.anchor)
            let data = made?.data ?? [:]
            if para.length == 0 || !info.isText {
                setBlock(info.with(type: type, data: data), at: para.location)
            } else {
                insertParagraph(
                    after: para,
                    info: BlockInfo(
                        id: BlockInfo.newID(), type: type, data: data, depth: info.depth),
                    text: "")
            }
        } else if let node = made?.node {
            insertBlock(node)
            // A chart, a formula or a diagram opens to be written at once,
            // as on the desktop.
            if ["chart", "math_equation", "diagram"].contains(node.type) { onEditBlock?(node) }
        } else if item.key == "subpage" {
            Task { [weak self] in
                guard let id = await self?.createSubpage?() else { return }
                self?.insertBlock(
                    DocNode(
                        id: BlockInfo.newID(), type: "subpage", data: ["page_id": .string(id)],
                        children: nil))
            }
        } else {
            onMenuItem?(item.key, paragraph(at: menu.anchor).info.id)
        }
    }

    /// MenuBlock is what a "/" item makes, as the core's MenuBlock says: a
    /// type and data for the block at the caret, or a block to put there.
    private struct MenuBlock: Decodable {
        var type: String?
        var data: [String: JSONValue]?
        var node: DocNode?
    }

    /// mention does what an "@" item says at anchor: asks an agent, opens
    /// Settings to add one, or mentions a page or a person.
    private func mention(_ item: MenuItem, at anchor: Int) {
        let parts = item.key.split(separator: ":", maxSplits: 1).map(String.init)
        let kind = parts.first ?? ""
        let id = parts.count > 1 ? parts[1] : ""
        switch kind {
        case "connect":
            onAddAgent?()
        case "agent":
            onAsk?(focusBlockIDs(), "", agents?().first { $0.providerID == id })
        case "page", "user":
            // A page by its title, linked; a person as "@" and the name, as
            // the desktop puts them.
            let page = kind == "page"
            let info = paragraph(at: anchor).info
            var attributes: [NSAttributedString.Key: Any] = [
                .leafletBlock: info,
                .leafletMention: Mention(kind: kind, id: id, name: item.label),
            ]
            if page { attributes[.leafletHref] = "leaflet://page/\(id)" }
            let mention = NSMutableAttributedString(
                string: (page ? "" : "@") + item.label, attributes: attributes)
            mention.append(NSAttributedString(string: " ", attributes: [.leafletBlock: info]))
            bypass = true
            storage.replaceCharacters(in: NSRange(location: anchor, length: 0), with: mention)
            bypass = false
            textView.selectedRange = NSRange(location: anchor + mention.length, length: 0)
            edited(NSRange(location: anchor, length: mention.length))
        default:
            break
        }
    }

    /// insertBlock puts a block without text at the caret: in place of an
    /// empty block, or after the block there, with an empty block after it
    /// to write on.
    func insertBlock(_ node: DocNode) {
        var (para, info) = paragraph(at: textView.selectedRange.location)
        let (attachment, blockInfo) = DocumentMapper.paragraph(node, depth: info.depth)
        bypass = true
        if para.length == 0, info.isText {
            storage.replaceCharacters(
                in: NSRange(location: para.location, length: 0), with: attachment)
        } else {
            let end = NSMaxRange(para)
            storage.replaceCharacters(
                in: NSRange(location: end, length: 0),
                with: NSAttributedString(string: "\n", attributes: [.leafletBlock: info]))
            storage.replaceCharacters(in: NSRange(location: end + 1, length: 0), with: attachment)
            para = NSRange(location: end + 1, length: 0)
        }
        bypass = false
        setBlock(blockInfo, at: para.location)
        insertParagraph(
            after: NSRange(location: para.location, length: attachment.length),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
    }

    // MARK: Commands

    /// selectedParagraphs are the paragraphs the selection touches.
    private func selectedParagraphs() -> [(range: NSRange, info: BlockInfo)] {
        let sel = textView.selectedRange
        var out: [(NSRange, BlockInfo)] = []
        var at = sel.location
        repeat {
            let p = paragraph(at: at)
            out.append(p)
            at = NSMaxRange(p.range) + 1
        } while at <= NSMaxRange(sel) && at <= storage.length && sel.length > 0
        return out
    }

    func indent(by step: Int) {
        for (range, info) in selectedParagraphs() {
            var depth = info.depth + step
            if step > 0 {
                // A block nests only in the block before it.
                guard range.location > 0 else { continue }
                let (_, prev) = paragraph(at: range.location - 1)
                let room = prev.isList ? prev.depth + 1 : prev.depth
                depth = min(depth, room)
            }
            depth = max(depth, 0)
            if depth != info.depth { setBlock(info.with(depth: depth), at: range.location) }
        }
    }

    func setType(_ type: String, _ data: [String: JSONValue] = [:]) {
        for (range, info) in selectedParagraphs() where info.isText {
            let same =
                info.type == type
                && (type != "heading" || info.level == Int(data["level"]?.int ?? 0))
            setBlock(
                same ? info.with(type: "paragraph", data: [:]) : info.with(type: type, data: data),
                at: range.location)
        }
    }

    func toggleInline(_ key: NSAttributedString.Key) {
        let sel = textView.selectedRange
        let on: Any =
            key == .underlineStyle || key == .strikethroughStyle
            ? NSUnderlineStyle.single.rawValue : true
        if sel.length == 0 {
            typing[key] = typing[key] == nil ? on : nil
            return
        }
        var all = true
        storage.enumerateAttribute(key, in: sel) { v, _, stop in
            if v == nil {
                all = false
                stop.pointee = true
            }
        }
        let undo = textView.undoManager
        let before = storage.attributedSubstring(from: sel)
        if all {
            storage.removeAttribute(key, range: sel)
        } else {
            storage.addAttribute(key, value: on, range: sel)
        }
        registerUndo(undo, range: sel, before: before)
        edited(sel)
    }

    /// setInline sets a format with a value (a link, a color) on the
    /// selection, or takes it off with nil.
    func setInline(_ key: NSAttributedString.Key, _ value: Any?) {
        let sel = textView.selectedRange
        guard sel.length > 0 else { return }
        let before = storage.attributedSubstring(from: sel)
        if let value {
            storage.addAttribute(key, value: value, range: sel)
        } else {
            storage.removeAttribute(key, range: sel)
        }
        registerUndo(textView.undoManager, range: sel, before: before)
        edited(sel)
    }

    /// registerUndo puts before back in place of range, which may be of
    /// another length, and the text there now back on redo.
    private func registerUndo(_ undo: UndoManager?, range: NSRange, before: NSAttributedString) {
        undo?.registerUndo(withTarget: self) { c in
            let now = c.storage.attributedSubstring(from: range)
            c.storage.replaceCharacters(in: range, with: before)
            let back = NSRange(location: range.location, length: before.length)
            c.registerUndo(undo, range: back, before: now)
            if back.length != range.length {
                c.textView.selectedRange = NSRange(location: NSMaxRange(back), length: 0)
            }
            c.edited(back)
        }
    }

    func moveBlock(by step: Int) {
        let (para, _) = paragraph(at: textView.selectedRange.location)
        let offset = textView.selectedRange.location - para.location
        let ns = storage.string as NSString
        let whole = ns.paragraphRange(for: NSRange(location: para.location, length: 0))
        if step < 0 {
            guard para.location > 0 else { return }
            let prev = ns.paragraphRange(for: NSRange(location: para.location - 1, length: 0))
            swapParagraphs(prev, whole)
            let moved = paragraph(at: prev.location)
            textView.selectedRange = NSRange(location: moved.range.location + offset, length: 0)
        } else {
            guard NSMaxRange(whole) < ns.length else { return }
            let next = ns.paragraphRange(for: NSRange(location: NSMaxRange(whole), length: 0))
            swapParagraphs(whole, next)
            let at = whole.location + next.length + (NSMaxRange(next) == ns.length ? 1 : 0)
            let moved = paragraph(at: min(at, storage.length))
            textView.selectedRange = NSRange(location: moved.range.location + offset, length: 0)
        }
    }

    /// swapParagraphs swaps two paragraphs next to each other, a before b,
    /// each with its separator.
    private func swapParagraphs(_ a: NSRange, _ b: NSRange) {
        let sa = NSMutableAttributedString(attributedString: storage.attributedSubstring(from: a))
        let sb = NSMutableAttributedString(attributedString: storage.attributedSubstring(from: b))
        // The last paragraph has no separator: it borrows the other's.
        if !sb.string.hasSuffix("\n") {
            let infoB =
                sb.length > 0 ? sb.attribute(.leafletBlock, at: 0, effectiveRange: nil) : trailing
            sb.append(
                NSAttributedString(
                    string: "\n", attributes: infoB.map { [.leafletBlock: $0] } ?? [:]))
            sa.deleteCharacters(in: NSRange(location: sa.length - 1, length: 1))
        }
        let joined = NSMutableAttributedString(attributedString: sb)
        joined.append(sa)
        let range = NSRange(location: a.location, length: a.length + b.length)
        let before = storage.attributedSubstring(from: range)
        storage.replaceCharacters(in: range, with: joined)
        registerUndo(
            textView.undoManager, range: NSRange(location: a.location, length: joined.length),
            before: before)
        edited(NSRange(location: a.location, length: joined.length))
    }

    func editLink() {
        let sel = textView.selectedRange
        guard sel.length > 0 else { return }
        let current =
            storage.attribute(.leafletHref, at: sel.location, effectiveRange: nil) as? String
        let alert = UIAlertController(title: L.editorLink, message: nil, preferredStyle: .alert)
        alert.addTextField { f in
            f.text = current
            f.placeholder = L.editorPasteLink
            f.keyboardType = .URL
            f.autocapitalizationType = .none
        }
        alert.addAction(UIAlertAction(title: L.commonCancel, style: .cancel))
        if current != nil {
            alert.addAction(
                UIAlertAction(title: LI.removeLink, style: .destructive) { [weak self] _ in
                    self?.setInline(.leafletHref, nil)
                })
        }
        alert.addAction(
            UIAlertAction(title: L.commonSave, style: .default) { [weak self, weak alert] _ in
                let link =
                    alert?.textFields?.first?.text?.trimmingCharacters(in: .whitespaces) ?? ""
                self?.setInline(.leafletHref, link.isEmpty ? nil : link)
            })
        textView.window?.rootViewController?.topmost.present(alert, animated: true)
    }

    // MARK: Taps on markers

    private func markerHit(_ point: CGPoint) -> (BlockInfo, Int)? {
        guard let tlm = textView.textLayoutManager else { return nil }
        let p = CGPoint(
            x: point.x - textView.textContainerInset.left,
            y: point.y - textView.textContainerInset.top)
        guard let fragment = tlm.textLayoutFragment(for: p) as? BlockLayoutFragment,
            let element = fragment.textElement, let range = element.elementRange
        else { return nil }
        let location = tlm.offset(from: tlm.documentRange.location, to: range.location)
        let (_, info) = paragraph(at: location)
        guard ["todo_list", "toggle_list", "heading", "callout"].contains(info.type) else {
            return nil
        }
        let marker = fragment.markerRect(info).offsetBy(
            dx: fragment.layoutFragmentFrame.minX, dy: fragment.layoutFragmentFrame.minY
        )
        .insetBy(dx: -8, dy: -6)
        return marker.contains(p) ? (info, location) : nil
    }

    /// codeBarHit finds the code block whose bar is at point, and whether
    /// the point is on its copy button rather than its language.
    private func codeBarHit(_ point: CGPoint) -> (BlockInfo, Int, copy: Bool)? {
        guard let tlm = textView.textLayoutManager else { return nil }
        let p = CGPoint(
            x: point.x - textView.textContainerInset.left,
            y: point.y - textView.textContainerInset.top)
        // The bar is above the code's first line, in the room the block's
        // spacing leaves.
        for dy: CGFloat in [0, BlockLayoutFragment.codeBar] {
            guard
                let fragment = tlm.textLayoutFragment(for: CGPoint(x: p.x, y: p.y + dy))
                    as? BlockLayoutFragment,
                let range = fragment.textElement?.elementRange
            else { continue }
            let location = tlm.offset(from: tlm.documentRange.location, to: range.location)
            let (_, info) = paragraph(at: location)
            guard info.type == "code" else { continue }
            let card = fragment.codeCard.offsetBy(
                dx: fragment.layoutFragmentFrame.minX, dy: fragment.layoutFragmentFrame.minY)
            let parts = BlockLayoutFragment.codeBarParts(card)
            if parts.copy.insetBy(dx: -6, dy: -4).contains(p) { return (info, location, true) }
            if parts.language.insetBy(dx: -6, dy: -4).contains(p) { return (info, location, false) }
        }
        return nil
    }

    func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        let at = g.location(in: textView)
        if g is UILongPressGestureRecognizer { return inSelection(at) }
        return markerHit(at) != nil || codeBarHit(at) != nil || dueChipHit(at) != nil
            || collapses(at: at)
    }

    /// inSelection reports whether point is on the text selected.
    private func inSelection(_ point: CGPoint) -> Bool {
        guard collapses(at: point), let range = textView.selectedTextRange else { return false }
        return textView.selectionRects(for: range).contains { $0.rect.contains(point) }
    }

    /// pressSelection shows the selection's menu (Ask agent, Comments, the
    /// formats), which a tap in it no longer does.
    @objc private func pressSelection(_ g: UILongPressGestureRecognizer) {
        // As the finger lifts: shown while it is down, the menu goes with
        // the touch.
        guard g.state == .ended else { return }
        selectionMenu.presentEditMenu(
            with: UIEditMenuConfiguration(identifier: nil, sourcePoint: g.location(in: textView)))
    }

    func editMenuInteraction(
        _ interaction: UIEditMenuInteraction, menuFor configuration: UIEditMenuConfiguration,
        suggestedActions: [UIMenuElement]
    ) -> UIMenu? {
        // The text's own menu, with the system's Cut, Copy and the rest,
        // which the text answers as the first responder.
        return textView(
            textView, editMenuForTextIn: textView.selectedRange, suggestedActions: suggestedActions)
    }

    func editMenuInteraction(
        _ interaction: UIEditMenuInteraction, targetRectFor configuration: UIEditMenuConfiguration
    ) -> CGRect {
        guard let range = textView.selectedTextRange else { return .null }
        return textView.firstRect(for: range)
    }

    /// collapses reports whether a tap at point puts the caret there, out
    /// of a selection, as a click does on the desktop: the text's own tap
    /// in a selection shows the edit menu instead, so none ends a
    /// selection of everything. A long press still shows the menu. Not on
    /// the title or the ways to start, which take their own taps.
    private func collapses(at point: CGPoint) -> Bool {
        guard textView.selectedRange.length > 0, textView.isFirstResponder else { return false }
        for v in [textView.header, textView.footer] {
            if let v, !v.isHidden, v.frame.contains(point) { return false }
        }
        return true
    }

    /// While text is selected, the text's own taps wait for this one,
    /// which takes the caret where the tap is.
    func gestureRecognizer(
        _ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer
    ) -> Bool {
        return textView.selectedRange.length > 0
            && (g is UILongPressGestureRecognizer
                ? other is UILongPressGestureRecognizer : other is UITapGestureRecognizer)
            && other.view?.isDescendant(of: textView) == true
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
                    self?.onEditBlock?(node)
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

    /// blockPreview lifts the block the menu is for, in its card's shape.
    private func blockPreview(_ configuration: UIContextMenuConfiguration) -> UITargetedPreview? {
        guard let id = configuration.identifier as? String,
            let view = textView.blocks.shownView(id)
        else { return nil }
        let p = UIPreviewParameters()
        p.visiblePath = UIBezierPath(roundedRect: view.bounds, cornerRadius: 10)
        return UITargetedPreview(view: view, parameters: p)
    }

    /// tapCodeBar copies a code block's code, or offers its languages.
    private func tapCodeBar(_ info: BlockInfo, at location: Int, copy: Bool, from point: CGPoint) {
        let (para, _) = paragraph(at: location)
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
                let (_, now) = self.paragraph(at: location)
                guard now.id == info.id else { return }
                self.keepingScroll {
                    self.setBlock(now.setting("language", .string(language.id)), at: location)
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

    func gestureRecognizer(
        _ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer
    ) -> Bool {
        false
    }

    @objc private func tapMarker(_ g: UITapGestureRecognizer) {
        let at = g.location(in: textView)
        if let (info, location, copy) = codeBarHit(at) {
            return tapCodeBar(info, at: location, copy: copy, from: at)
        }
        if let (info, rect) = dueChipHit(at) {
            return openDue(info, from: rect)
        }
        if textView.isEditable, let (info, location) = markerHit(at) {
            return keepingScroll { tapMarker(info, at: location) }
        }
        if collapses(at: at), let position = textView.closestPosition(to: at) {
            let location = textView.offset(from: textView.beginningOfDocument, to: position)
            textView.selectedRange = NSRange(location: location, length: 0)
        }
    }

    /// keepingScroll changes a block away from the caret without the text
    /// view scrolling back to the caret, as it does after a change.
    private func keepingScroll(_ change: () -> Void) {
        let offset = textView.contentOffset
        change()
        textView.layoutIfNeeded()
        textView.setContentOffset(offset, animated: false)
        DispatchQueue.main.async { [weak self] in
            self?.textView.setContentOffset(offset, animated: false)
        }
    }

    private func tapMarker(_ info: BlockInfo, at location: Int) {
        if info.type == "todo_list" {
            setBlock(info.setting("checked", .bool(!info.checked)), at: location)
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
        } else if info.type == "callout" {
            // Its icon gives it the next tone, as the desktop's does.
            let tones = ["info", "tip", "warning", "danger"]
            let tone = info.data["tone"]?.string?.lowercased() ?? ""
            let next = tones[((tones.firstIndex(of: tone) ?? 0) + 1) % tones.count]
            setBlock(info.setting("tone", .string(next)), at: location)
        } else {
            setBlock(
                info.setting("collapsed", .bool(!(info.data["collapsed"]?.bool ?? false))),
                at: location)
            // A caret folded away goes to the end of the toggle's line.
            let caret = textView.selectedRange.location
            if caret < storage.length,
                let inside = storage.attribute(.leafletBlock, at: caret, effectiveRange: nil)
                    as? BlockInfo,
                folded.contains(inside.id)
            {
                let (line, _) = paragraph(at: location)
                textView.selectedRange = NSRange(location: NSMaxRange(line), length: 0)
            }
        }
    }

    // MARK: Due dates

    /// updateDateOffer has the to-do at the caret offer a due date, as the
    /// desktop's focused to-do does, drawing the to-dos that change again.
    private func updateDateOffer() {
        var offer = dueOpen
        if offer == nil, textView.isEditable, textView.isFirstResponder,
            textView.selectedRange.length == 0
        {
            let (_, info) = paragraph(at: textView.selectedRange.location)
            if info.type == "todo_list" { offer = info.id }
        }
        guard offer != dateOffer else { return }
        let old = dateOffer
        dateOffer = offer
        guard let tlm = textView.textLayoutManager else { return }
        for p in DocumentMapper.paragraphs(storage, trailing: trailing)
        where p.info.id == old || p.info.id == offer {
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

    /// dueChipHit finds the to-do whose due date's chip is at point, and
    /// the chip, in the text view.
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

    /// dueChipRect is where a to-do's chip is, in the text view, if shown.
    private func dueChipRect(_ info: BlockInfo) -> CGRect? {
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

    /// openDue opens a to-do's due date from rect, which the to-do takes
    /// as it closes.
    private func openDue(_ info: BlockInfo, from rect: CGRect) {
        let id = info.id
        let popover = DuePopover(Due(info)) { [weak self] due in
            guard let self else { return }
            dueOpen = nil
            if let p = DocumentMapper.paragraphs(storage, trailing: trailing)
                .first(where: { $0.info.id == id }), p.info.type == "todo_list"
            {
                keepingScroll {
                    self.setBlock(p.info.setting("due", due?.value), at: p.range.location)
                }
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

    // MARK: Layout

    func textLayoutManager(
        _ textLayoutManager: NSTextLayoutManager,
        textLayoutFragmentFor location: any NSTextLocation,
        in textElement: NSTextElement
    ) -> NSTextLayoutFragment {
        let f = BlockLayoutFragment(textElement: textElement, range: textElement.elementRange)
        f.palette = style.palette
        f.dateOffer = dateOffer
        // The last paragraph's fragment draws an empty block after it.
        if let trailing, let range = textElement.elementRange,
            textLayoutManager.offset(
                from: range.endLocation, to: textLayoutManager.documentRange.endLocation)
                == 0
        {
            f.trailing = BlockLayoutFragment.Trailing(
                info: trailing, font: style.baseFont(trailing),
                ordinal: trailing.type == "numbered_list"
                    ? EditorStyle.ordinal(storage, at: storage.length, trailing) : 1)
        }
        return f
    }

    // MARK: Menus

    func textView(
        _ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]
    ) -> UIMenu? {
        guard textView.isEditable, range.length > 0 else {
            // At the caret in a to-do, its due date, which its chip may
            // have no room to offer.
            let (_, info) = paragraph(at: range.location)
            guard textView.isEditable, info.type == "todo_list" else {
                return UIMenu(children: suggestedActions)
            }
            let date = UIAction(
                title: Due(info)?.label() ?? L.editorAddDate, image: UIImage(systemName: "calendar")
            ) { [weak self] _ in
                guard let self else { return }
                let caret = textView.selectedTextRange.map { textView.caretRect(for: $0.end) }
                self.openDue(info, from: self.dueChipRect(info) ?? caret ?? .zero)
            }
            return UIMenu(children: [date] + suggestedActions)
        }
        return UIMenu(children: [agentMenu(), formatMenu(), colorMenu()] + suggestedActions)
    }

    private func formatMenu() -> UIMenu {
        UIMenu(
            title: L.editorFormat, image: UIImage(systemName: "textformat"),
            options: .displayInline,
            children: [
                UIAction(title: L.editorBold, image: UIImage(systemName: "bold")) { [weak self] _ in
                    self?.toggleInline(.leafletBold)
                },
                UIAction(title: L.editorItalic, image: UIImage(systemName: "italic")) {
                    [weak self] _ in self?.toggleInline(.leafletItalic)
                },
                UIAction(title: LI.underline, image: UIImage(systemName: "underline")) {
                    [weak self] _ in self?.toggleInline(.underlineStyle)
                },
                UIAction(title: LI.strikethrough, image: UIImage(systemName: "strikethrough")) {
                    [weak self] _ in self?.toggleInline(.strikethroughStyle)
                },
                UIAction(
                    title: L.editorInlineCode,
                    image: UIImage(systemName: "chevron.left.forwardslash.chevron.right")
                ) { [weak self] _ in self?.toggleInline(.leafletCode) },
                UIAction(title: L.editorLink, image: UIImage(systemName: "link")) { [weak self] _ in
                    self?.editLink()
                },
            ])
    }

    /// swatches are the desktop's text colors, as hex, by name.
    static let swatches: [(String, String)] = [
        (L.editorColorRed, "#ef4444"), (L.editorColorOrange, "#f97316"),
        (L.editorColorYellow, "#eab308"),
        (L.editorColorGreen, "#22c55e"), (L.editorColorTeal, "#14b8a6"),
        (L.editorColorBlue, "#3b82f6"),
        (L.editorColorIndigo, "#6366f1"), (L.editorColorPurple, "#a855f7"),
        (L.editorColorPink, "#ec4899"),
        (L.editorColorBrown, "#92400e"), (L.editorColorGray, "#71717a"),
    ]

    private func colorMenu() -> UIMenu {
        func dot(_ hex: String) -> UIImage? {
            UIImage(systemName: "circle.fill")?.withTintColor(
                UIColor(hex: hex) ?? .label, renderingMode: .alwaysOriginal)
        }
        let text = Self.swatches.map { name, hex in
            UIAction(title: name, image: dot(hex)) { [weak self] _ in
                self?.setInline(.leafletColor, hex + "ff")
            }
        }
        let highlight = Self.swatches.map { name, hex in
            UIAction(title: name, image: dot(hex)) { [weak self] _ in
                self?.setInline(.leafletBackground, hex + "40")
            }
        }
        return UIMenu(
            title: L.editorColor, image: UIImage(systemName: "paintpalette"),
            children: [
                UIMenu(
                    title: L.editorColorText, options: .displayInline,
                    children: text + [
                        UIAction(title: L.editorNoTextColor) { [weak self] _ in
                            self?.setInline(.leafletColor, nil)
                        }
                    ]),
                UIMenu(
                    title: L.editorColorHighlight,
                    children: highlight + [
                        UIAction(title: L.editorNoHighlight) { [weak self] _ in
                            self?.setInline(.leafletBackground, nil)
                        }
                    ]),
            ])
    }

    /// blockMenu turns the block at the caret into another kind.
    func blockMenu() -> UIMenu {
        func item(
            _ title: String, _ symbol: String, _ type: String, _ data: [String: JSONValue] = [:]
        ) -> UIAction {
            UIAction(title: title, image: UIImage(systemName: symbol)) { [weak self] _ in
                self?.setType(type, data)
            }
        }
        return UIMenu(
            title: LI.turnInto,
            children: [
                item(L.hostSlashText, "text.alignleft", "paragraph"),
                item(L.hostSlashHeading1, "textformat.size.larger", "heading", ["level": .int(1)]),
                item(L.hostSlashHeading2, "textformat.size", "heading", ["level": .int(2)]),
                item(L.hostSlashHeading3, "textformat.size.smaller", "heading", ["level": .int(3)]),
                item(L.hostSlashBulleted, "list.bullet", "bulleted_list"),
                item(L.hostSlashNumbered, "list.number", "numbered_list"),
                item(L.hostSlashTodo, "checklist", "todo_list", ["checked": .bool(false)]),
                item(L.hostSlashToggle, "chevron.right.square", "toggle_list"),
                item(L.hostSlashQuote, "text.quote", "quote"),
                item(L.hostSlashCallout, "info.circle", "callout", ["tone": .string("info")]),
                item(L.hostSlashCode, "chevron.left.forwardslash.chevron.right", "code"),
                UIAction(title: L.hostSlashDivider, image: UIImage(systemName: "minus")) {
                    [weak self] _ in self?.insertDivider()
                },
            ])
    }

    private func insertDivider() {
        insertBlock(DocNode(id: BlockInfo.newID(), type: "divider", data: nil, children: nil))
    }

    // MARK: Keyboard bar

    /// installKeyboardBar puts the editor's commands over the keyboard on
    /// iPhone, and in the shortcuts bar on iPad.
    private func installKeyboardBar() {
        let insert = Self.menuItem(
            LI.turnInto, "plus.circle",
            UIMenu(children: [
                UIDeferredMenuElement.uncached { [weak self] done in
                    done(self.map { [$0.blockMenu()] } ?? [])
                }
            ]))
        let todo = UIBarButtonItem(
            image: UIImage(systemName: "checklist"),
            primaryAction: UIAction(title: L.hostSlashTodo) { [weak self] _ in
                self?.setType("todo_list", ["checked": .bool(false)])
            })
        let outdent = UIBarButtonItem(
            image: UIImage(systemName: "decrease.indent"),
            primaryAction: UIAction(title: LI.outdent) { [weak self] _ in
                self?.indent(by: -1)
            })
        let indent = UIBarButtonItem(
            image: UIImage(systemName: "increase.indent"),
            primaryAction: UIAction(title: LI.indent) { [weak self] _ in
                self?.indent(by: 1)
            })
        let format = Self.menuItem(
            L.editorFormat, "textformat",
            UIMenu(children: [
                UIDeferredMenuElement.uncached { [weak self] done in
                    guard let self else { return done([]) }
                    done([self.formatMenu(), self.colorMenu()])
                }
            ]))
        if UIDevice.current.userInterfaceIdiom == .phone {
            let bar = UIToolbar()
            let undo = UIBarButtonItem(
                image: UIImage(systemName: "arrow.uturn.backward"),
                primaryAction: UIAction(title: L.commonUndo) { [weak self] _ in
                    self?.textView.undoManager?.undo()
                })
            let done = UIBarButtonItem(
                image: UIImage(systemName: "keyboard.chevron.compact.down"),
                primaryAction: UIAction(title: LI.hideKeyboard) { [weak self] _ in
                    self?.textView.resignFirstResponder()
                })
            bar.items = [insert, format, todo, outdent, indent, .flexibleSpace(), undo, done]
            bar.sizeToFit()
            textView.inputAccessoryView = bar
        } else {
            let item = textView.inputAssistantItem
            item.leadingBarButtonGroups += [
                UIBarButtonItemGroup(
                    barButtonItems: [insert, format, todo, outdent, indent], representativeItem: nil
                )
            ]
        }
    }

    /// menuItem is a bar item that opens menu. On iPad it is a button of
    /// its own, as the shortcuts bar hides an item while its menu is open,
    /// leaving a gap in the bar.
    private static func menuItem(_ title: String, _ symbol: String, _ menu: UIMenu)
        -> UIBarButtonItem
    {
        if UIDevice.current.userInterfaceIdiom == .phone {
            return UIBarButtonItem(title: title, image: UIImage(systemName: symbol), menu: menu)
        }
        let button = StayingMenuButton(type: .system)
        button.setImage(UIImage(systemName: symbol), for: .normal)
        button.accessibilityLabel = title
        button.menu = menu
        button.showsMenuAsPrimaryAction = true
        // The padding the bar gives its own items.
        button.widthAnchor.constraint(equalToConstant: button.intrinsicContentSize.width + 12)
            .isActive = true
        return UIBarButtonItem(customView: button)
    }
}

/// StayingMenuButton stays in view while its menu is open: the menu grows
/// from a clear stand-in, where it would hide the button it grows from.
private final class StayingMenuButton: UIButton {
    private let anchor = UIView()

    override init(frame: CGRect) {
        super.init(frame: frame)
        anchor.isUserInteractionEnabled = false
        addSubview(anchor)
    }

    required init?(coder: NSCoder) { fatalError() }

    override func layoutSubviews() {
        super.layoutSubviews()
        anchor.frame = bounds
    }

    private func preview() -> UITargetedPreview {
        let p = UIPreviewParameters()
        p.backgroundColor = .clear
        return UITargetedPreview(view: anchor, parameters: p)
    }

    override func contextMenuInteraction(
        _ interaction: UIContextMenuInteraction,
        previewForHighlightingMenuWithConfiguration configuration: UIContextMenuConfiguration
    ) -> UITargetedPreview? { preview() }

    override func contextMenuInteraction(
        _ interaction: UIContextMenuInteraction,
        previewForDismissingMenuWithConfiguration configuration: UIContextMenuConfiguration
    ) -> UITargetedPreview? { preview() }
}

extension UIViewController {
    /// topmost is the view controller shown on top of this one.
    var topmost: UIViewController { presentedViewController?.topmost ?? self }
}
