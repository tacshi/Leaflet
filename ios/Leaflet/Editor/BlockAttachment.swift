import SwiftUI
import UIKit

/// BlockAttachment is a block without text in the editor (an image, a
/// table, a chart), kept whole as one character of its paragraph. It only
/// keeps the block's room in the text and draws nothing: BlockViews puts
/// the block's view there, as TextKit's own attachment views show only
/// once laid out a second time.
final class BlockAttachment: NSTextAttachment, @unchecked Sendable {
    let node: DocNode

    init(node: DocNode) {
        self.node = node
        super.init(data: nil, ofType: nil)
        allowsTextAttachmentView = false
    }

    /// accessibilityLabel is what VoiceOver reads for the block, as it
    /// reads the text: an image's description, or what the block is and
    /// its title, as the block has them.
    override var accessibilityLabel: String? {
        get {
            let d = node.data ?? [:]
            let alt = d["alt"]?.string ?? ""
            let title = d["title"]?.string ?? ""
            let kind = CardBlock.name(node.type)
            return node.type == "image" && !alt.isEmpty
                ? alt : title.isEmpty ? kind : kind + ", " + title
        }
        set {}
    }

    // UIKit archives the text it copies or drags, its attachments with
    // it, and reads it back as it is pasted or dropped: the block goes
    // with them, as JSON.
    private static let nodeKey = "leaflet.node"

    override static var supportsSecureCoding: Bool { true }

    override func encode(with coder: NSCoder) {
        super.encode(with: coder)
        if let json = try? JSONEncoder().encode(node) { coder.encode(json, forKey: Self.nodeKey) }
    }

    required init?(coder: NSCoder) {
        guard let json = coder.decodeObject(of: NSData.self, forKey: Self.nodeKey) as Data?,
            let node = try? JSONDecoder().decode(DocNode.self, from: json)
        else { return nil }
        self.node = node
        super.init(coder: coder)
        allowsTextAttachmentView = false
    }

    override func attachmentBounds(
        for attributes: [NSAttributedString.Key: Any], location: any NSTextLocation,
        textContainer: NSTextContainer?, proposedLineFragment: CGRect, position: CGPoint
    ) -> CGRect {
        let width = max(proposedLineFragment.width - 4, 100)
        // TextKit lays the page out on the main thread, where the heights
        // blocks measured are kept.
        let node = node
        let height = MainActor.assumeIsolated { BlockCard.height(node, width: width) }
        return CGRect(x: 0, y: 0, width: width, height: height)
    }

    override func image(
        for bounds: CGRect, attributes: [NSAttributedString.Key: Any], location: any NSTextLocation,
        textContainer: NSTextContainer?
    ) -> UIImage? {
        Self.blank
    }

    override func image(
        forBounds imageBounds: CGRect, textContainer: NSTextContainer?,
        characterIndex charIndex: Int
    ) -> UIImage? {
        Self.blank
    }

    /// blank is what TextKit draws for the block: nothing.
    private static let blank = UIImage()
}

/// BlockViews shows the views of the blocks without text that are in
/// view, each over its attachment's room, made once and kept while the
/// page is open, so a web block isn't loaded again as it scrolls past.
@MainActor
final class BlockViews {
    private struct Shown {
        let node: DocNode
        let view: UIView
        /// host is the SwiftUI block's, or nil for a web block's view.
        let host: UIHostingController<BlockCard>?
    }

    private var shown: [String: Shown] = [:]
    /// links let subpages' cards show their pages' titles and open them.
    /// The page gives them once the window hears of its editor, which can
    /// be after the first cards showed: those take them then, or they
    /// would say their pages are not found for as long as the page is open.
    var links: PageLinks? {
        didSet { refresh() }
    }
    /// edit opens a block's editor, from its long press's menu, while the
    /// page can be changed.
    var edit: (@MainActor (DocNode) -> Void)? {
        didSet { refresh() }
    }
    /// editable says the page can be changed, which the blocks' views
    /// follow as it changes: a table opens its sheet only then.
    var editable = false {
        didSet { if editable != oldValue { refresh() } }
    }
    /// kept is how many views out of view are kept.
    private let kept = 40

    /// layout places the views of the attachments in the text view's
    /// viewport, and hides the others.
    func layout(in textView: UITextView) {
        guard let tlm = textView.textLayoutManager,
            let viewport = tlm.textViewportLayoutController.viewportRange
        else { return }
        let storage = textView.textStorage
        let doc = tlm.documentRange.location
        let start = tlm.offset(from: doc, to: viewport.location)
        let end = min(tlm.offset(from: doc, to: viewport.endLocation), storage.length)
        var visible = Set<String>()
        if start < end {
            storage.enumerateAttribute(
                .attachment, in: NSRange(location: start, length: end - start)
            ) { value, range, _ in
                guard let attachment = value as? BlockAttachment,
                    let info = storage.attribute(
                        .leafletBlock, at: range.location, effectiveRange: nil)
                        as? BlockInfo,
                    let from = tlm.location(doc, offsetBy: range.location),
                    let to = tlm.location(from, offsetBy: 1),
                    let textRange = NSTextRange(location: from, end: to)
                else { return }
                var frame = CGRect.null
                tlm.enumerateTextSegments(in: textRange, type: .standard, options: []) {
                    _, rect, _, _ in
                    frame = rect
                    return false
                }
                guard !frame.isNull, frame.height > 0 else { return }
                let view = self.view(for: attachment.node, id: info.id, in: textView)
                let inset = textView.textContainerInset
                // Set only when it moved: a SwiftUI block lays out again
                // for each frame it is given.
                let placed = frame.offsetBy(dx: inset.left, dy: inset.top)
                if view.frame != placed { view.frame = placed }
                if view.isHidden { view.isHidden = false }
                if view.superview !== textView { textView.addSubview(view) }
                visible.insert(info.id)
            }
        }
        // Out of view, a web block leaves the text view: WebKit pauses a
        // page's animations and timers only out of the window, and an
        // embed's would go on drawing, slowing scrolling. A SwiftUI block
        // only hides, which keeps it laid out for when it comes back.
        for (id, s) in shown where !visible.contains(id) {
            if s.host == nil {
                if s.view.superview != nil { Self.takeOut(s.view) }
            } else if !s.view.isHidden {
                s.view.isHidden = true
            }
        }
        if shown.count > kept + visible.count {
            for id in shown.keys where !visible.contains(id) {
                drop(id)
                if shown.count <= kept + visible.count { break }
            }
        }
    }

