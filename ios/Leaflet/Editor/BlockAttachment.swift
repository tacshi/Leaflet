import SwiftUI
import UIKit

final class BlockAttachment: NSTextAttachment, @unchecked Sendable {
    let node: DocNode

    init(node: DocNode) {
        self.node = node
        super.init(data: nil, ofType: nil)
        allowsTextAttachmentView = false
    }

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

    private static let blank = UIImage()
}

@MainActor
final class BlockViews {
    private struct Shown {
        let node: DocNode
        let view: UIView
        let host: UIHostingController<BlockCard>?
    }

    private var shown: [String: Shown] = [:]
    var links: PageLinks? {
        didSet { refresh() }
    }
    var edit: (@MainActor (DocNode) -> Void)? {
        didSet { refresh() }
    }
    var editable = false {
        didSet { if editable != oldValue { refresh() } }
    }
    private let kept = 40

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
                let placed = frame.offsetBy(dx: inset.left, dy: inset.top)
                if view.frame != placed { view.frame = placed }
                if view.isHidden { view.isHidden = false }
                if view.superview !== textView { textView.addSubview(view) }
                visible.insert(info.id)
            }
        }
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

    private func view(for node: DocNode, id: String, in textView: UIView) -> UIView {
        if let s = shown[id] {
            if s.node == node { return s.view }
            if let host = s.host, Self.samePlayer(s.node, node) {
                host.rootView = card(node, in: textView)
                shown[id] = Shown(node: node, view: s.view, host: host)
                return s.view
            }
        }
        drop(id)
        if let page = BlockCard.page(for: node) {
            let web = WebBlockView(html: page, interactive: node.type == "embed")
            web.clipsToBounds = true
            web.onHeight = { [weak textView] size in
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

    func placed(_ id: String) -> CGRect? { shown[id]?.view.frame }

    func frame(of id: String) -> CGRect? {
        guard let view = shown[id]?.view, view.superview != nil, !view.isHidden else { return nil }
        return view.frame
    }

    private static func samePlayer(_ old: DocNode, _ new: DocNode) -> Bool {
        guard old.id == new.id, old.type == new.type else { return false }
        return switch new.type {
        case "video": old.data?["url"] == new.data?["url"]
        case "file": old.data?["file_id"] == new.data?["file_id"]
        default: false
        }
    }

    private func card(_ node: DocNode, in textView: UIView) -> BlockCard {
        BlockCard(
            node: node, links: links, edit: editable ? edit : nil,
            editor: (textView as? EditorTextView)?.controller,
            onHeight: { [weak textView] size in
                guard BlockHeights.set(node.id, size), let tv = textView as? EditorTextView
                else { return }
                tv.relayout(block: node.id)
            })
    }

    private func refresh() {
        for s in shown.values {
            guard let host = s.host, let textView = s.view.superview else { continue }
            host.rootView = card(s.node, in: textView)
        }
    }

    static let editable: Set = ["chart", "math_equation", "diagram", "embed"]

    func menuHit(_ point: CGPoint) -> (node: DocNode, view: UIView)? {
        guard editable, edit != nil else { return nil }
        for s in shown.values where Self.editable.contains(s.node.type) {
            if !s.view.isHidden, s.view.superview != nil, s.view.frame.contains(point) {
                return (s.node, s.view)
            }
        }
        return nil
    }

    func owns(_ view: UIView) -> Bool {
        shown.values.contains { $0.view === view }
    }

    func shownView(_ id: String) -> UIView? {
        guard let view = shown[id]?.view, !view.isHidden, view.superview != nil else { return nil }
        return view
    }

    func reset(where chosen: (DocNode) -> Bool = { _ in true }) {
        for (id, s) in shown where chosen(s.node) { drop(id) }
    }

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
