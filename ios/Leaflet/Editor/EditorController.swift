import LeafletCore
import UIKit

/// EditorController edits a page in an EditorTextView. It is the text
/// view's delegate, and brings together the parts that edit the page: the
/// text as blocks (BlockText), the content passed to and from the core
/// (DocumentSession), the editing rules of the desktop's editor
/// (EditingRules), the commands (BlockCommands), the formats
/// (FormatController), the menu at the caret (CaretMenuController), the
/// layout and folds (EditorLayout), the taps (EditorTaps), the due dates
/// (DueDates), the heading the page is at (HeadingTracker) and the menus
/// (EditorMenus). Every change goes in through one path (TextEdits), and
/// each is followed by edited.
@MainActor
final class EditorController: NSObject, UITextViewDelegate {
    let textView: EditorTextView
    /// text is the page's text as its blocks.
    let text: BlockText
    var style: EditorStyle {
        didSet {
            BlockLook.palette = style.palette
            BlockLook.textSize = style.textSize
            // A theme changed draws the page again in it, its text and its
            // blocks: those drawn before keep the colors they were made
            // in.
            let restyled = oldValue.palette.key != style.palette.key
            let resized = oldValue.textSize != style.textSize
            if restyled || resized {
                restyleAll()
                textView.relayout()
            }
            // A table's cells are in the page's text size.
            if resized { textView.blocks.reset { $0.type == "table" } }
            if restyled { refreshBlocks() }
        }
    }
    /// blocksPalette is the palette the blocks' views were made in.
    private var blocksPalette: String

    private(set) lazy var edits = TextEdits(self)
    private(set) lazy var session = DocumentSession(self)
    private(set) lazy var rules = EditingRules(self)
    private(set) lazy var commands = BlockCommands(self)
    private(set) lazy var format = FormatController(self)
    private(set) lazy var clipboard = EditorClipboard(self)
    private(set) lazy var caretMenu = CaretMenuController(self)
    private(set) lazy var layout = EditorLayout(self)
    private(set) lazy var taps = EditorTaps(self)
    private(set) lazy var due = DueDates(self)
    private(set) lazy var headings = HeadingTracker(self)
    private(set) lazy var menus = EditorMenus(self)

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
    /// onEditing says the text took the keyboard, or gave it up.
    var onEditing: ((Bool) -> Void)?
    /// onBlank says the page became blank, one empty paragraph, or no
    /// longer is: a blank page offers ways to start it. It is told as it
    /// is set, too.
    var onBlank: ((Bool) -> Void)? {
        didSet {
            wasBlank = nil
            reportBlank()
        }
    }
    private var wasBlank: Bool?
    /// onUndoRun undoes the latest agent run, as Undo after an agent's
    /// change does.
    var onUndoRun: (@MainActor () -> Void)?
    /// onEditBlock opens a block's editor, from its long press's menu.
    var onEditBlock: (@MainActor (DocNode) -> Void)? {
        didSet { textView.blocks.edit = onEditBlock }
    }
    /// onRenameBlock asks for a new name for a block, named title now: a
    /// recording's, from its menu.
    var onRenameBlock: (@MainActor (DocNode, String) -> Void)?
    /// pageLinks open the pages subpages' cards show.
    var pageLinks: PageLinks? {
        didSet { textView.blocks.links = pageLinks }
    }
    /// createSubpage makes a page in this one, for the / menu's Subpage,
    /// and returns its ID.
    var createSubpage: (() async -> String?)?
    /// plainCopy writes blocks as text alone, for Copy as Plain Text, as
    /// the core's PlainCopy does (CoreClient.plainCopy); copyText as Copy
    /// puts them on the clipboard, their Markdown (CoreClient.copyText).
    var plainCopy: (([DocNode]) async -> String?)?
    var copyText: (([DocNode]) async -> String?)?
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