    /// view returns the block's view, made again when the block changed.
    /// Its hosting controller joins the page's view controller: without
    /// it, SwiftUI never tells the block it appeared, and a picture or a
    /// video's thumbnail never starts loading.
    private func view(for node: DocNode, id: String, in textView: UIView) -> UIView {
        if let s = shown[id], s.node == node { return s.view }
        drop(id)
        // A web block is its web view alone: inside a SwiftUI host in the
        // text view, WebKit paints the page but it never shows.
        if let page = BlockCard.page(for: node) {
            let web = WebBlockView(html: page, interactive: node.type == "embed")
            web.clipsToBounds = true
            web.onHeight = { [weak textView] size in
                // The block's room follows its page, laid out again once.
                guard BlockHeights.set(node.id, size), let tv = textView as? EditorTextView
                else { return }
                tv.relayout(block: node.id)
            }
            textView.addSubview(web)
            shown[id] = Shown(node: node, view: web, host: nil)
            return web
        }
        let host = UIHostingController(rootView: card(node, in: textView))
        host.view.backgroundColor = .clear
        // A block draws within its room in the text, never over the text.
        host.view.clipsToBounds = true
        host.sizingOptions = []
        host.safeAreaRegions = []
        if let parent = sequence(first: textView.next, next: { $0?.next })
            .first(where: { $0 is UIViewController }) as? UIViewController
        {
            parent.addChild(host)
            textView.addSubview(host.view)
            host.didMove(toParent: parent)
        }
        shown[id] = Shown(node: node, view: host.view, host: host)
        return host.view
    }

    /// card is a block's view, as the page can be changed or not.
    private func card(_ node: DocNode, in textView: UIView) -> BlockCard {
        BlockCard(
            node: node, links: links, edit: editable ? edit : nil,
            editor: (textView as? EditorTextView)?.controller,
            onHeight: { [weak textView] size in
                // The block's room follows its view, laid out again once.
                guard BlockHeights.set(node.id, size), let tv = textView as? EditorTextView
                else { return }
                tv.relayout(block: node.id)
            })
    }

    /// refresh gives the blocks' views shown the page's links and edit as
    /// they are now, keeping what each shows.
    private func refresh() {
        for s in shown.values {
            guard let host = s.host, let textView = s.view.superview else { continue }
            host.rootView = card(s.node, in: textView)
        }
    }

    /// editable are the blocks a long press offers to edit.
    static let editable: Set = ["chart", "math_equation", "diagram", "embed"]

    /// menuHit returns the block at point, in the text view's space, with
    /// its view, when a long press there offers to edit it.
    func menuHit(_ point: CGPoint) -> (node: DocNode, view: UIView)? {
        guard editable, edit != nil else { return nil }
        for s in shown.values where Self.editable.contains(s.node.type) {
            if !s.view.isHidden, s.view.superview != nil, s.view.frame.contains(point) {
                return (s.node, s.view)
            }
        }
        return nil
    }

    /// shownView returns the view of the block of id, while it shows.
    func shownView(_ id: String) -> UIView? {
        guard let view = shown[id]?.view, !view.isHidden, view.superview != nil else { return nil }
        return view
    }

    /// reset lets the blocks' views go, every one or those of the blocks
    /// chosen, for each to be made again as it shows: as the theme
    /// changes, or the text size tables' cells are in, and as the page
    /// closes, whose view controller would keep them.
    func reset(where chosen: (DocNode) -> Bool = { _ in true }) {
        for (id, s) in shown where chosen(s.node) { drop(id) }
    }

    /// takeOut takes a block's view out of the text view. An embed used
    /// last has the focus, which goes with it: UIKit would hand it back to
    /// the text view, which then scrolls back to its caret, away from where
    /// the page was scrolled to.
    private static func takeOut(_ view: UIView) {
        let textView = view.superview as? EditorTextView
        textView?.refusesFocus = true
        view.removeFromSuperview()
        textView?.refusesFocus = false
    }

    private func drop(_ id: String) {
        guard let s = shown[id] else { return }
        s.host?.willMove(toParent: nil)
        Self.takeOut(s.view)
        s.host?.removeFromParent()
        shown[id] = nil
    }
}
