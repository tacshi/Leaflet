import LeafletCore
import UIKit

@MainActor
final class EditorController: NSObject, UITextViewDelegate {
    /// Names this editor's players. One thing sounds at a time in the whole
    /// app, but a video or a file playing belongs to the window it was
    /// started in: the same page open in another window has players of its
    /// own, and closing either leaves the other's as they are.
    let players = UUID().uuidString

    func player(_ block: String) -> String { players + "/" + block }

    let textView: EditorTextView
    let text: BlockText
    /// The selection a formula shows its source at, which a new style from
    /// the page's view takes on.
    var mathOpen: NSRange?

    var style: EditorStyle {
        didSet {
            if style.mathOpen != mathOpen { style.mathOpen = mathOpen }
            BlockLook.palette = style.palette
            BlockLook.textSize = style.textSize
            let restyled = oldValue.palette.key != style.palette.key
            let resized = oldValue.textSize != style.textSize
            if restyled || resized {
                restyleAll()
                textView.relayout()
            }
            if resized { textView.blocks.reset { $0.type == "table" } }
            if restyled { refreshBlocks() }
        }
    }
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

    var onEdit: ((DocContent) -> Void)?
    var onComment: ((_ blockID: String, _ quote: String) -> Void)?
    var onAsk: ((_ focus: [String], _ selection: String, _ agent: AgentState.Option?) -> Void)?
    var onAddAgent: (() -> Void)?
    var onHeading: ((String?) -> Void)?
    var onEditing: ((Bool) -> Void)?
    var onBlank: ((Bool) -> Void)? {
        didSet {
            wasBlank = nil
            reportBlank()
        }
    }
    private var wasBlank: Bool?
    var onUndoRun: (@MainActor () -> Void)?
    var onEditBlock: (@MainActor (DocNode) -> Void)? {
        didSet { textView.blocks.edit = onEditBlock }
    }
    var onRenameBlock: (@MainActor (DocNode, String) -> Void)?
    private var followAt: CGFloat?
    var onShare: (@MainActor (SharedFile) -> Void)?
    var onOpenFile: (@MainActor (String) -> Void)?
    var onEditSpeaker: (@MainActor (DocNode, String?, Int) -> Void)?
    var onReplaceNotes: (@MainActor (DocNode, String) -> Void)?
    var client: CoreClient?
    var onEditNotes: (@MainActor (DocNode) -> Void)?
    var onEditLink: (@MainActor (DocNode) -> Void)?
    var onSummarize: (@MainActor (DocNode, String) -> Void)?
    var pageID = ""
    var pageLinks: PageLinks? {
        didSet { textView.blocks.links = pageLinks }
    }
    var createSubpage: (() async -> String?)?
    var plainCopy: (([DocNode]) async -> String?)?
    var copyText: (([DocNode]) async -> String?)?
    var menuItems: ((Character, @escaping @MainActor (String) -> Void) -> Void)?
    var agents: (() -> [AgentState.Option])?
    var author: (() -> String)?
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
        NotificationCenter.default.addObserver(
            self, selector: #selector(refreshDue),
            name: UIApplication.significantTimeChangeNotification, object: nil)
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

    private func refreshBlocks() {
        guard UIApplication.shared.applicationState != .background,
            blocksPalette != style.palette.key
        else { return }
        blocksPalette = style.palette.key
        textView.blocks.reset()
        DispatchQueue.main.async { [weak self] in self?.textView.replaceBlocks() }
    }

    @objc private func willEnterForeground() {
        refreshBlocks()
        due.refresh()
    }

    @objc private func refreshDue() { due.refresh() }

    func load(_ content: DocContent, tinted: Set<String>) {
        session.load(content, tinted: tinted)
    }

    @discardableResult
    func update(
        _ content: DocContent, tinted: Set<String>, byAgent: Bool = false, notesOf: String? = nil
    )
        -> Bool
    {
        session.update(content, tinted: tinted, byAgent: byAgent, notesOf: notesOf)
    }

    func tint(_ ids: Set<String>) { session.tint(ids) }

    func send() { session.send() }

    var hasPendingEdit: Bool { session.hasPendingEdit }

    var composing: Bool { session.composing }

    func composed() { session.composed() }

    func close() {
        caretMenu.close()
        due.stop()
        VideoPlayback.shared.stop(
            Set(text.blocks().filter { $0.info.type == "video" }.map { player($0.info.id) }))
        FilePlayback.shared.stop(
            Set(text.blocks().filter { $0.info.type == "file" }.map { $0.info.id }),
            in: players)
        textView.blocks.reset()
    }

    func stopPlaying(_ gone: [BlockInfo]) {
        let ids = Set(gone.map(\.id))
        VideoPlayback.shared.stop(Set(ids.map(player)))
        FilePlayback.shared.stop(ids, in: players)
        let recordings = gone.filter { $0.type == "recording" }
            .compactMap { $0.data["recording_id"]?.string }
        let recorder = Recorder.shared
        if let playing = recorder.playing ?? recorder.loading, recordings.contains(playing) {
            recorder.stopPlaying()
        }
    }