    init(style: EditorStyle) {
        self.style = style
        blocksPalette = style.palette.key
        BlockLook.palette = style.palette
        BlockLook.textSize = style.textSize
        let view = EditorTextView()
        textView = view
        text = BlockText(view.textStorage)
        super.init()
        NotificationCenter.default.addObserver(
            self, selector: #selector(willEnterForeground),
            name: UIApplication.willEnterForegroundNotification, object: nil)
        // Past midnight, or in another time zone, a due date's chip says
        // another day.
        NotificationCenter.default.addObserver(
            self, selector: #selector(refreshDue),
            name: UIApplication.significantTimeChangeNotification, object: nil)
        // The text's fonts follow the text size chosen in Settings, as they
        // are made: they are made again as it changes.
        textView.registerForTraitChanges([UITraitPreferredContentSizeCategory.self]) {
            [weak self] (_: EditorTextView, previous: UITraitCollection) in
            guard let self,
                previous.preferredContentSizeCategory
                    != self.textView.traitCollection.preferredContentSizeCategory
            else { return }
            self.restyleAll()
            self.format.updateTypingAttributes()
            self.textView.blocks.reset { $0.type == "table" }
            self.textView.relayout()
        }
        textView.controller = self
        textView.delegate = self
        layout.attach()
        taps.attach()
        menus.installKeyboardBar()
    }

    private var storage: NSTextStorage { text.storage }

    /// refreshBlocks makes the blocks' views again in the page's palette,
    /// once the app shows. iOS changes the appearance for a moment as it
    /// takes the app's pictures for the app switcher: made again for it,
    /// every embed would load again and a video would stop, for an
    /// appearance the app comes back from.
    private func refreshBlocks() {
        guard UIApplication.shared.applicationState != .background,
            blocksPalette != style.palette.key
        else { return }
        blocksPalette = style.palette.key
        textView.blocks.reset()
        // A block the top of the view cuts is not in the first pass's
        // text: the next one puts it back.
        DispatchQueue.main.async { [weak self] in self?.textView.replaceBlocks() }
    }

    @objc private func willEnterForeground() {
        refreshBlocks()
        due.refresh()
    }

    @objc private func refreshDue() { due.refresh() }

    // MARK: Content in and out

    /// load shows content, as the page opens.
    func load(_ content: DocContent, tinted: Set<String>) {
        session.load(content, tinted: tinted)
    }

    /// update brings content from outside the editor (sync, an agent, a
    /// merge) into the text, as DocumentSession.update does.
    func update(_ content: DocContent, tinted: Set<String>, byAgent: Bool = false) {
        session.update(content, tinted: tinted, byAgent: byAgent)
    }

    /// tint shows the blocks an agent run last changed lit.
    func tint(_ ids: Set<String>) { session.tint(ids) }

    /// send passes the page's content now, as the page closes.
    func send() { session.send() }

    var hasPendingEdit: Bool { session.hasPendingEdit }

    /// close lets the page's views go as it closes: the blocks' views
    /// joined the view controller the page is in, which outlives it.
    func close() {
        caretMenu.close()
        due.stop()
        // The page's videos stop with it: opened again, they show their
        // pictures.
        VideoPlayback.shared.playing.subtract(
            text.blocks().filter { $0.info.type == "video" }.map { $0.info.id })
        textView.blocks.reset()
    }

    /// emptyPageBlock is the block of an empty page, when it draws a
    /// marker or a card, with its font and the palette.
    var emptyPageBlock: (BlockInfo, UIFont, Palette)? {
        guard storage.length == 0, let trailing = text.trailing, trailing.type != "paragraph",
            trailing.type != "heading"
        else { return nil }
        return (trailing, style.baseFont(trailing), style.palette)
    }

    /// isBlank reports whether the page is one empty paragraph.
    var isBlank: Bool {
        storage.length == 0 && (text.trailing?.type ?? "paragraph") == "paragraph"
    }

    func reportBlank() {
        let blank = isBlank
        guard blank != wasBlank else { return }
        wasBlank = blank
        // On the next turn: the change may come from an update of the
        // page's view, which what the page shows must not change during.
        DispatchQueue.main.async { [weak self] in self?.onBlank?(blank) }
    }

    // MARK: After each change

