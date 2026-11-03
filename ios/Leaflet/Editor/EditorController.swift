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

    /// pageLinks open the pages subpages' cards show.
    var pageLinks: PageLinks? {
        didSet { textView.blocks.links = pageLinks }
    }
    /// meta is the page's meta as the core last sent it.
    private var meta: JSONValue?
    /// trailing is the block of the last paragraph, which no character
    /// holds when it is empty. It is kept while the paragraph has text too
    /// (keepTrailing), and counts only once it is empty.
    private var trailing: BlockInfo?
    /// bypass lets the controller's own insertions through the rules;
    /// putting says text going in has its blocks given after it, which the
    /// text view's change takes nothing from (put).
    private var bypass = false
    private var putting = false
    /// inEdit counts the text view's own edits under way, the keyboard's,
    /// which the rules change the text in: the text view takes changes to
    /// the text then as part of its edit (put).
    private var inEdit = 0
    /// updating says a change from outside is going in: the selection it
    /// moves on the way says nothing of the menu at the caret, whose "/" or
    /// "@" moves with it once it is in.
    private var updating = false
    private var sendTask: Task<Void, Never>?
    /// unsentSince is when the first edit not yet sent was made: typing
    /// that never pauses is still sent, every maxWait.
    private var unsentSince: ContinuousClock.Instant?
    /// pause is how long after the last edit the page is sent, and maxWait
    /// the longest an edit waits while typing goes on: a crash in a long
    /// run of fast typing loses no more.
    private static let pause = Duration.milliseconds(250)
    private static let maxWait = Duration.seconds(2)
    /// held is a change from outside that came while text was being
    /// composed, as Japanese or Chinese is typed: put in then, it would
    /// commit the composition, or move it under the input method. Once the
    /// composition ends, what was typed goes to the core, which merges it
    /// with the change and sends the merge (sendComposed).
    private var held: (content: DocContent, tinted: Set<String>)?
    /// typing are the block and formats what is typed next takes. The
    /// text view's own typing attributes drop the editor's keys, so the
    /// editor sets them on typed text itself.
    private var typing: [NSAttributedString.Key: Any] = [:]
    /// typed is where text went in that the rules let through, for
    /// textViewDidChange to give it the typing attributes as they were:
    /// the selection's change, which comes first, reads them again from
    /// the text just typed, losing a format toggled for it.
    private var typed: (range: NSRange, attributes: [NSAttributedString.Key: Any])?
    /// emptied is the block of the page's last paragraph, as an edit
    /// empties it, for the trailing block to keep.
    private var emptied: BlockInfo?
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

    /// formatKeys are the inline formats typing carries on.
    private static let formatKeys: [NSAttributedString.Key] = [
        .leafletBold, .leafletItalic, .leafletCode, .leafletUnderline, .leafletStrike,
        .leafletColor, .leafletBackground,
    ]
    /// shownKeys are the attributes the style draws the formats with, which
    /// typing never carries on: the style draws them again for the text.
    private static let shownKeys: [NSAttributedString.Key] = [
        .underlineStyle, .underlineColor, .strikethroughStyle, .strikethroughColor, .link,
        .backgroundColor,
    ]

    init(style: EditorStyle) {
        self.style = style
        blocksPalette = style.palette.key
        BlockLook.palette = style.palette
        BlockLook.textSize = style.textSize
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
            self.updateTypingAttributes()
            self.textView.blocks.reset { $0.type == "table" }
            self.textView.relayout()
        }
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
        scheduleDue()
        DispatchQueue.main.async { [weak self] in self?.updateHeading() }
    }

    /// close lets the page's views go as it closes: the blocks' views
    /// joined the view controller the page is in, which outlives it.
    func close() {
        closeMenu()
        dueAt?.cancel()
        dueAt = nil
        // The page's videos stop with it: opened again, they show their
        // pictures.
        VideoPlayback.shared.playing.subtract(
            DocumentMapper.blocks(storage, trailing: trailing)
                .filter { $0.info.type == "video" }.map { $0.info.id })
        textView.blocks.reset()
    }

    /// emptyPageBlock is the block of an empty page, when it draws a
    /// marker or a card, with its font and the palette.
    var emptyPageBlock: (BlockInfo, UIFont, Palette)? {
        guard storage.length == 0, let trailing, trailing.type != "paragraph",
            trailing.type != "heading"
        else { return nil }
        return (trailing, style.baseFont(trailing), style.palette)
    }

    /// isBlank reports whether the page is one empty paragraph.
    var isBlank: Bool { storage.length == 0 && (trailing?.type ?? "paragraph") == "paragraph" }

    private func reportBlank() {
        let blank = isBlank
        guard blank != wasBlank else { return }
        wasBlank = blank
        // On the next turn: the change may come from an update of the
        // page's view, which what the page shows must not change during.
        DispatchQueue.main.async { [weak self] in self?.onBlank?(blank) }
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
        onHeading?(found)
    }

    /// update brings content from outside the editor (sync, an agent, a
    /// merge) into the text: only the blocks that changed, where the
    /// blocks are the same; all of it otherwise. The caret stays in its
    /// block, and the page where it is: an agent writing does not scroll
    /// it, the user does.
    func update(_ content: DocContent, tinted: Set<String>, byAgent: Bool = false) {
        if textView.markedTextRange != nil {
            held = (content, tinted)
            return
        }
        held = nil
        defer { reportBlank() }
        updating = true
        defer { updating = false }
        // What was typed is in content, or given up: the page can't be
        // changed. Nothing waits to be sent.
        sendTask?.cancel()
        sendTask = nil
        unsentSince = nil
        meta = content.meta
        // The selection, and the menu's "/" or "@", as places in their
        // blocks, which keep them as the text around them changes.
        let sel = textView.selectedRange
        let start = position(at: sel.location)
        let end = position(at: NSMaxRange(sel))
        let anchor = menu.map { position(at: $0.anchor) }
        let offset = textView.contentOffset
        let (text, newTrailing) = DocumentMapper.attributedString(content, tinted: tinted)
        let old = DocumentMapper.paragraphs(storage, trailing: trailing)
        let new = DocumentMapper.paragraphs(text, trailing: newTrailing)
        // The blocks the same at either end, by ID, are compared one by
        // one; those between, which came, went or moved, are replaced at
        // once, and only what is replaced is styled again: the whole page
        // laid out again for each edit an agent makes would hold up
        // typing on a long one.
        var head = 0
        while head < old.count, head < new.count, old[head].info.id == new[head].info.id {
            head += 1
        }
        var tail = 0
        while tail < old.count - head, tail < new.count - head,
            old[old.count - 1 - tail].info.id == new[new.count - 1 - tail].info.id
        {
            tail += 1
        }
        let sameBlocks = old.count == new.count && head == old.count
        // A block that only came or went is replaced with the one before
        // it, or after, for neither side of the change to be empty.
        if !sameBlocks, head + tail == old.count || head + tail == new.count {
            if head > 0 { head -= 1 } else { tail -= 1 }
        }
        var changed = false
        // styled are where the text replaced now is, as each replacement
        // before it moves it.
        var styled: [NSRange] = []
        func put(_ range: NSRange, _ t: NSAttributedString, last info: BlockInfo) {
            changed = true
            storage.replaceCharacters(in: range, with: t)
            let moved = t.length - range.length
            for i in styled.indices { styled[i].location += moved }
            styled.append(NSRange(location: range.location, length: t.length))
            // The separator after the paragraph is its block's too.
            let sep = range.location + t.length
            if sep < storage.length {
                storage.addAttribute(
                    .leafletBlock, value: info, range: NSRange(location: sep, length: 1))
            }
        }
        func keep(_ o: DocumentMapper.Paragraph, _ n: DocumentMapper.Paragraph) {
            // A block nested or taken out keeps its node: its depth is
            // where it is in the tree.
            let same =
                o.info.depth == n.info.depth
                && DocumentMapper.node(for: o.info, text: o.text)
                    == DocumentMapper.node(for: n.info, text: n.text)
                && (o.text.length == 0 || n.text.length == 0
                    || (o.text.attribute(.leafletTinted, at: 0, effectiveRange: nil) != nil)
                        == (n.text.attribute(.leafletTinted, at: 0, effectiveRange: nil) != nil))
            if !same { put(o.range, n.text, last: n.info) }
        }
        do {
            storage.beginEditing()
            // From the end, for the ranges before each change to stay.
            for j in (0..<tail).reversed() {
                keep(old[old.count - tail + j], new[new.count - tail + j])
            }
            if !sameBlocks {
                let from = old[head].range.location
                let to = NSMaxRange(old[old.count - tail - 1].range)
                let newFrom = new[head].range.location
                let newTo = NSMaxRange(new[new.count - tail - 1].range)
                put(
                    NSRange(location: from, length: to - from),
                    text.attributedSubstring(
                        from: NSRange(location: newFrom, length: newTo - newFrom)),
                    last: new[new.count - tail - 1].info)
            }
            for j in (0..<head).reversed() { keep(old[j], new[j]) }
            if changed { trailing = newTrailing }
            storage.endEditing()
        }
        // Changes made to the storage itself are no undo steps: the text
        // view records only typing and its own edits. Those steps were
        // made over text that has moved since, which they no longer fit.
        guard changed else { return }
        for r in styled {
            style.apply(to: storage, in: r)
            restyleNumbersAfter(r)
        }
        keepTrailing()
        redrawEnd()
        textView.undoManager?.removeAllActions()
        // An agent's run is one step to undo, as on the desktop: the core
        // undoes it, its ops taken back over what came since.
        if byAgent, let undo = textView.undoManager, onUndoRun != nil {
            undo.registerUndo(withTarget: self) { c in c.onUndoRun?() }
        }
        tellKeyboard()
        if let menu, let anchor {
            // The menu goes on from its "/" or "@" where it now is, or
            // closes once it is gone.
            if let at = place(anchor) { menu.anchor = at } else { closeMenu() }
        }
        if let s = place(start) {
            textView.selectedRange = NSRange(location: s, length: max((place(end) ?? s) - s, 0))
        } else if let s = nearest(start.id, in: old) {
            textView.selectedRange = NSRange(location: s, length: 0)
        }
        // The selection back where it was, the menu goes by it.
        updating = false
        updateMenu()
        updateFolds()
        updateTypingAttributes()
        scheduleDue()
        hold(offset)
    }

    /// tint shows the blocks an agent run last changed, ids, lit, or no
    /// more: a run's last tint comes after its last change, and an undo
    /// takes it away without one.
    func tint(_ ids: Set<String>) {
        storage.beginEditing()
        for p in DocumentMapper.blocks(storage, trailing: trailing) where p.range.length > 0 {
            let lit =
                storage.attribute(.leafletTinted, at: p.range.location, effectiveRange: nil) != nil
            let want = ids.contains(p.info.id)
            guard lit != want else { continue }
            if want {
                storage.addAttribute(.leafletTinted, value: true, range: p.range)
            } else {
                storage.removeAttribute(.leafletTinted, range: p.range)
            }
            style.apply(to: storage, in: p.range)
        }
        storage.endEditing()
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
        for p in DocumentMapper.blocks(storage, trailing: trailing) {
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

    /// keepShown unfolds what folds the block of id away, as a new line or
    /// a block moved goes where a toggle or a heading folds: the caret
    /// would be out of sight. What folds a block is the first block before
    /// it that shows, the blocks between folded away with it.
    private func keepShown(_ id: String) {
        for _ in 0..<8 {
            updateFolds()
            guard folded.contains(id) else { return }
            let all = DocumentMapper.blocks(storage, trailing: trailing)
            guard let i = all.firstIndex(where: { $0.info.id == id }),
                let folder = all[..<i].last(where: { !folded.contains($0.info.id) }),
                folder.info.data["collapsed"]?.bool == true
            else { return }
            setBlock(folder.info.setting("collapsed", .bool(false)), at: folder.range.location)
        }
    }

    @objc private func willEnterForeground() {
        refreshBlocks()
        refreshDue()
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
        keepTrailing()
        fitDepths()
        // The last block, as deep as it now is.
        keepTrailing()
        style.apply(to: storage, in: range)
        restyleNumbersAfter(range)
        updateTypingAttributes()
        updateFolds()
        reportBlank()
        sendTask?.cancel()
        sendTask = nil
        let now = ContinuousClock.now
        let since = unsentSince ?? now
        unsentSince = since
        // Text being composed is not the page's yet: it goes once it is.
        guard textView.markedTextRange == nil else { return }
        if sendComposed() { return }
        let wait = min(Self.pause, max(since + Self.maxWait - now, .zero))
        sendTask = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled else { return }
            self?.send()
        }
    }

    /// sendComposed sends the page at once when a change from outside
    /// waited for a composition that has ended (held), and reports whether
    /// it did: the core merges what was typed with the change, and the
    /// editor shows the merge as it comes.
    private func sendComposed() -> Bool {
        guard held != nil, textView.markedTextRange == nil else { return false }
        held = nil
        send()
        return true
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

    /// copyPlainText copies the selection as text alone, without its
    /// formats, as the desktop's Copy as Plain Text: a line per block, for
    /// apps that would paste the formats the system's Copy keeps.
    func copyPlainText() {
        let range = textView.selectedRange
        guard textView.isFirstResponder, range.length > 0, let plainCopy else { return }
        let nodes =
            DocumentMapper.content(storage.attributedSubstring(from: range), meta: nil)
            .document.children ?? []
        Task {
            if let text = await plainCopy(nodes), !text.isEmpty {
                UIPasteboard.general.string = text
            }
        }
    }

    /// withCopyPlainText puts Copy as Plain Text after the system's Copy
    /// in an edit menu's actions, or first when they have no Copy: a page
    /// of plain text, not Copy's own symbol.
    private func withCopyPlainText(_ actions: [UIMenuElement]) -> [UIMenuElement] {
        let item = UIAction(
            title: L.editorCopyPlainText, image: UIImage(systemName: "doc.plaintext")
        ) { [weak self] _ in self?.copyPlainText() }
        var placed = false
        func place(_ elements: [UIMenuElement]) -> [UIMenuElement] {
            elements.flatMap { element -> [UIMenuElement] in
                if placed { return [element] }
                if let menu = element as? UIMenu {
                    return [menu.replacingChildren(place(menu.children))]
                }
                if let command = element as? UICommand,
                    command.action == #selector(UIResponderStandardEditActions.copy(_:))
                {
                    placed = true
                    return [command, item]
                }
                return [element]
            }
        }
        let out = place(actions)
        return placed ? out : [item] + out
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

    /// setData gives a block without text new data, as its editor saved
    /// it, in its place.
    func setData(_ data: [String: JSONValue], ofBlock id: String) {
        replaceBlock(DocNode(id: id, type: "", data: data, children: nil))
    }

    /// replaceBlock puts node in place of the block without text of its
    /// ID, as a table's sheet gives it with its cells; a node of no type
    /// keeps the block's.
    func replaceBlock(_ node: DocNode) {
        // A page the user may only view changes from no command: its
        // change would never be saved, and would go with their next one.
        guard textView.isEditable else { return }
        let all = DocumentMapper.blocks(storage, trailing: trailing)
        guard let p = all.first(where: { $0.info.id == node.id }), !p.info.isText else { return }
        var node = node
        if node.type.isEmpty { node.type = p.info.type }
        let (attachment, info) = DocumentMapper.paragraph(node, depth: p.info.depth)
        let at = NSRange(location: p.range.location, length: 1)
        keepingScroll {
            replace(at, with: attachment)
            setBlock(info, at: at.location, undoable: false)
        }
    }

    /// reveal scrolls to a block and puts the caret at its start.
    /// atTop brings the block to the top of the page, as the outline goes
    /// to a heading, rather than only into view.
    func reveal(blockID: String, atTop: Bool = false) {
        let all = DocumentMapper.blocks(storage, trailing: trailing)
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
        unsentSince = nil
        uniqueIDs()
        onEdit?(DocumentMapper.content(storage, trailing: trailing, meta: meta))
    }

    /// uniqueIDs gives a block whose ID a block before it has a new one, in
    /// the text: DocumentMapper.content would give it another each time
    /// the page is sent, a new block to the core each time.
    private func uniqueIDs() {
        var seen = Set<String>()
        for p in DocumentMapper.blocks(storage, trailing: trailing) {
            guard seen.contains(p.info.id) else {
                seen.insert(p.info.id)
                continue
            }
            let info = p.info.with(id: BlockInfo.newID())
            seen.insert(info.id)
            // The separator after the paragraph is its block's too.
            var whole = p.range
            if NSMaxRange(whole) < storage.length { whole.length += 1 }
            if whole.length > 0 {
                storage.addAttribute(.leafletBlock, value: info, range: whole)
            } else {
                trailing = info
                redrawEnd()
            }
        }
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
            at = NSMaxRange(para)
        }
        // At once, for each item's number to go on from the one before.
        if at > rest.location {
            style.apply(
                to: storage, in: NSRange(location: rest.location, length: at - rest.location))
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

    /// setBlock makes info the block of the paragraph at location, as a
    /// step to undo unless it is only how the page shows, as a fold.
    func setBlock(_ info: BlockInfo, at location: Int, undoable: Bool = true) {
        let (range, old) = paragraph(at: location)
        var whole = range
        if NSMaxRange(range) < storage.length { whole.length += 1 }
        if whole.length > 0 {
            storage.addAttribute(.leafletBlock, value: info, range: whole)
        }
        let before = trailing
        if NSMaxRange(range) == storage.length { trailing = info }
        if trailing !== before { redrawEnd() }
        if undoable, old !== info { registerBlockUndo(textView.undoManager, now: info, was: old) }
        // The block is the one given, whatever an edit emptying the
        // paragraph before it left.
        emptied = nil
        edited(whole.length > 0 ? whole : NSRange(location: range.location, length: 0))
    }

    /// registerBlockUndo puts block was back in place of now, as a step to
    /// undo, and now on redo. It finds now itself, wherever the text has
    /// moved it since: the text view's own steps undone before it move it.
    private func registerBlockUndo(_ undo: UndoManager?, now: BlockInfo, was: BlockInfo) {
        undo?.registerUndo(withTarget: self) { c in
            guard let at = c.location(of: now) else { return }
            c.setBlock(was, at: at, undoable: false)
            c.registerBlockUndo(undo, now: was, was: now)
        }
    }

    /// location is where the paragraph of block info is, or nil once no
    /// paragraph is that block.
    private func location(of info: BlockInfo) -> Int? {
        let ns = storage.string as NSString
        if trailing === info, ns.length == 0 || ns.character(at: ns.length - 1) == 0x0A {
            return ns.length
        }
        var found: Int?
        storage.enumerateAttribute(.leafletBlock, in: NSRange(location: 0, length: ns.length)) {
            v, r, stop in
            if (v as? BlockInfo) === info {
                found = r.location
                stop.pointee = true
            }
        }
        return found
    }

    /// redrawEnd lays the last paragraph out again, whose fragment draws
    /// the empty block after it.
    private func redrawEnd() {
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
        guard let trailing else { return nil }
        return BlockLayoutFragment.Trailing(
            info: trailing, font: style.baseFont(trailing),
            ordinal: trailing.type == "numbered_list"
                ? EditorStyle.ordinal(storage, at: storage.length, trailing) : 1)
    }

    /// normalize keeps each paragraph in and near range one block: the
    /// block of its first character, and one apart from the paragraphs
    /// before it, as lines pasted in all take the block they went in.
    private func normalize(around range: NSRange) {
        let ns = storage.string as NSString
        guard ns.length > 0 else { return }
        let start = ns.paragraphRange(
            for: NSRange(location: min(range.location, ns.length - 1), length: 0))
        let endLoc = min(NSMaxRange(range) + 1, ns.length)
        var at =
            start.location > 0
            ? ns.paragraphRange(for: NSRange(location: start.location - 1, length: 0)).location : 0
        var seen = Set<String>()
        while at < ns.length {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            // Text typed into an empty block may have no block yet: the
            // rest of the paragraph, its separator, or the trailing block
            // holds the paragraph's.
            var info =
                blockAt(para.location) ?? firstBlock(in: para) ?? trailing
                ?? BlockInfo.paragraph()
            if seen.contains(info.id) { info = derived(info) }
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
            seen.insert(info.id)
            at = NSMaxRange(para)
            if at > endLoc && !differs { break }
        }
        if at >= ns.length, let trailing, seen.contains(trailing.id),
            ns.character(at: ns.length - 1) == 0x0A
        {
            self.trailing = derived(trailing)
        }
    }

    /// firstBlock is the first block a character in range holds.
    private func firstBlock(in range: NSRange) -> BlockInfo? {
        var found: BlockInfo?
        storage.enumerateAttribute(.leafletBlock, in: range) { v, _, stop in
            if let b = v as? BlockInfo {
                found = b
                stop.pointee = true
            }
        }
        return found
    }

    /// keepTrailing keeps the page's last paragraph a block of its own.
    /// Once empty, no character holds it, and the trailing block does: the
    /// last paragraph's block, kept while it has text too, for an edit
    /// the editor isn't told of, as the text view's own undo, to leave it
    /// that block; or, as an edit empties it, the block it was then.
    private func keepTrailing() {
        let ns = storage.string as NSString
        let before = trailing
        if ns.length > 0, ns.character(at: ns.length - 1) != 0x0A {
            let last = ns.paragraphRange(for: NSRange(location: ns.length - 1, length: 0))
            trailing = blockAt(last.location) ?? trailing
        } else {
            if let emptied {
                trailing = emptied
            } else if trailing == nil {
                trailing =
                    ns.length > 0 ? blockAt(ns.length - 1).map(derived) : BlockInfo.paragraph()
            }
            // An empty block without text, its attachment gone, as the
            // page's last image deleted, is an empty paragraph, as
            // normalize has it: it would be saved as the block it was.
            if let t = trailing, !t.isText { trailing = t.with(type: "paragraph", data: [:]) }
        }
        emptied = nil
        if trailing !== before { redrawEnd() }
    }

    /// fitDepths keeps each block as deep as the page's content has it, as
    /// DocumentMapper.content reads the text: a block goes in the block
    /// before it that holds blocks, one deeper, or at the top. A block
    /// shown deeper, as one whose list item became text, would be saved
    /// where it shows not.
    private func fitDepths() {
        let ns = storage.string as NSString
        let before = trailing
        var holders: [Int] = []
        var at = 0
        var changed: [NSRange] = []
        repeat {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            let last = para.length == 0 || NSMaxRange(para) == ns.length
            guard let info = para.length > 0 ? blockAt(para.location) : trailing else { break }
            while let top = holders.last, top >= info.depth { holders.removeLast() }
            let depth = holders.last.map { $0 + 1 } ?? 0
            var fitted = info
            if depth != info.depth {
                fitted = info.with(depth: depth)
                if para.length > 0 {
                    storage.addAttribute(.leafletBlock, value: fitted, range: para)
                    changed.append(para)
                } else {
                    trailing = fitted
                }
            }
            if fitted.isList { holders.append(depth) }
            at = NSMaxRange(para)
            // The empty paragraph after the last separator is the
            // trailing block's.
            if last, para.length > 0, ns.character(at: ns.length - 1) == 0x0A,
                let t = trailing
            {
                while let top = holders.last, top >= t.depth { holders.removeLast() }
                let d = holders.last.map { $0 + 1 } ?? 0
                if d != t.depth { trailing = t.with(depth: d) }
            }
        } while at < ns.length
        for r in changed { style.apply(to: storage, in: r) }
        if trailing !== before { redrawEnd() }
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
        case "bulleted_list", "numbered_list", "paragraph":
            var d = info.data
            d["due"] = nil
            return BlockInfo(id: BlockInfo.newID(), type: info.type, data: d, depth: info.depth)
        default:
            // A quote or a callout ends with the line, as on the desktop
            // (editor.continues): only lists go on.
            return BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth)
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
        for k in Self.formatKeys + Self.shownKeys + [.leafletHref, .leafletMention, .leafletTinted]
        {
            attrs[k] = nil
        }
        attrs.merge(typing) { $1 }
        attrs[.paragraphStyle] = style.paragraphStyle(info)
        attrs[.font] = style.baseFont(info)
        attrs[.foregroundColor] = UIColor(style.palette.text)
        textView.typingAttributes = attrs
    }

    // MARK: Caret

    /// Position is a place in the text as its block and the offset in it,
    /// which stays the same place as the text around the block changes.
    private typealias Position = (id: String, offset: Int)

    private func caretPosition() -> Position? { position(at: textView.selectedRange.location) }

    private func position(at location: Int) -> Position {
        let (range, info) = paragraph(at: location)
        return (info.id, location - range.location)
    }

    /// place is where position is in the text now, or nil once its block
    /// is gone.
    private func place(_ position: Position) -> Int? {
        for p in DocumentMapper.blocks(storage, trailing: trailing)
        where p.info.id == position.id {
            return p.range.location + min(position.offset, p.range.length)
        }
        return nil
    }

    /// nearest is where the caret goes once a change from outside took
    /// away its block, id, which was in the paragraphs before it, old: the
    /// end of the nearest block before it that stayed, or else the start
    /// of the nearest after it, or else the start of the page. Left to
    /// the text view, the caret would go where the change ended, which may
    /// be far from where the user was.
    private func nearest(_ id: String, in old: [DocumentMapper.Paragraph]) -> Int? {
        guard let i = old.firstIndex(where: { $0.info.id == id }) else { return nil }
        var now: [String: NSRange] = [:]
        for p in DocumentMapper.blocks(storage, trailing: trailing) where now[p.info.id] == nil {
            now[p.info.id] = p.range
        }
        for p in old[..<i].reversed() {
            if let r = now[p.info.id] { return NSMaxRange(r) }
        }
        for p in old[(i + 1)...] {
            if let r = now[p.info.id] { return r.location }
        }
        return 0
    }

    private func restoreCaret(_ caret: Position?) {
        guard let caret, let at = place(caret) else { return }
        textView.selectedRange = NSRange(location: at, length: 0)
    }

    // MARK: Rules

    func textView(
        _ textView: UITextView, shouldChangeTextIn range: NSRange, replacementText text: String
    ) -> Bool {
        // An edit that empties the page's last paragraph leaves no
        // character to hold its block, which the trailing block keeps.
        if text.isEmpty, range.length > 0, NSMaxRange(range) == storage.length,
            paragraph(at: range.location).range.location == range.location
        {
            emptied = blockAt(range.location)
        }
        // Undo and redo put back what was: a rule taking one for an edit,
        // as Backspace at a list item's start, would refuse it, and leave
        // the steps before it where the text no longer is, which undone
        // there crashed the app.
        if bypass || textView.undoManager?.isUndoing == true
            || textView.undoManager?.isRedoing == true
        {
            return true
        }
        inEdit += 1
        defer { inEdit -= 1 }
        let allowed = follow(range, text)
        // A rule changed the text itself, in place of the keyboard's edit.
        if !allowed {
            emptied = nil
            tellKeyboard()
        }
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
            if let item = menu.current {
                pick(item)
                return false
            }
            closeMenu()
            // Return or Tab before the menu has its items is the key's
            // own; once it has none to pick, it only closes the menu.
            if menu.loaded { return false }
        }
        // A mention is one unit, as on the desktop: an edit that cuts one
        // takes all of it, and what is typed inside one goes after it.
        if text != "\n", text != "\t" {
            let whole = widenToMentions(range)
            if whole != range {
                insert(text, replacing: whole)
                textView.selectedRange = NSRange(
                    location: whole.location + (text as NSString).length, length: 0)
                return false
            }
            if range.length == 0, let run = mentionRun(at: range.location),
                run.location < range.location
            {
                insert(text, replacing: NSRange(location: NSMaxRange(run), length: 0))
                textView.selectedRange = NSRange(
                    location: NSMaxRange(run) + (text as NSString).length, length: 0)
                return false
            }
        }
        let (para, info) = paragraph(at: range.location)
        switch text {
        case "\n":
            return enter(range, para, info)
        case "":
            guard range.length == 1 else { return true }
            let ns = storage.string as NSString
            let sel = textView.selectedRange
            if ns.character(at: range.location) == 0x0A, sel.length == 0 {
                // Backspace at a block's start deletes the separator
                // before it, the caret after it: the rules go by the block
                // the caret is in. Delete at a block's end deletes the
                // same separator, the caret before it.
                if sel.location == NSMaxRange(range) {
                    let (caretPara, caretInfo) = paragraph(at: NSMaxRange(range))
                    if caretPara.location == NSMaxRange(range) {
                        return backspaceAtStart(caretPara, caretInfo)
                    }
                } else if sel.location == range.location {
                    return deleteAtEnd(para, info)
                }
            }
            return true
        case "\t":
            indent(by: 1)
            return false
        default:
            break
        }
        if !info.isText {
            // Text typed by a block without text goes in a new block after
            // it.
            insertParagraph(after: para, info: BlockInfo.paragraph(), text: text)
            return false
        }
        if text == " " || text == "\u{3000}", markdownShortcut(range, para, info, typing: text) {
            return false
        }
        if dividerShortcut(text, range, para, info) { return false }
        if info.type != "code", (text as NSString).length > 1,
            pastedCopy(text, replacing: range, para, info)
        {
            return false
        }
        if info.type != "code", (text as NSString).length > 1, !text.contains("\u{FFFC}"),
            pasted(text, replacing: range, para, info)
        {
            return false
        }
        let length = (text as NSString).length
        if text.utf16.contains(where: { [0x0A, 0x0D, 0x2029, 0x85].contains($0) }) {
            // Lines pasted, or written by dictation or Writing Tools: a
            // code block keeps them as its line breaks, in its one block.
            var lines = DocumentMapper.lines(text)
            if info.type == "code" {
                lines = lines.replacingOccurrences(of: "\n", with: String(lineBreak))
            }
            if lines != text {
                let attributes = typing
                insert(lines, replacing: range)
                let r = NSRange(location: range.location, length: (lines as NSString).length)
                takeTyping(r, attributes)
                edited(paragraphs(around: r))
                return false
            }
        }
        typed = (
            NSRange(location: range.location, length: length),
            range.length > 0 ? replacing(range) : typing
        )
        if info.type != "code", let trigger = menuTrigger(text, at: range.location, in: para) {
            opening = (range.location, trigger)
        }
        return true
    }

    /// Clip is what Copy or Cut last took in an editor, as the desktop's
    /// editor keeps its own copy: the text it left on the clipboard, and
    /// the blocks it stands for, whole or cut to the selection, or the
    /// formatted text of a selection within a block. count is the
    /// clipboard's changeCount once it was there, which another app's copy
    /// since, of the same text maybe, changes.
    struct Clip {
        var text: String?
        var blocks: [DocNode] = []
        var inline: NSAttributedString?
        var count: Int?
    }

    /// clip is Leaflet's own copy, as the app's editors share it.
    private static var clip: Clip?

    /// copied keeps what the system's Copy or Cut takes of the selection
    /// as Leaflet's own copy, before it goes: text within a block, which
    /// pastes back with its formats, or blocks, which go on the clipboard
    /// as their Markdown, as the desktop's Copy puts them, for other apps,
    /// and paste back here as the blocks they were, their kind and nesting
    /// too, which the system's plain text has lost.
    func copied() {
        let range = textView.selectedRange
        guard range.length > 0 else { return }
        let piece = storage.attributedSubstring(from: range)
        if !piece.string.contains("\n"), !piece.string.contains("\u{FFFC}") {
            Self.clip = Clip(text: piece.string, inline: piece)
            return
        }
        let blocks = DocumentMapper.content(piece, meta: nil).document.children ?? []
        let clip = Clip(blocks: blocks)
        Self.clip = clip
        guard let copyText else { return }
        Task {
            guard let text = await copyText(blocks), !text.isEmpty else { return }
            UIPasteboard.general.string = text
            Self.clip?.text = text
            Self.clip?.count = UIPasteboard.general.changeCount
        }
    }

    /// copiedOut marks the clip as on the clipboard, as the system's Copy
    /// or Cut has put the text within a block there.
    func copiedOut() {
        if Self.clip?.inline != nil { Self.clip?.count = UIPasteboard.general.changeCount }
    }

    /// pastedCopy pastes Leaflet's own copy, when text is what it left on
    /// the clipboard, and reports whether it did: blocks as the blocks they
    /// were, with IDs of their own; text from within a block with its
    /// formats, in the block it goes in.
    private func pastedCopy(
        _ text: String, replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        let lines = { (s: String) in DocumentMapper.lines(s) }
        guard let clip = Self.clip, let copied = clip.text, lines(copied) == lines(text),
            clip.count == UIPasteboard.general.changeCount
        else {
            return false
        }
        if let inline = clip.inline {
            let made = NSMutableAttributedString(attributedString: inline)
            made.addAttribute(
                .leafletBlock, value: info, range: NSRange(location: 0, length: made.length))
            replace(range, with: made)
            let r = NSRange(location: range.location, length: made.length)
            textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
            edited(paragraphs(around: r))
            return true
        }
        guard !clip.blocks.isEmpty else { return false }
        pasteBlocks(clip.blocks.map(Self.renewed), replacing: range, para, info)
        return true
    }

    /// pasteBlocks puts blocks in place of range: the first joining the
    /// line it goes in, as the desktop pastes them, a block without text
    /// on a line of its own.
    private func pasteBlocks(
        _ blocks: [DocNode], replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) {
        let page = DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: blocks), meta: nil)
        let made = NSMutableAttributedString(
            attributedString: DocumentMapper.attributedString(page).text)
        if !(BlockInfo.textTypes.contains(blocks[0].type)), range.location > para.location {
            made.insert(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]), at: 0)
        }
        replace(range, with: made)
        let r = NSRange(location: range.location, length: made.length)
        textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
        edited(paragraphs(around: r))
    }

    /// pasted puts text pasted, or written by dictation, in place of range
    /// as the desktop's editor pastes it (mobile.PasteText), and reports
    /// whether it did: lines as the blocks their Markdown says, the first
    /// joining the line it goes in; a video's or a picture's link on an
    /// empty line as that block; one link on text chosen as its link, and
    /// at the caret as a link to itself. Other text goes in as typed.
    private func pasted(
        _ text: String, replacing range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        struct Pasted: Decodable {
            var blocks: [DocNode]?
            var media: DocNode?
            var link: String?
        }
        let empty = para.length == 0 && info.isText && range.length == 0
        var error: NSError?
        let raw = MobilePasteText(text, empty, &error)
        guard error == nil, let p = try? JSONDecoder().decode(Pasted.self, from: Data(raw.utf8))
        else { return false }
        if let blocks = p.blocks, !blocks.isEmpty {
            pasteBlocks(blocks, replacing: range, para, info)
            return true
        }
        if let media = p.media {
            insertBlock(media)
            return true
        }
        guard let link = p.link, !link.isEmpty else { return false }
        if range.length > 0 {
            textView.selectedRange = range
            setInline(.leafletHref, link)
            return true
        }
        var attributes = typing
        attributes[.leafletHref] = link
        replace(range, with: NSAttributedString(string: text, attributes: attributes))
        let r = NSRange(location: range.location, length: (text as NSString).length)
        textView.selectedRange = NSRange(location: NSMaxRange(r), length: 0)
        edited(paragraphs(around: r))
        return true
    }

    /// deleteAtEnd follows Delete at the end of a block, which joins the
    /// block after it to it: a block without text, after or before, is
    /// chosen first, to go with another Delete, as Backspace does.
    private func deleteAtEnd(_ para: NSRange, _ info: BlockInfo) -> Bool {
        let (next, nextInfo) = paragraph(at: NSMaxRange(para) + 1)
        if !nextInfo.isText {
            textView.selectedRange = next
            return false
        }
        if !info.isText {
            textView.selectedRange = para
            return false
        }
        return true
    }

    /// mentionRun is the whole of the mention the character at location
    /// is part of, or nil.
    private func mentionRun(at location: Int) -> NSRange? {
        guard location < storage.length else { return nil }
        var run = NSRange()
        guard
            storage.attribute(
                .leafletMention, at: location, longestEffectiveRange: &run,
                in: NSRange(location: 0, length: storage.length)) != nil
        else { return nil }
        return run
    }

    /// widenToMentions widens range to the whole of each mention it cuts,
    /// as doc.Delta.Widen does: a mention is taken out or formatted whole.
    private func widenToMentions(_ range: NSRange) -> NSRange {
        guard range.length > 0 else { return range }
        var out = range
        if let r = mentionRun(at: range.location) { out = NSUnionRange(out, r) }
        if let r = mentionRun(at: NSMaxRange(range) - 1) { out = NSUnionRange(out, r) }
        return out
    }

    /// replacing is what text put in place of range takes, as autocorrect,
    /// a text replacement or dictation puts a word in place of another, or
    /// a composition goes on: the formats, link and mention of the text it
    /// replaces, which the caret's typing would take off.
    private func replacing(_ range: NSRange) -> [NSAttributedString.Key: Any] {
        var out = typing
        let replaced = storage.attributes(at: range.location, effectiveRange: nil)
        for k in Self.formatKeys + [.leafletHref, .leafletMention] { out[k] = replaced[k] }
        // A mention is its name, and its page's link: text put in place of
        // all of it is neither.
        var run = NSRange()
        if storage.attribute(
            .leafletMention, at: range.location, longestEffectiveRange: &run,
            in: NSRange(location: 0, length: storage.length)) != nil,
            NSIntersectionRange(run, range) == run
        {
            out[.leafletMention] = nil
            out[.leafletHref] = nil
        }
        return out
    }

    /// takeTyping gives text typed in range the block and formats it was
    /// typed with: the text view's own typing attributes drop the editor's.
    private func takeTyping(_ r: NSRange, _ attributes: [NSAttributedString.Key: Any]) {
        guard NSMaxRange(r) <= storage.length else { return }
        for k in Self.formatKeys where attributes[k] == nil { storage.removeAttribute(k, range: r) }
        storage.removeAttribute(.leafletHref, range: r)
        storage.removeAttribute(.leafletMention, range: r)
        storage.addAttributes(attributes, range: r)
    }

    /// paragraphs is range widened to the paragraphs it touches, and the
    /// caret's.
    private func paragraphs(around range: NSRange) -> NSRange {
        let (caret, _) = paragraph(at: textView.selectedRange.location)
        let r = NSUnionRange(range, NSRange(location: caret.location, length: max(caret.length, 1)))
        return NSIntersectionRange(r, NSRange(location: 0, length: storage.length))
    }

    private func enter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) -> Bool {
        let caret = range.location
        if info.type == "code" {
            codeEnter(range, para, info)
            return false
        }
        if info.type == "paragraph", range.length == 0, caret == NSMaxRange(para), para.length >= 2,
            fenceEnter(para, info)
        {
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
        let collapsed = info.data["collapsed"]?.bool == true
        let nested = hasNested(para, info)
        if !atStart, info.type == "toggle_list", collapsed, nested {
            splitAfterNested(range, para, info)
            return false
        }
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
            // The text keeps its block. The one normalize gave it for a
            // moment, as the line went in before it, is no step to undo:
            // undone first, it would leave the text a new ID, and no type.
            setBlock(info, at: next, undoable: false)
            // Before a heading that ends a folded section, the new line
            // would join it, out of sight.
            keepShown(before.id)
            return false
        }
        var after = derived(info)
        if info.type == "toggle_list", !collapsed {
            // In an open toggle, the new line is its first block, as on
            // the desktop.
            after = BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth + 1)
        } else if info.isList, nested {
            // The new item comes first among the items in it.
            after = after.with(depth: info.depth + 1)
        } else if info.type == "heading", collapsed {
            // A new line after a folded heading starts its section, which
            // shows.
            setBlock(info.setting("collapsed", .bool(false)), at: para.location)
        }
        setBlock(after, at: next)
        keepShown(after.id)
        return false
    }

    /// codeEnter breaks a code block's line at range, the new line indented
    /// as the one it leaves, as on the desktop (editor.codeEnter). Enter on
    /// the second blank line at its end leaves the block, without the blank
    /// lines, for a paragraph after it.
    private func codeEnter(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) {
        let ns = storage.string as NSString
        let caret = range.location
        let code = ns.substring(with: para)
        let lines = code.components(separatedBy: String(lineBreak))
        let blank = { (s: String) in s.trimmingCharacters(in: .whitespaces).isEmpty }
        if caret == NSMaxRange(para), range.length == 0, lines.count >= 3,
            blank(lines[lines.count - 1]), blank(lines[lines.count - 2])
        {
            // The two blank lines and the breaks before them go.
            let kept = lines.dropLast(2).joined(separator: String(lineBreak))
            let keptLength = (kept as NSString).length
            delete(NSRange(location: para.location + keptLength, length: para.length - keptLength))
            insertParagraph(
                after: NSRange(location: para.location, length: keptLength),
                info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth),
                text: "")
            return
        }
        // The new line starts as indented as the one the caret is on, as
        // far as the caret.
        let before = ns.substring(
            with: NSRange(location: para.location, length: caret - para.location))
        let line = before.components(separatedBy: String(lineBreak)).last ?? ""
        let indent = String(line.prefix { $0 == " " || $0 == "\t" })
        insert(String(lineBreak) + indent, replacing: range)
    }

    /// fenceEnter turns a paragraph of a Markdown fence, as "```go", into a
    /// code block in that language as Enter goes at its end, as on the
    /// desktop (mobile.FenceEnter); "```mermaid" makes a diagram and "$$"
    /// math, opened to write. It reports whether it did.
    private func fenceEnter(_ para: NSRange, _ info: BlockInfo) -> Bool {
        let line = (storage.string as NSString).substring(with: para)
        guard line.contains("`") || line.contains("$") else { return false }
        struct Fence: Decodable {
            var code: Bool?
            var language: String?
            var block: DocNode?
        }
        var error: NSError?
        let raw = MobileFenceEnter(line, &error)
        guard error == nil, let f = try? JSONDecoder().decode(Fence.self, from: Data(raw.utf8))
        else { return false }
        if f.code == true {
            delete(para)
            setBlock(
                info.with(type: "code", data: ["language": .string(f.language ?? "")]),
                at: para.location)
            // A code block last on the page has a line after it, to leave
            // it by.
            if para.location == storage.length
                || NSMaxRange(paragraph(at: para.location).range) == storage.length
            {
                insertParagraph(
                    after: paragraph(at: para.location).range,
                    info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth),
                    text: "")
                textView.selectedRange = NSRange(location: para.location, length: 0)
            }
            return true
        }
        guard let block = f.block else { return false }
        delete(para)
        insertBlock(block, at: para.location)
        onEditBlock?(block)
        return true
    }

    /// hasNested reports whether blocks are nested in the block of the
    /// paragraph para.
    private func hasNested(_ para: NSRange, _ info: BlockInfo) -> Bool {
        guard NSMaxRange(para) < storage.length else { return false }
        return paragraph(at: NSMaxRange(para) + 1).info.depth > info.depth
    }

    /// splitAfterNested splits a closed toggle at range: the text after
    /// the caret goes in a new toggle after the blocks folded in it, which
    /// stay in it, as on the desktop.
    private func splitAfterNested(_ range: NSRange, _ para: NSRange, _ info: BlockInfo) {
        if range.length > 0 { delete(range) }
        let caret = range.location
        let all = DocumentMapper.paragraphs(storage, trailing: trailing)
        let i = index(all, at: para.location)
        let end = NSMaxRange(all[subtree(all, i)].range)
        let tail = NSRange(location: caret, length: NSMaxRange(paragraph(at: caret).range) - caret)
        let new = derived(info)
        let piece = NSMutableAttributedString(
            string: "\n", attributes: [.leafletBlock: paragraph(at: end).info])
        let text = NSMutableAttributedString(
            attributedString: storage.attributedSubstring(from: tail))
        text.addAttribute(
            .leafletBlock, value: new, range: NSRange(location: 0, length: text.length))
        piece.append(text)
        replace(NSRange(location: end, length: 0), with: piece)
        if tail.length > 0 { replace(tail, with: NSAttributedString()) }
        let start = end - tail.length + 1
        setBlock(new, at: start, undoable: false)
        textView.selectedRange = NSRange(location: start, length: 0)
    }

    /// backspaceAtPageStart follows Backspace with the caret at the start
    /// of the page, which UIKit asks no rule about, and reports whether it
    /// took it.
    func backspaceAtPageStart() -> Bool {
        guard textView.isEditable, textView.markedTextRange == nil,
            textView.selectedRange == NSRange(location: 0, length: 0)
        else { return false }
        let (para, info) = paragraph(at: 0)
        _ = backspaceAtStart(para, info)
        tellKeyboard()
        return true
    }

    /// roomToCompose puts the caret in a new paragraph after a block
    /// without text it is by, as a composition starts there (Chinese,
    /// Japanese or Korean input): text typed by such a block goes in a new
    /// block after it, which the rules can't do for marked text, the
    /// keyboard keeping it where it began.
    func roomToCompose() {
        guard textView.isEditable, textView.markedTextRange == nil else { return }
        let (para, info) = paragraph(at: textView.selectedRange.location)
        guard !info.isText else { return }
        insertParagraph(after: para, info: BlockInfo.paragraph(), text: "")
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

    /// dividerShortcut turns a paragraph of "--" and a third "-" typed,
    /// full-width too, into a divider, as the core's rules have it.
    private func dividerShortcut(
        _ typed: String, _ range: NSRange, _ para: NSRange, _ info: BlockInfo
    ) -> Bool {
        guard typed.count == 1, range.length == 0, range.location == NSMaxRange(para),
            MobileDividerShortcut(
                info.type, (storage.string as NSString).substring(with: para) + typed)
        else { return false }
        delete(para)
        let divider = DocNode(id: BlockInfo.newID(), type: "divider", data: nil, children: nil)
        let (attachment, dInfo) = DocumentMapper.paragraph(divider, depth: info.depth)
        replace(NSRange(location: para.location, length: 0), with: attachment)
        setBlock(dInfo, at: para.location, undoable: false)
        insertParagraph(
            after: NSRange(location: para.location, length: 1),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
        return true
    }

    // MARK: Changing the text

    private func insert(_ text: String, replacing range: NSRange) {
        // Outside the text view's edits, as a command or a tap puts text
        // in, a step of the editor's own, which keeps the blocks as they
        // were to undo and redo (put); the text view's would not.
        let undo = textView.undoManager
        if inEdit == 0, undo?.isUndoing != true, undo?.isRedoing != true {
            replace(
                range, with: NSAttributedString(string: text, attributes: textView.typingAttributes)
            )
            textViewDidChange(textView)
            return
        }
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
        let undo = textView.undoManager
        if inEdit == 0, undo?.isUndoing != true, undo?.isRedoing != true {
            // Outside the text view's edits, the block goes in with the step
            // that puts it in, the separator after the paragraph with it:
            // given its block after, the step would undo and redo by the ID
            // it is given in place of, which the block before may have too.
            let last = end == storage.length
            let piece = NSMutableAttributedString(
                string: "\n", attributes: [.leafletBlock: paragraph(at: para.location).info])
            piece.append(NSAttributedString(string: text, attributes: [.leafletBlock: info]))
            if !last {
                piece.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]))
            }
            replace(NSRange(location: end, length: last ? 0 : 1), with: piece)
            if last {
                trailing = info
                redrawEnd()
            }
            edited(NSRange(location: end, length: piece.length))
            textView.selectedRange = NSRange(
                location: end + 1 + (text as NSString).length, length: 0)
            return
        }
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
        closeMenu()
        updateDateOffer()
    }

    func textViewDidChange(_ textView: UITextView) {
        guard !putting else { return }
        inEdit += 1
        defer { inEdit -= 1 }
        defer { updateHeading() }
        let typed = self.typed
        self.typed = nil
        if let typed { takeTyping(typed.range, typed.attributes) }
        var changed = typed?.range ?? NSRange(location: textView.selectedRange.location, length: 0)
        // Text pasted or dropped, other than typed, may bring block cards.
        let cards =
            typed.map {
                NSMaxRange($0.range) <= storage.length
                    && (storage.string as NSString).substring(with: $0.range).contains("\u{FFFC}")
            } ?? true
        // What an undo or a redo puts back was in place: a card moved then
        // would be a step on the redo's stack.
        let undoing =
            textView.undoManager?.isUndoing == true || textView.undoManager?.isRedoing == true
        if cards, !undoing, let lifted = liftCards() { changed = NSUnionRange(changed, lifted) }
        if let r = typed?.range, r.length == 1, textView.markedTextRange == nil,
            inlineShortcut(NSMaxRange(r))
        {
            changed = NSRange(location: r.location, length: 0)
        }
        defer {
            if let (at, trigger) = opening {
                opening = nil
                openMenu(at: at, trigger: trigger)
            } else {
                updateMenu()
            }
        }
        // All the paragraphs typed or pasted in, each of them a block.
        edited(paragraphs(around: changed))
    }

    /// liftCards gives each block card pasted or dropped into the page a
    /// paragraph and a block of its own, and returns the range from the
    /// first it moved to the end of the page, or nil when none needed it.
    /// UIKit puts a card where the caret is, in a block of text, which
    /// writes its text without it, so it would be saved as nothing; and a
    /// copy of a card the page has, with the same IDs, its cells' too,
    /// which then get IDs of their own.
    private func liftCards() -> NSRange? {
        let all = NSRange(location: 0, length: storage.length)
        var cards: [(range: NSRange, card: BlockAttachment)] = []
        storage.enumerateAttribute(.attachment, in: all) { v, r, _ in
            if let card = v as? BlockAttachment { cards.append((r, card)) }
        }
        let ns = storage.string as NSString
        var kept = Set<String>()
        var lift: [(range: NSRange, card: BlockAttachment)] = []
        for (r, card) in cards {
            let info =
                storage.attribute(.leafletBlock, at: r.location, effectiveRange: nil) as? BlockInfo
            let para = ns.paragraphRange(for: NSRange(location: r.location, length: 0))
            var length = para.length
            if length > 0, ns.character(at: NSMaxRange(para) - 1) == 0x0A { length -= 1 }
            if let info, !info.isText, info.type == card.node.type, info.id == card.node.id,
                length == r.length, !kept.contains(info.id)
            {
                kept.insert(info.id)
            } else {
                lift.append((r, card))
            }
        }
        guard let first = lift.first else { return nil }
        // From the end, so the ranges before stay where they are, each a
        // step to undo before the paste: left out, the paste undone would
        // take out what is no longer where it put it.
        for (r, card) in lift.reversed() {
            let ns = storage.string as NSString
            let para = ns.paragraphRange(for: NSRange(location: r.location, length: 0))
            var end = NSMaxRange(para)
            if end > para.location, ns.character(at: end - 1) == 0x0A { end -= 1 }
            let depth =
                (storage.attribute(.leafletBlock, at: r.location, effectiveRange: nil) as? BlockInfo)?
                .depth ?? 0
            // A card moved keeps its IDs; a copy takes new ones.
            var node = card.node
            if kept.contains(node.id) { node = Self.renewed(node) }
            kept.insert(node.id)
            let (text, info) = DocumentMapper.paragraph(node, depth: depth)
            let piece = NSMutableAttributedString()
            if r.location > para.location {
                piece.append(
                    NSAttributedString(
                        string: "\n",
                        attributes: storage.attributes(at: r.location - 1, effectiveRange: nil)))
            }
            piece.append(text)
            if NSMaxRange(r) < end {
                piece.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: info]))
            }
            replace(r, with: piece)
        }
        return NSRange(
            location: first.range.location, length: storage.length - first.range.location)
    }

    /// renewed is node with new IDs, its children's too, as a copy of it.
    private static func renewed(_ node: DocNode) -> DocNode {
        var n = node
        n.id = BlockInfo.newID()
        n.children = n.children?.map(renewed)
        return n
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
            case "strike": (.leafletStrike, true)
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
        registerUndo(undo, range: after, before: before, trailing: trailing)
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
        // A composition can end with no change of the text, as the caret
        // moves away from it.
        _ = sendComposed()
        updateTypingAttributes()
        if !updating { updateMenu() }
        updateDateOffer()
    }

    // MARK: The menu at the caret

    /// menuTrigger is the menu typed at location opens, "/" or "@", as the
    /// core's rules have it, the desktop's: "/" and "@", in full width too,
    /// at a block's start or after a space or Chinese or Japanese text, and
    /// "、", which a Chinese input method types for "/", at a block's start
    /// or after a space; nil for none.
    private func menuTrigger(_ typed: String, at location: Int, in para: NSRange) -> Character? {
        guard typed.count == 1 else { return nil }
        let ns = storage.string as NSString
        let before =
            location > para.location
            ? ns.substring(with: ns.rangeOfComposedCharacterSequence(at: location - 1)) : ""
        return MobileMenu(typed, before).first
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
        guard anchored(menu) else { return closeMenu() }
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

    /// anchored reports whether the menu's "/" or "@" is still where it
    /// was, before the caret.
    private func anchored(_ menu: CaretMenu) -> Bool {
        let ns = storage.string as NSString
        let caret = textView.selectedRange
        guard caret.length == 0, menu.anchor < ns.length, caret.location > menu.anchor else {
            return false
        }
        let char = ns.substring(with: ns.rangeOfComposedCharacterSequence(at: menu.anchor))
        return MobileMenuKind(char).first == menu.trigger
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
        // A menu whose "/" or "@" went, as a change from outside took it,
        // only closes: it would take the place of other text.
        guard anchored(menu) else { return closeMenu() }
        let caret = textView.selectedRange.location
        closeMenu()
        delete(NSRange(location: menu.anchor, length: max(caret - menu.anchor, 1)))
        textView.selectedRange = NSRange(location: menu.anchor, length: 0)
        defer { tellKeyboard() }
        if menu.trigger == "@" {
            mention(item, at: menu.anchor)
            return
        }
        make(item, at: menu.anchor)
    }

    /// make does what a "/" item says at location, as the "/" menu and the
    /// keyboard bar's Insert do: an empty block becomes the kind picked
    /// and one with text gets a block of it after it; a block without text
    /// goes in after the block there.
    private func make(_ item: MenuItem, at location: Int) {
        var error: NSError?
        let raw = MobileMenuBlock(item.key, author?() ?? "", &error)
        let made = try? JSONDecoder().decode(MenuBlock.self, from: Data(raw.utf8))
        if let type = made?.type {
            let (para, info) = paragraph(at: location)
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
            // The card goes where the "/" was, wherever the caret is once
            // the page is made.
            let block = paragraph(at: location).info.id
            Task { [weak self] in
                guard let id = await self?.createSubpage?(), let self else { return }
                let card = DocNode(
                    id: BlockInfo.newID(), type: "subpage", data: ["page_id": .string(id)],
                    children: nil)
                let at = DocumentMapper.paragraphs(self.storage, trailing: self.trailing)
                    .first { $0.info.id == block }?.range.location
                self.insertBlock(card, at: at ?? self.storage.length)
            }
        } else {
            onMenuItem?(item.key, paragraph(at: location).info.id)
        }
    }

    /// insertMenu gives the keyboard bar's Insert what the "/" menu offers,
    /// its groups apart, once the core has said, to put at the caret as
    /// "/" puts it.
    func insertMenu(_ done: @escaping ([UIMenuElement]) -> Void) {
        guard let menuItems else { return done([]) }
        menuItems("/") { [weak self] all in
            var error: NSError?
            let raw = MobileFilterMenu(all, "", &error)
            let items = (try? JSONDecoder().decode([MenuItem].self, from: Data(raw.utf8))) ?? []
            var groups: [(name: String, actions: [UIMenuElement])] = []
            for item in items {
                let action = UIAction(title: item.label, image: UIImage(systemName: item.symbol)) {
                    [weak self] _ in self?.insertItem(item)
                }
                if groups.last?.name == item.group {
                    groups[groups.count - 1].actions.append(action)
                } else {
                    groups.append((item.group, [action]))
                }
            }
            guard self != nil else { return done([]) }
            done(
                groups.map { UIMenu(title: $0.name, options: .displayInline, children: $0.actions) }
            )
        }
    }

    /// insertItem puts what a "/" item makes at the caret, from the
    /// keyboard bar's Insert.
    func insertItem(_ item: MenuItem) {
        guard textView.isEditable else { return }
        let at = textView.selectedRange.location
        textView.selectedRange = NSRange(location: at, length: 0)
        make(item, at: at)
        tellKeyboard()
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
            replace(NSRange(location: anchor, length: 0), with: mention)
            textView.selectedRange = NSRange(location: anchor + mention.length, length: 0)
            edited(NSRange(location: anchor, length: mention.length))
        default:
            break
        }
    }

    /// insertBlock puts a block without text at the caret, or in the
    /// paragraph at location: in place of an empty block, or after the
    /// block there, with an empty block after it to write on, where the
    /// caret goes. One put at location leaves the caret where it is.
    func insertBlock(_ node: DocNode, at location: Int? = nil) {
        guard textView.isEditable else { return }
        let caret = location == nil ? nil : caretPosition()
        var (para, info) = paragraph(at: location ?? textView.selectedRange.location)
        let (attachment, blockInfo) = DocumentMapper.paragraph(node, depth: info.depth)
        if para.length == 0, info.isText {
            replace(NSRange(location: para.location, length: 0), with: attachment)
        } else {
            let end = NSMaxRange(para)
            let piece = NSMutableAttributedString(string: "\n", attributes: [.leafletBlock: info])
            piece.append(attachment)
            replace(NSRange(location: end, length: 0), with: piece)
            para = NSRange(location: end + 1, length: 0)
        }
        setBlock(blockInfo, at: para.location, undoable: false)
        insertParagraph(
            after: NSRange(location: para.location, length: attachment.length),
            info: BlockInfo(id: BlockInfo.newID(), type: "paragraph", depth: info.depth), text: "")
        if let caret { restoreCaret(caret) }
    }

    /// insertBlocks puts blocks in the page after the block of id, and those
    /// in it, at its depth, or at the page's end once it is gone, as one
    /// step to undo, the caret and the page staying where they are: an
    /// agent's answer, or a recording's transcript, as the desktop's editor
    /// puts them in. Put in by the core, they would be a change from
    /// outside, which no Undo takes back and which clears the steps before.
    func insertBlocks(_ nodes: [DocNode], after id: String) {
        guard textView.isEditable, !nodes.isEmpty else { return }
        let caret = caretPosition()
        let all = DocumentMapper.paragraphs(storage, trailing: trailing)
        var at = storage.length
        var depth = 0
        if let i = all.firstIndex(where: { $0.info.id == id }) {
            at = NSMaxRange(all[subtree(all, i)].range)
            depth = all[i].info.depth
        }
        let page = DocContent(
            document: DocNode(id: "root", type: "page", data: nil, children: nodes), meta: nil)
        let made = NSMutableAttributedString(
            attributedString: DocumentMapper.attributedString(page).text)
        var last: BlockInfo?
        made.enumerateAttribute(.leafletBlock, in: NSRange(location: 0, length: made.length)) {
            value, range, _ in
            guard let info = value as? BlockInfo else { return }
            let shifted = info.with(depth: info.depth + depth)
            made.addAttribute(.leafletBlock, value: shifted, range: range)
            last = shifted
        }
        made.insert(
            NSAttributedString(string: "\n", attributes: [.leafletBlock: paragraph(at: at).info]),
            at: 0)
        let r = NSRange(location: at, length: made.length)
        keepingScroll {
            replace(NSRange(location: at, length: 0), with: made)
            // The separator after them, the block's before, is theirs now.
            if let last, NSMaxRange(r) < storage.length {
                storage.addAttribute(
                    .leafletBlock, value: last, range: NSRange(location: NSMaxRange(r), length: 1))
            }
            edited(paragraphs(around: r))
            restoreCaret(caret)
        }
    }

    // MARK: Commands

    /// selectedParagraphs are the paragraphs the selection touches: one
    /// it ends at the start of, it does not.
    private func selectedParagraphs() -> [(range: NSRange, info: BlockInfo)] {
        let sel = textView.selectedRange
        var out: [(NSRange, BlockInfo)] = []
        var at = sel.location
        repeat {
            let p = paragraph(at: at)
            out.append(p)
            at = NSMaxRange(p.range) + 1
        } while at < NSMaxRange(sel) && at <= storage.length && sel.length > 0
        return out
    }

    /// subtree is the last of the paragraphs from first that its block
    /// holds, nested in it: the block moves with them.
    private func subtree(_ all: [DocumentMapper.Paragraph], _ first: Int) -> Int {
        var last = first
        while last + 1 < all.count, all[last + 1].info.depth > all[first].info.depth { last += 1 }
        return last
    }

    /// index is the paragraph of all that location is in.
    private func index(_ all: [DocumentMapper.Paragraph], at location: Int) -> Int {
        all.lastIndex { $0.range.location <= location } ?? 0
    }

    /// indent nests the blocks chosen in the block before each, or takes
    /// them out a level, with the blocks nested in them.
    func indent(by step: Int) {
        guard textView.isEditable else { return }
        // In code, Tab indents its lines, as on the desktop.
        if paragraph(at: textView.selectedRange.location).info.type == "code" {
            indentCode(by: step)
            return
        }
        let caret = caretPosition()
        let selection = textView.selectedRange
        var all = DocumentMapper.paragraphs(storage, trailing: trailing)
        let chosen = Set(selectedParagraphs().map(\.info.id))
        var changed: ClosedRange<Int>?
        var shown: String?
        var i = 0
        while i < all.count {
            let last = subtree(all, i)
            defer { i = chosen.contains(all[i].info.id) ? last + 1 : i + 1 }
            guard chosen.contains(all[i].info.id) else { continue }
            let info = all[i].info
            var depth = max(info.depth + step, 0)
            if step > 0 {
                // A block nests only in the block before it.
                guard i > 0 else { continue }
                let prev = all[i - 1].info
                depth = min(depth, prev.isList ? prev.depth + 1 : prev.depth)
                // A closed toggle opens to take it, as on the desktop.
                if prev.type == "toggle_list", depth > prev.depth { shown = info.id }
            }
            guard depth != info.depth else { continue }
            if step < 0, chosen.count == 1, !info.isList, info.depth > 0,
                last + 1 < all.count, all[last + 1].info.depth == info.depth
            {
                // Text that is not a list item takes no blocks: the blocks
                // after it stay where they are, and it goes after them.
                var end = last + 1
                while end + 1 < all.count, all[end + 1].info.depth >= info.depth { end += 1 }
                let moved = all[i...last].map { p in
                    (p.info.with(depth: p.info.depth - 1), p.text)
                }
                rewrite(all, i, end, all[(last + 1)...end].map { ($0.info, $0.text) } + moved)
                restoreCaret(caret)
                return
            }
            for k in i...last {
                all[k].info = all[k].info.with(depth: all[k].info.depth + depth - info.depth)
            }
            changed = (changed?.lowerBound ?? i)...last
        }
        guard let changed else { return }
        rewrite(
            all, changed.lowerBound, changed.upperBound, all[changed].map { ($0.info, $0.text) })
        restoreCaret(caret)
        if selection.length > 0 { textView.selectedRange = selection }
        if let shown { keepShown(shown) }
    }

    func setType(_ type: String, _ data: [String: JSONValue] = [:]) {
        guard textView.isEditable else { return }
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
        guard textView.isEditable else { return }
        // The system's underline and strikethrough are the text's own, which
        // the style draws: a link's and a checked to-do's lines are not.
        let key: NSAttributedString.Key =
            switch key {
            case .underlineStyle: .leafletUnderline
            case .strikethroughStyle: .leafletStrike
            default: key
            }
        var sel = textView.selectedRange
        if sel.length == 0 {
            // What is typed next takes it, or goes without it.
            typing[key] = typing[key] == nil ? true : nil
            return
        }
        // A mention is formatted whole.
        sel = widenToMentions(sel)
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
            storage.addAttribute(key, value: true, range: sel)
        }
        registerUndo(undo, range: sel, before: before, trailing: trailing)
        edited(sel)
    }

    /// setInline sets a format with a value (a link, a color) on the
    /// selection, or takes it off with nil.
    func setInline(_ key: NSAttributedString.Key, _ value: Any?) {
        guard textView.isEditable else { return }
        // A mention is formatted whole.
        let sel = widenToMentions(textView.selectedRange)
        guard sel.length > 0 else { return }
        let before = storage.attributedSubstring(from: sel)
        if let value {
            storage.addAttribute(key, value: value, range: sel)
        } else {
            storage.removeAttribute(key, range: sel)
        }
        // A page mention linked elsewhere, or no more, is text no longer
        // standing for its page, as the desktop's SetLink has it: the link
        // given is what it opens.
        if key == .leafletHref {
            storage.enumerateAttribute(.leafletMention, in: sel) { v, r, _ in
                if let m = v as? Mention, m.kind == "page",
                    value as? String != "leaflet://page/\(m.id)"
                {
                    storage.removeAttribute(.leafletMention, range: r)
                }
            }
        }
        registerUndo(textView.undoManager, range: sel, before: before, trailing: trailing)
        edited(sel)
    }

    /// indentCode indents the lines of the code block at the selection, or
    /// takes an indent off them, as the desktop's does (editor.indentCode):
    /// a tab where the code indents with tabs, two spaces otherwise, at the
    /// caret for Tab with no selection.
    private func indentCode(by step: Int) {
        let sel = textView.selectedRange
        let (para, info) = paragraph(at: sel.location)
        let ns = storage.string as NSString
        let code = ns.substring(with: para)
        let br = String(lineBreak)
        let unit = (br + code).contains(br + "\t") ? "\t" : "  "
        if step > 0, sel.length == 0 {
            insert(unit, replacing: sel)
            return
        }
        // The lines from the selection's first to its last, leaving out a
        // line the selection only reaches the start of.
        var lines = code.components(separatedBy: br)
        var at = para.location
        var first = sel.location
        var last = NSMaxRange(sel)
        for i in lines.indices {
            let length = (lines[i] as NSString).length
            let starts = at
            let ends = at + length
            defer { at = ends + 1 }
            let touched =
                sel.length == 0
                ? starts <= sel.location && sel.location <= ends
                : starts < NSMaxRange(sel) && ends >= sel.location
            guard touched else { continue }
            if step > 0 {
                lines[i] = unit + lines[i]
                if starts <= sel.location { first += (unit as NSString).length }
                last += (unit as NSString).length
            } else {
                let lead = lines[i].prefix { $0 == " " || $0 == "\t" }
                let cut = lead.hasPrefix("\t") ? 1 : min(lead.count, 2)
                guard cut > 0 else { continue }
                lines[i].removeFirst(cut)
                if starts < sel.location { first -= min(cut, sel.location - starts) }
                last -= cut
            }
        }
        let made = lines.joined(separator: br)
        guard made != code else { return }
        var attributes = storage.attributes(at: para.location, effectiveRange: nil)
        attributes[.leafletBlock] = info
        replace(para, with: NSAttributedString(string: made, attributes: attributes))
        let r = NSRange(location: para.location, length: (made as NSString).length)
        edited(r)
        textView.selectedRange = NSRange(
            location: max(para.location, min(first, NSMaxRange(r))),
            length: max(0, min(last, NSMaxRange(r)) - max(para.location, min(first, NSMaxRange(r))))
        )
    }

    /// softBreak breaks the line within the block, as Shift-Return does on
    /// the desktop: the block stays one.
    func softBreak() {
        guard textView.isEditable else { return }
        insert(String(lineBreak), replacing: textView.selectedRange)
    }

    /// replace puts text in place of range, as the rules and menus change
    /// the text, as a step to undo: the text view's own steps, as typing,
    /// were made over the text as it was, which undoing this gives back.
    private func replace(_ range: NSRange, with text: NSAttributedString) {
        let before = storage.attributedSubstring(from: range)
        let was = trailing
        put(text, in: range)
        registerUndo(
            textView.undoManager, range: NSRange(location: range.location, length: text.length),
            before: before, trailing: was)
        tellKeyboard()
    }

    /// put puts text in place of range. Within the text view's own edit,
    /// as a rule takes a key, or as a step is undone or redone, it changes
    /// the text itself. Otherwise, as a command or a sheet changes it, its
    /// characters go in through the text view, which drops every step to
    /// undo, the typing before too, when its text changes under it, then
    /// their blocks and formats: the step that undoes it is the editor's
    /// (replace), with the blocks as they were, so the text view registers
    /// none of its own.
    private func put(_ text: NSAttributedString, in range: NSRange) {
        let undo = textView.undoManager
        guard inEdit == 0, undo?.isUndoing != true, undo?.isRedoing != true,
            let r = textRange(range)
        else { return storage.replaceCharacters(in: range, with: text) }
        // The selection where the text changing would leave it.
        var sel = textView.selectedRange
        let moved = text.length - range.length
        if sel.location >= NSMaxRange(range) {
            sel.location += moved
        } else if NSMaxRange(sel) > range.location {
            sel = NSRange(location: range.location + text.length, length: 0)
        }
        let emptied = self.emptied
        putting = true
        bypass = true
        undo?.disableUndoRegistration()
        textView.replace(r, withText: text.string)
        undo?.enableUndoRegistration()
        bypass = false
        putting = false
        self.emptied = emptied
        storage.beginEditing()
        text.enumerateAttributes(in: NSRange(location: 0, length: text.length)) {
            attributes, run, _ in
            storage.setAttributes(
                attributes,
                range: NSRange(location: range.location + run.location, length: run.length))
        }
        storage.endEditing()
        if NSMaxRange(sel) <= storage.length { textView.selectedRange = sel }
    }

    /// registerUndo puts before back in place of range, which may be of
    /// another length, and the trailing block as it was, and the text there
    /// now back on redo.
    private func registerUndo(
        _ undo: UndoManager?, range: NSRange, before: NSAttributedString, trailing was: BlockInfo?
    ) {
        undo?.registerUndo(withTarget: self) { c in
            guard NSMaxRange(range) <= c.storage.length else { return }
            let now = c.storage.attributedSubstring(from: range)
            let trailingNow = c.trailing
            c.storage.replaceCharacters(in: range, with: before)
            c.trailing = was
            let back = NSRange(location: range.location, length: before.length)
            c.registerUndo(undo, range: back, before: now, trailing: trailingNow)
            if back.length != range.length {
                c.textView.selectedRange = NSRange(location: NSMaxRange(back), length: 0)
            }
            c.tellKeyboard()
            c.redrawEnd()
            c.edited(back)
        }
    }

    /// rewrite puts blocks, each a paragraph, in place of the paragraphs
    /// first through last of all, as one step to undo.
    private func rewrite(
        _ all: [DocumentMapper.Paragraph], _ first: Int, _ last: Int,
        _ blocks: [(info: BlockInfo, text: NSAttributedString)]
    ) {
        let start = all[first].range.location
        let toEnd = last == all.count - 1
        let end = toEnd ? storage.length : all[last + 1].range.location
        let out = NSMutableAttributedString()
        for (i, b) in blocks.enumerated() {
            let text = NSMutableAttributedString(attributedString: b.text)
            text.addAttribute(
                .leafletBlock, value: b.info, range: NSRange(location: 0, length: text.length))
            out.append(text)
            // Each paragraph's separator is its block's; the page's last
            // paragraph has none.
            if i < blocks.count - 1 || !toEnd {
                out.append(NSAttributedString(string: "\n", attributes: [.leafletBlock: b.info]))
            }
        }
        replace(NSRange(location: start, length: end - start), with: out)
        if toEnd, let b = blocks.last { trailing = b.info }
        redrawEnd()
        edited(NSRange(location: start, length: out.length))
    }

    /// moveBlock moves the block at the caret, with the blocks nested in
    /// it, past the block before or after it at its depth. The first block
    /// in a list item goes before it, and the last after it, at its depth.
    func moveBlock(by step: Int) {
        guard textView.isEditable else { return }
        let caret = caretPosition()
        let all = DocumentMapper.paragraphs(storage, trailing: trailing)
        let i = index(all, at: textView.selectedRange.location)
        let last = subtree(all, i)
        let depth = all[i].info.depth
        func block(_ p: DocumentMapper.Paragraph, by shift: Int = 0) -> (
            BlockInfo, NSAttributedString
        ) {
            (shift == 0 ? p.info : p.info.with(depth: p.info.depth + shift), p.text)
        }
        if step < 0 {
            var k = i - 1
            while k >= 0, all[k].info.depth > depth { k -= 1 }
            guard k >= 0 else { return }
            // Past the block before, or out before the one it is in.
            let shift = all[k].info.depth - depth
            rewrite(
                all, k, last,
                all[i...last].map { block($0, by: shift) } + all[k..<i].map { block($0) })
        } else {
            guard last + 1 < all.count else { return }
            let next = all[last + 1].info.depth
            if next == depth {
                let end = subtree(all, last + 1)
                rewrite(
                    all, i, end,
                    all[(last + 1)...end].map { block($0) } + all[i...last].map { block($0) })
            } else {
                // The last block in another goes out after it, where it
                // is, at its depth.
                rewrite(all, i, last, all[i...last].map { block($0, by: -1) })
            }
        }
        restoreCaret(caret)
        if let caret { keepShown(caret.id) }
    }

    func editLink() {
        guard textView.isEditable else { return }
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

    /// markerHit finds the block whose marker is at point: its checkbox,
    /// its fold arrow or its callout's icon, its own paragraph's or the
    /// empty block's after the last. It reaches around the marker, but not
    /// into the text, whose taps place the caret.
    private func markerHit(_ point: CGPoint) -> (BlockInfo, Int)? {
        guard let tlm = textView.textLayoutManager else { return nil }
        // A page that can't be changed still folds, as the desktop's does.
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
        let (_, info) = paragraph(at: location)
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

    /// linkHit finds where the link at point goes, when a tap there opens
    /// it, as a click does on the desktop: a page's at once, and the web's
    /// with ⌘, or while the page isn't being written in, for a tap to put
    /// the caret in it otherwise. On a page that can't be changed the text
    /// view opens links itself.
    private func linkHit(_ point: CGPoint, command: Bool) -> URL? {
        guard textView.isEditable, let position = textView.closestPosition(to: point) else {
            return nil
        }
        let offset = textView.offset(from: textView.beginningOfDocument, to: position)
        for at in [offset, offset - 1] where at >= 0 && at < storage.length {
            guard let range = textRange(NSRange(location: at, length: 1)),
                textView.firstRect(for: range).insetBy(dx: -2, dy: -2).contains(point),
                let url = EditorStyle.linkTarget(storage.attributes(at: at, effectiveRange: nil))
            else { continue }
            let page = url.scheme == "leaflet"
            return page || command || !textView.isFirstResponder ? url : nil
        }
        return nil
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

    func textView(
        _ textView: UITextView, primaryActionFor textItem: UITextItem,
        defaultAction: UIAction
    ) -> UIAction? {
        guard case .link(let url) = textItem.content else { return defaultAction }
        return UIAction { [weak self] _ in self?.open(url) }
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
            // Its language is chosen only on a page that can be changed.
            if textView.isEditable, parts.language.insetBy(dx: -6, dy: -4).contains(p) {
                return (info, location, false)
            }
        }
        return nil
    }

    func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        let at = g.location(in: textView)
        if g is UILongPressGestureRecognizer { return inSelection(at) }
        return markerHit(at) != nil || codeBarHit(at) != nil || dueChipHit(at) != nil
            || linkHit(at, command: g.modifierFlags.contains(.command)) != nil
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
        if let (info, location) = markerHit(at) {
            return keepingScroll { tapMarker(info, at: location) }
        }
        if let url = linkHit(at, command: g.modifierFlags.contains(.command)) {
            return open(url)
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
        } else if !textView.isEditable {
            foldHere(
                info.setting("collapsed", .bool(!(info.data["collapsed"]?.bool ?? false))),
                at: location)
        } else {
            // Folding is how the page shows, not an edit to undo, as on the
            // desktop, though it is kept.
            setBlock(
                info.setting("collapsed", .bool(!(info.data["collapsed"]?.bool ?? false))),
                at: location, undoable: false)
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

    /// foldHere folds or unfolds a toggle or heading of a page that can't
    /// be changed, as info says: here alone, as it saves nothing, for a
    /// section its owner folded to show.
    private func foldHere(_ info: BlockInfo, at location: Int) {
        let (range, _) = paragraph(at: location)
        var whole = range
        if NSMaxRange(range) < storage.length { whole.length += 1 }
        if whole.length > 0 {
            storage.addAttribute(.leafletBlock, value: info, range: whole)
            style.apply(to: storage, in: whole)
        }
        if NSMaxRange(range) == storage.length {
            trailing = info
            redrawEnd()
        }
        updateFolds()
    }

    // MARK: VoiceOver

    /// accessibilityActions are what VoiceOver offers at the caret: what a
    /// tap does on the block's marker, its due date's chip, its view or a
    /// link, which VoiceOver can't reach in the text, and the choices of
    /// the menu at the caret while it shows.
    func accessibilityActions() -> [UIAccessibilityCustomAction] {
        var out: [UIAccessibilityCustomAction] = []
        func offer(_ name: String, _ run: @escaping @MainActor (EditorController) -> Void) {
            out.append(
                UIAccessibilityCustomAction(name: name) { [weak self] _ in
                    guard let self else { return false }
                    run(self)
                    return true
                })
        }
        if let menu {
            for item in menu.items { offer(item.label) { $0.pick(item) } }
            return out
        }
        let caret = textView.selectedRange.location
        let (para, info) = paragraph(at: caret)
        let at = para.location
        let editable = textView.isEditable
        let collapsed = info.data["collapsed"]?.bool == true
        switch info.type {
        case "todo_list" where editable:
            offer(info.checked ? LI.markNotDone : LI.markDone) { $0.tapMarker(info, at: at) }
            offer(Due(info)?.label() ?? L.editorAddDate) { c in
                let rect = c.textView.selectedTextRange.map { c.textView.caretRect(for: $0.end) }
                c.openDue(info, from: c.dueChipRect(info) ?? rect ?? .zero)
            }
        case "toggle_list":
            offer(collapsed ? LI.expand : LI.collapse) { $0.tapMarker(info, at: at) }
        case "heading":
            offer(collapsed ? L.editorUnfoldSection : L.editorFoldSection) {
                $0.tapMarker(info, at: at)
            }
        case "code":
            // What the code's bar offers, Copy and its language.
            offer(L.editorCopyCode) { $0.tapCodeBar(info, at: at, copy: true, from: .zero) }
            if editable {
                let name = CodeLanguage.name(info.data["language"]?.string ?? "")
                offer(L.editorCodeLanguage(name: name)) { c in
                    let caret = c.textView.selectedTextRange.map {
                        c.textView.caretRect(for: $0.end)
                    }
                    c.tapCodeBar(info, at: at, copy: false, from: caret?.origin ?? .zero)
                }
            }
        default:
            break
        }
        if !info.isText, para.length > 0,
            let node =
                (storage.attribute(.attachment, at: at, effectiveRange: nil)
                as? BlockAttachment)?.node
        {
            if editable, onEditBlock != nil,
                BlockViews.editable.contains(node.type) || node.type == "table"
            {
                offer(node.type == "chart" ? L.editorEditData : L.commonEdit) {
                    $0.onEditBlock?(node)
                }
            }
            if node.type == "subpage", let id = node.data?["page_id"]?.string {
                offer(L.commonOpen) { $0.pageLinks?.open(id) }
            }
            if node.type == "video", !VideoPlayback.shared.playing.contains(node.id) {
                offer(L.recPlay) { _ in VideoPlayback.shared.playing.insert(node.id) }
            }
            if node.type == "recording", let id = node.data?["recording_id"]?.string {
                for action in RecordingBlock.actions(id, canEdit: editable, editor: self) {
                    offer(action.title) { _ in action.run() }
                }
            }
        }
        // The link the caret is in, or just after.
        for i in [caret, caret - 1] where i >= 0 && i < storage.length {
            if let url = EditorStyle.linkTarget(storage.attributes(at: i, effectiveRange: nil)) {
                offer(LI.openLink) { $0.open(url) }
                break
            }
        }
        return out
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
        for p in DocumentMapper.blocks(storage, trailing: trailing)
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

    /// refreshDue draws the to-dos' due dates again, as the day changes, a
    /// time one is due at passes, or the app comes back: what a chip says,
    /// Today or Tomorrow, its room and whether it is overdue are as of when
    /// it was drawn.
    @objc private func refreshDue() {
        guard let tlm = textView.textLayoutManager else { return }
        let ns = storage.string as NSString
        for p in DocumentMapper.blocks(storage, trailing: trailing) where Due(p.info) != nil {
            guard p.range.length > 0 else {
                redrawEnd()
                continue
            }
            let para = ns.paragraphRange(for: p.range)
            style.apply(to: storage, in: para)
            guard let start = tlm.location(tlm.documentRange.location, offsetBy: para.location),
                let end = tlm.location(start, offsetBy: para.length),
                let range = NSTextRange(location: start, end: end)
            else { continue }
            tlm.invalidateLayout(for: range)
        }
        textView.setNeedsLayout()
        scheduleDue()
    }

    /// dueAt is the refresh of the due dates at the next time a to-do is
    /// due at, which turns it overdue.
    private var dueAt: Task<Void, Never>?

    /// scheduleDue has the due dates drawn again at the next time a to-do
    /// of the page is due at.
    private func scheduleDue() {
        dueAt?.cancel()
        dueAt = nil
        let now = Date.now
        let next = DocumentMapper.blocks(storage, trailing: trailing)
            .compactMap { Due($0.info) }.filter { $0.timed && $0.date > now }.map(\.date).min()
        guard let next else { return }
        dueAt = Task { [weak self] in
            try? await Task.sleep(for: .seconds(next.timeIntervalSinceNow + 1))
            guard !Task.isCancelled else { return }
            self?.refreshDue()
        }
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
        let popover = DuePopover(Due(info)) { [weak self] due, changed in
            guard let self else { return }
            dueOpen = nil
            if changed,
                let p = DocumentMapper.blocks(storage, trailing: trailing)
                    .first(where: { $0.info.id == id }), p.info.type == "todo_list"
            {
                keepingScroll {
                    self.setBlock(p.info.setting("due", due?.value), at: p.range.location)
                }
                scheduleDue()
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
        if let range = textElement.elementRange,
            textLayoutManager.offset(
                from: range.endLocation, to: textLayoutManager.documentRange.endLocation)
                == 0
        {
            f.trailing = trailingLook()
        }
        return f
    }

    // MARK: Menus

    func textView(
        _ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]
    ) -> UIMenu? {
        guard textView.isEditable, range.length > 0 else {
            // Text chosen on a page that can't change is copied.
            if range.length > 0 { return UIMenu(children: withCopyPlainText(suggestedActions)) }
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
        return UIMenu(
            children: [agentMenu(), formatMenu(), colorMenu()] + withCopyPlainText(suggestedActions)
        )
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
                    [weak self] _ in self?.toggleInline(.leafletUnderline)
                },
                UIAction(title: LI.strikethrough, image: UIImage(systemName: "strikethrough")) {
                    [weak self] _ in self?.toggleInline(.leafletStrike)
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

    /// swatches are the desktop's text colors, as hex, by name, in the
    /// language the app is in now.
    static var swatches: [(String, String)] {
        [
            (L.editorColorRed, "#ef4444"), (L.editorColorOrange, "#f97316"),
            (L.editorColorYellow, "#eab308"),
            (L.editorColorGreen, "#22c55e"), (L.editorColorTeal, "#14b8a6"),
            (L.editorColorBlue, "#3b82f6"),
            (L.editorColorIndigo, "#6366f1"), (L.editorColorPurple, "#a855f7"),
            (L.editorColorPink, "#ec4899"),
            (L.editorColorBrown, "#92400e"), (L.editorColorGray, "#71717a"),
        ]
    }

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

    /// blockMenu turns the block at the caret into another kind, as the
    /// menu bar's Turn Into does.
    private func blockMenu() -> UIMenu {
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
            ])
    }

    // MARK: Keyboard bar

    /// installKeyboardBar puts the editor's commands over the keyboard on
    /// iPhone, and in the shortcuts bar on iPad.
    private func installKeyboardBar() {
        // Insert offers what "/" does, and puts it in as "/" does: Turn
        // Into is the Format menu's.
        let insert = Self.menuItem(
            LI.insert, "plus.circle",
            UIMenu(children: [
                UIDeferredMenuElement.uncached { [weak self] done in
                    guard let self else { return done([]) }
                    self.insertMenu(done)
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
                    done([self.formatMenu(), self.blockMenu(), self.colorMenu()])
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