    var emptyPageBlock: (BlockInfo, UIFont, Palette)? {
        guard storage.length == 0, let trailing = text.trailing, trailing.type != "paragraph",
            trailing.type != "heading"
        else { return nil }
        return (trailing, style.baseFont(trailing), style.palette)
    }

    var isBlank: Bool {
        storage.length == 0 && (text.trailing?.type ?? "paragraph") == "paragraph"
    }

    func reportBlank() {
        let blank = isBlank
        guard blank != wasBlank else { return }
        wasBlank = blank
        DispatchQueue.main.async { [weak self] in self?.onBlank?(blank) }
    }

    func edited(_ range: NSRange, fit: Bool = true) {
        var changed = NSUnionRange(range, text.normalize(around: range))
        if text.keepTrailing() { layout.redrawEnd() }
        if fit {
            let fitted = text.fitDepths(from: changed)
            for r in fitted.changed { changed = NSUnionRange(changed, r) }
            if fitted.trailing { layout.redrawEnd() }
            edits.registerFitUndo(textView.undoManager, fitted.fitted)
        }
        if text.keepTrailing() { layout.redrawEnd() }
        style.apply(to: storage, in: changed)
        restyleNumbersAfter(changed)
        format.updateTypingAttributes()
        layout.updateFolds()
        reportBlank()
        session.edited()
    }

    func paragraphs(around range: NSRange) -> NSRange {
        let (caret, _) = text.paragraph(at: textView.selectedRange.location)
        let r = NSUnionRange(range, NSRange(location: caret.location, length: max(caret.length, 1)))
        return NSIntersectionRange(r, NSRange(location: 0, length: storage.length))
    }

    func restyleAll() {
        style.apply(to: storage, in: NSRange(location: 0, length: storage.length))
    }

    func restyleNumbersAfter(_ range: NSRange) {
        let ns = storage.string as NSString
        guard ns.length > 0 else { return }
        let start = min(range.location, ns.length)
        let last = range.length > 0 ? min(NSMaxRange(range), ns.length) - 1 : start
        var at = NSMaxRange(ns.paragraphRange(for: NSRange(location: last, length: 0)))
        guard at < ns.length else { return renumberEnd(open: true) }
        var known: [Int: Int] = [:]
        storage.enumerateAttribute(
            .leafletOrdinal, in: NSRange(location: start, length: at - start)
        ) { v, r, _ in
            if let n = v as? Int {
                known[ns.paragraphRange(for: NSRange(location: r.location, length: 0)).location] = n
            }
        }
        var open = Int.max
        storage.beginEditing()
        while at < ns.length, open > 0 {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            guard let info = text.blockAt(para.location) else { break }
            let was =
                storage.attribute(.leafletOrdinal, at: para.location, effectiveRange: nil) as? Int
            if info.depth < open {
                open = info.depth
                if info.type == "numbered_list" {
                    let n = EditorStyle.ordinal(storage, at: para.location, info, known: known)
                    known[para.location] = n
                    if n != was {
                        storage.addAttribute(.leafletOrdinal, value: n, range: para)
                        open = .max
                    }
                }
            } else if info.type == "numbered_list", let was {
                known[para.location] = was
            }
            at = NSMaxRange(para)
        }
        storage.endEditing()
        if at >= ns.length { renumberEnd(open: open > 0) }
    }

