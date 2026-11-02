import SwiftUI

/// PageEditorView is the page's block editor, with header (the title and
/// tags) above its text, scrolling with it.
struct PageEditorView<Header: View, Footer: View>: UIViewRepresentable {
    let model: PageModel
    let style: EditorStyle
    /// createSubpage makes a page in this one, for the / menu.
    var createSubpage: () async -> String? = { nil }
    /// onController hears the editor, for the window's Format menu.
    var onController: (EditorController) -> Void = { _ in }
    @ViewBuilder let header: () -> Header
    /// footer goes under the text, as a blank page's ways to start it.
    @ViewBuilder let footer: () -> Footer

    func makeCoordinator() -> Coordinator {
        Coordinator(controller: EditorController(style: style))
    }

    func makeUIView(context: Context) -> EditorTextView {
        let c = context.coordinator
        let host = UIHostingController(rootView: AnyView(header()))
        host.view.backgroundColor = .clear
        host.sizingOptions = .intrinsicContentSize
        // The text view already keeps its content clear of the bars; the
        // header, inside it, lays out from its own top.
        host.safeAreaRegions = []
        c.host = host
        let view = c.controller.textView
        view.header = host.view
        view.measureHeader = { [weak host] width in
            host?.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude)).height
                ?? 0
        }
        let foot = UIHostingController(rootView: AnyView(footer()))
        foot.view.backgroundColor = .clear
        foot.sizingOptions = .intrinsicContentSize
        foot.safeAreaRegions = []
        c.footer = foot
        view.footer = foot.view
        view.measureFooter = { [weak foot] width in
            foot?.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude)).height
                ?? 0
        }
        c.controller.onEdit = { [weak model] content in model?.edit(content) }
        model.sendPending = { [weak controller = c.controller] in
            if let controller, controller.hasPendingEdit { controller.send() }
        }
        c.controller.createSubpage = createSubpage
        onController(c.controller)
        if let content = model.content {
            c.controller.load(content, tinted: model.tinted)
            c.loaded = model.external
            model.shown = model.rev
        }
        view.isEditable = !model.readOnly
        #if DEBUG
            // -LeafletAutoScroll YES scrolls the page down and up for good,
            // to profile scrolling on a simulator.
            if UserDefaults.standard.bool(forKey: "LeafletAutoScroll") {
                Task { @MainActor in
                    try? await Task.sleep(for: .seconds(3))
                    var step: CGFloat = 12
                    while !Task.isCancelled, view.window != nil {
                        let top = -view.adjustedContentInset.top
                        let bottom =
                            view.contentSize.height + view.adjustedContentInset.bottom
                            - view.bounds.height
                        var y = view.contentOffset.y + step
                        if y > bottom || y < top {
                            step = -step
                            y = min(max(y, top), bottom)
                        }
                        view.contentOffset.y = y
                        try? await Task.sleep(for: .milliseconds(16))
                    }
                }
            }
            // -LeafletScrollTo <text> scrolls the page to that text, to look
            // at it on a simulator.
            if let target = UserDefaults.standard.string(forKey: "LeafletScrollTo") {
                Task { @MainActor in
                    try? await Task.sleep(for: .milliseconds(2500))
                    let r = (view.text as NSString).range(of: target)
                    guard r.location != NSNotFound,
                        let start = view.position(
                            from: view.beginningOfDocument, offset: r.location)
                    else { return }
                    // The text goes to the top of the view.
                    let y = view.caretRect(for: start).minY - view.adjustedContentInset.top - 8
                    view.setContentOffset(
                        CGPoint(x: 0, y: max(y, -view.adjustedContentInset.top)), animated: false)
                }
            }
        #endif
        return view
    }

    func updateUIView(_ view: EditorTextView, context: Context) {
        let c = context.coordinator
        c.host?.rootView = AnyView(header())
        c.footer?.rootView = AnyView(footer())
        // They may take another height, showing what they now do.
        view.remeasure()
        c.controller.style = style
        c.controller.createSubpage = createSubpage
        view.isEditable = !model.readOnly
        if let content = model.content, c.loaded != model.external {
            if c.loaded == nil {
                c.controller.load(content, tinted: model.tinted)
                model.shown = model.rev
            } else if c.controller.hasPendingEdit, !model.readOnly {
                // What was typed and not yet sent goes first, over the rev
                // the editor shows: the core merges it with this change,
                // and the editor shows the merge as it comes instead.
                c.controller.send()
            } else {
                c.controller.update(content, tinted: model.tinted)
                model.shown = model.rev
            }
            c.loaded = model.external
        }
    }

    static func dismantleUIView(_ view: EditorTextView, coordinator: Coordinator) {
        // What was typed in the last moment goes before the page closes.
        if coordinator.controller.hasPendingEdit { coordinator.controller.send() }
        coordinator.controller.close()
    }

    @MainActor
    final class Coordinator {
        let controller: EditorController
        var host: UIHostingController<AnyView>?
        var footer: UIHostingController<AnyView>?
        /// loaded is the count of the model's outside changes the editor
        /// shows, or nil before it shows any.
        var loaded: Int?

        init(controller: EditorController) { self.controller = controller }
    }
}