    /// edited follows each change of the text: it keeps each paragraph
    /// one block, shows the paragraphs changed, and sends the page soon.
    func edited(_ range: NSRange) {
        text.normalize(around: range)
        if text.keepTrailing() { layout.redrawEnd() }
        let fitted = text.fitDepths()
        for r in fitted.changed { style.apply(to: storage, in: r) }
        if fitted.trailing { layout.redrawEnd() }
        // The last block, as deep as it now is.
        if text.keepTrailing() { layout.redrawEnd() }
        style.apply(to: storage, in: range)
        restyleNumbersAfter(range)
        format.updateTypingAttributes()
        layout.updateFolds()
        reportBlank()
        session.edited()
    }

    /// paragraphs is range widened to the paragraphs it touches, and the
    /// caret's.
    func paragraphs(around range: NSRange) -> NSRange {
        let (caret, _) = text.paragraph(at: textView.selectedRange.location)
        let r = NSUnionRange(range, NSRange(location: caret.location, length: max(caret.length, 1)))
        return NSIntersectionRange(r, NSRange(location: 0, length: storage.length))
    }

    func restyleAll() {
        style.apply(to: storage, in: NSRange(location: 0, length: storage.length))
    }

    func restyleNumbersAfter(_ range: NSRange) {
        // A numbered item's number follows those before it.
        let end = min(NSMaxRange(range), storage.length)
        let rest = NSRange(location: end, length: storage.length - end)
        guard rest.length > 0 else { return }
        var at = rest.location
        let ns = storage.string as NSString
        while at < storage.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            guard let info = text.blockAt(para.location),
                info.type == "numbered_list" || info.depth > 0
            else {
                break
            }
            at = NSMaxRange(para)
        }
        // At once, for each item's number to go on from the one before.
        if at > rest.location {
            style.apply(
                to: storage, in: NSRange(location: rest.location, length: at - rest.location))
        }
    }

    // MARK: The text view's delegate

    func textView(
        _ textView: UITextView, shouldChangeTextIn range: NSRange, replacementText text: String
    ) -> Bool {
        rules.shouldChange(range, text)
    }

    func textViewDidChange(_ textView: UITextView) {
        guard !edits.putting else { return }
        edits.inEdit += 1
        defer { edits.inEdit -= 1 }
        defer { headings.update() }
        let typed = format.typed
        format.typed = nil
        if let typed { format.takeTyping(typed.range, typed.attributes) }
        var changed = typed?.range ?? NSRange(location: textView.selectedRange.location, length: 0)
        // Text pasted or dropped, other than typed, may bring block cards.
        let cards =
            typed.map {
                NSMaxRange($0.range) <= storage.length
                    && (storage.string as NSString).substring(with: $0.range).contains("\u{FFFC}")
            } ?? true
        // What an undo or a redo puts back was in place: a card moved then
        // would be a step on the redo's stack.
        if cards, !edits.undoing, let lifted = clipboard.liftCards() {
            changed = NSUnionRange(changed, lifted)
        }
        if let r = typed?.range, r.length == 1, textView.markedTextRange == nil,
            rules.inlineShortcut(NSMaxRange(r))
        {
            changed = NSRange(location: r.location, length: 0)
        }
        defer { caretMenu.openOrUpdate() }
        // All the paragraphs typed or pasted in, each of them a block.
        edited(paragraphs(around: changed))
    }

    func textViewDidChangeSelection(_ textView: UITextView) {
        // A composition can end with no change of the text, as the caret
        // moves away from it.
        _ = session.sendComposed()
        format.updateTypingAttributes()
        if !session.updating { caretMenu.update() }
        due.updateDateOffer()
    }

    func textViewDidBeginEditing(_ textView: UITextView) {
        onEditing?(true)
        due.updateDateOffer()
    }

    func textViewDidEndEditing(_ textView: UITextView) {
        onEditing?(false)
        caretMenu.close()
        due.updateDateOffer()
    }

    func textView(
        _ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]
    ) -> UIMenu? {
        menus.editMenu(for: range, suggested: suggestedActions)
    }

    func textView(
        _ textView: UITextView, primaryActionFor textItem: UITextItem,
        defaultAction: UIAction
    ) -> UIAction? {
        guard case .link(let url) = textItem.content else { return defaultAction }
        return UIAction { [weak self] _ in self?.open(url) }
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) { headings.didScroll() }

    func scrollViewDidEndScrollingAnimation(_ scrollView: UIScrollView) {
        headings.didEndScrollingAnimation()
    }

    func scrollViewWillBeginDragging(_ scrollView: UIScrollView) { headings.willBeginDragging() }

    // MARK: What the page and the app ask of it

    /// paragraph returns the paragraph at location: its text's range,
    /// without the separator, and its block.
    func paragraph(at location: Int) -> (range: NSRange, info: BlockInfo) {
        text.paragraph(at: location)
    }

    /// setBlock makes info the block of the paragraph at location, as a
    /// step to undo unless it is only how the page shows, as a fold.
    func setBlock(_ info: BlockInfo, at location: Int, undoable: Bool = true) {
        edits.setBlock(info, at: location, undoable: undoable)
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
        for (_, info) in text.paragraphs(touching: textView.selectedRange)
        where !ids.contains(info.id) {
            ids.append(info.id)
        }
        return ids
    }

    /// reveal scrolls to a block and puts the caret at its start.
    /// atTop brings the block to the top of the page, as the outline goes
    /// to a heading, rather than only into view.
    func reveal(blockID: String, atTop: Bool = false) {
        headings.reveal(blockID: blockID, atTop: atTop)
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

    /// open opens a link: a page in the app, or a web or mail link in the
    /// app for it.
    func open(_ url: URL) {
        if url.scheme == "leaflet" {
            let id = url.pathComponents.dropFirst().first ?? ""
            guard !id.isEmpty else { return }
            // A link to a block of the page opens the page there.
            if let block = url.fragment, !block.isEmpty, let openAt = pageLinks?.openAt {
                openAt(id, block)
            } else {
                pageLinks?.open(id)
            }
            return
        }
        UIApplication.shared.open(url)
    }

    /// setData gives a block without text new data, as its editor saved
    /// it, in its place.
    func setData(_ data: [String: JSONValue], ofBlock id: String) {
        replaceBlock(DocNode(id: id, type: "", data: data, children: nil))
    }

    func replaceBlock(_ node: DocNode) { commands.replaceBlock(node) }

    func removeBlock(_ id: String) { commands.removeBlock(id) }

    func insertBlock(_ node: DocNode, at location: Int? = nil) {
        commands.insertBlock(node, at: location)
    }

    func insertBlocks(_ nodes: [DocNode], after id: String) {
        commands.insertBlocks(nodes, after: id)
    }

    func indent(by step: Int) { commands.indent(by: step) }

    func setType(_ type: String, _ data: [String: JSONValue] = [:]) {
        commands.setType(type, data)
    }

    func moveBlock(by step: Int) { commands.moveBlock(by: step) }

    func softBreak() { commands.softBreak() }

    func toggleInline(_ key: NSAttributedString.Key) { format.toggle(key) }

    func setInline(_ key: NSAttributedString.Key, _ value: Any?) { format.set(key, value) }

    func editLink() { format.editLink() }

    func copied() { clipboard.copied() }

    func copiedOut() { clipboard.copiedOut() }

    func copyPlainText() { clipboard.copyPlainText() }

    func backspaceAtPageStart() -> Bool { rules.backspaceAtPageStart() }

    func roomToCompose() { rules.roomToCompose() }

    /// menu is the menu at the caret while "/" or "@" is being typed.
    var menu: CaretMenu? { caretMenu.menu }

    func closeMenu() { caretMenu.close() }

    func moveInMenu(_ step: Int) { caretMenu.move(step) }

    func insertMenu(_ done: @escaping ([UIMenuElement]) -> Void) { caretMenu.insertMenu(done) }

    func insertItem(_ item: MenuItem) { caretMenu.insertItem(item) }

    func accessibilityActions() -> [UIAccessibilityCustomAction] {
        EditorAccessibility.actions(self)
    }
}