    private func renumberEnd(open: Bool) {
        let ns = storage.string as NSString
        if open, text.trailing?.type == "numbered_list", ns.length > 0,
            ns.character(at: ns.length - 1) == 0x0A
        {
            layout.redrawEnd()
        }
    }

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
        let cards =
            typed.map {
                NSMaxRange($0.range) <= storage.length
                    && (storage.string as NSString).substring(with: $0.range).contains("\u{FFFC}")
            } ?? true
        if cards, !edits.undoing, let lifted = clipboard.liftCards() {
            changed = NSUnionRange(changed, lifted)
        }
        if let r = typed?.range, r.length == 1, textView.markedTextRange == nil,
            rules.inlineShortcut(NSMaxRange(r))
        {
            changed = NSRange(location: r.location, length: 0)
        }
        defer { caretMenu.openOrUpdate() }
        edited(paragraphs(around: changed))
    }

    func textViewDidChangeSelection(_ textView: UITextView) {
        session.composed()
        format.updateTypingAttributes()
        if !session.updating { caretMenu.update() }
        due.updateDateOffer()
        mathFollowsSelection()
    }

    func textViewDidBeginEditing(_ textView: UITextView) {
        onEditing?(true)
        due.updateDateOffer()
        mathFollowsSelection()
    }

    func textViewDidEndEditing(_ textView: UITextView) {
        onEditing?(false)
        caretMenu.close()
        due.updateDateOffer()
        mathFollowsSelection()
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

    func paragraph(at location: Int) -> (range: NSRange, info: BlockInfo) {
        text.paragraph(at: location)
    }

    func setBlock(_ info: BlockInfo, at location: Int, undoable: Bool = true) {
        edits.setBlock(info, at: location, undoable: undoable)
    }

    func selectedText() -> String {
        (storage.string as NSString).substring(with: textView.selectedRange)
            .replacingOccurrences(of: String(lineBreak), with: "\n")
            .replacingOccurrences(of: "\u{FFFC}", with: "")
    }

    func focusBlockIDs() -> [String] {
        var ids: [String] = []
        for (_, info) in text.paragraphs(touching: textView.selectedRange)
        where !ids.contains(info.id) {
            ids.append(info.id)
        }
        return ids
    }

    func reveal(blockID: String, atTop: Bool = false) {
        headings.reveal(blockID: blockID, atTop: atTop)
    }

    func focusStart() {
        _ = textView.becomeFirstResponder()
        textView.selectedRange = NSRange(location: 0, length: 0)
        DispatchQueue.main.async { [weak self] in
            guard let self, !self.textView.isFirstResponder else { return }
            _ = self.textView.becomeFirstResponder()
            self.textView.selectedRange = NSRange(location: 0, length: 0)
        }
    }

    func open(_ url: URL) {
        if url.scheme == "leaflet" {
            let id = url.pathComponents.dropFirst().first ?? ""
            guard !id.isEmpty else { return }
            let href = url.absoluteString
            let block = MobileLinkedBlock(href)
            if !block.isEmpty, let openAt = pageLinks?.openAt {
                openAt(id, block, MobileLinkedMoment(href))
            } else {
                pageLinks?.open(id)
            }
            return
        }
        UIApplication.shared.open(url)
    }

    func play(blockID: String, at: Int) {
        guard at >= 0, let info = text.blocks().first(where: { $0.info.id == blockID })?.info
        else { return }
        switch info.type {
        case "video":
            VideoPlayback.shared.seek(
                player(blockID), link: info.data["url"]?.string ?? "", at: at)
        case "recording":
            if let id = info.data["recording_id"]?.string {
                Recorder.shared.play(id, from: TimeInterval(at))
            }
        case "file":
            if let file = info.data["file_id"]?.string {
                FilePlayback.shared.play(
                    block: blockID, in: players, file: file, at: at, client: client)
            }
        default:
            break
        }
    }

    func showsHead(ofBlock id: String) -> Bool {
        guard let frame = textView.blocks.frame(of: id) else { return false }
        let top = textView.contentOffset.y + textView.adjustedContentInset.top
        let bottom =
            textView.contentOffset.y + textView.bounds.height
            - textView.adjustedContentInset.bottom
        return frame.minY + 110 > top && frame.minY < bottom
    }

    func follow(block id: String, y: CGFloat) {
        guard UserDefaults.standard.bool(forKey: LineClock.followKey),
            let frame = textView.blocks.placed(id), !textView.isDragging, !textView.isDecelerating
        else { return }
        if let followAt, abs(textView.contentOffset.y - followAt) > 2 {
            self.followAt = nil
            return
        }
        let top = -textView.adjustedContentInset.top
        let bottom = max(
            textView.contentSize.height + textView.adjustedContentInset.bottom
                - textView.bounds.height, top)
        let target = min(max(frame.minY + y - textView.bounds.height / 3, top), bottom)
        followAt = target
        textView.setContentOffset(
            CGPoint(x: textView.contentOffset.x, y: target), animated: true)
    }

    func recordingBlock(_ id: String) -> DocNode? {
        for (info, _) in text.blocks()
        where info.type == "recording" && info.data["recording_id"]?.string == id {
            return node(ofBlock: info.id)
        }
        return nil
    }

    func setData(_ data: [String: JSONValue], ofBlock id: String) {
        replaceBlock(DocNode(id: id, type: "", data: data, children: nil))
    }

    func replaceBlock(_ node: DocNode) { commands.replaceBlock(node) }

    func removeBlock(_ id: String) { commands.removeBlock(id) }

    func node(ofBlock id: String) -> DocNode? {
        guard let (info, range) = text.blocks().first(where: { $0.info.id == id }) else {
            return nil
        }
        return DocumentMapper.node(for: info, text: storage.attributedSubstring(from: range))
    }

    func copyBlock(_ id: String) {
        guard let node = node(ofBlock: id) else { return }
        clipboard.copy(blocks: [node])
    }

    func cutBlock(_ id: String) {
        copyBlock(id)
        removeBlock(id)
    }

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

    var menu: CaretMenu? { caretMenu.menu }

    func closeMenu() { caretMenu.close() }

    func moveInMenu(_ step: Int) { caretMenu.move(step) }

    func insertMenu(_ done: @escaping ([UIMenuElement]) -> Void) { caretMenu.insertMenu(done) }

    func insertItem(_ item: MenuItem) { caretMenu.insertItem(item) }

    func accessibilityActions() -> [UIAccessibilityCustomAction] {
        EditorAccessibility.actions(self)
    }
}
