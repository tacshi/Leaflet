import SwiftUI

struct PageEditorView<Header: View, Footer: View>: UIViewRepresentable {
    let model: PageModel
    let style: EditorStyle
    var createSubpage: () async -> String? = { nil }
    var onController: (EditorController) -> Void = { _ in }
    @ViewBuilder let header: () -> Header
    @ViewBuilder let footer: () -> Footer

    func makeCoordinator() -> Coordinator {
        Coordinator(controller: EditorController(style: style))
    }

    func makeUIView(context: Context) -> EditorTextView {
        let c = context.coordinator
        let host = UIHostingController(rootView: AnyView(header()))
        host.view.backgroundColor = .clear
        host.sizingOptions = .intrinsicContentSize
        host.safeAreaRegions = []
        c.host = host
        let view = c.controller.textView
        view.header = host.view
        view.measureHeader = { [weak host] width in
            _ = host?.sizeThatFits(in: CGSize(width: width + 1, height: .greatestFiniteMagnitude))
            return host?.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude))
                .height ?? 0
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
        if let content = model.content {
            c.controller.load(content, tinted: model.tinted)
            c.loaded = model.external
            c.tinted = model.tinted
            model.shown = model.rev
        }
        view.isEditable = !model.readOnly
        Task { @MainActor [onController] in onController(c.controller) }
        #if DEBUG
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
            if let target = UserDefaults.standard.string(forKey: "LeafletScrollTo") {
                Task { @MainActor in
                    try? await Task.sleep(for: .milliseconds(2500))
                    let r = (view.text as NSString).range(of: target)
                    guard r.location != NSNotFound,
                        let start = view.position(
                            from: view.beginningOfDocument, offset: r.location)
                    else { return }
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
        view.remeasure()
        c.controller.style = style
        c.controller.createSubpage = createSubpage
        view.isEditable = !model.readOnly
        if model.hasContent, c.loaded != model.external || c.tinted != model.tinted {
            Task { @MainActor [model] in c.show(model) }
        }
    }

    static func dismantleUIView(_ view: EditorTextView, coordinator: Coordinator) {
        if coordinator.controller.hasPendingEdit { coordinator.controller.send() }
        coordinator.controller.close()
        coordinator.closed = true
    }

    @MainActor
    final class Coordinator {
        let controller: EditorController
        var host: UIHostingController<AnyView>?
        var footer: UIHostingController<AnyView>?
        var loaded: Int?
        var tinted: Set<String> = []
        var closed = false

        init(controller: EditorController) { self.controller = controller }

        func show(_ model: PageModel) {
            guard !closed, let content = model.content else { return }
            if loaded != model.external {
                if loaded == nil {
                    controller.load(content, tinted: model.tinted)
                    tinted = model.tinted
                    model.shown = model.rev
                } else if controller.hasPendingEdit, !controller.composing, !model.readOnly {
                    controller.send()
                } else {
                    if controller.update(
                        content, tinted: model.tinted, byAgent: model.byAgent,
                        notesOf: model.notesOf)
                    {
                        model.shown = model.rev
                    }
                    tinted = model.tinted
                }
                loaded = model.external
            }
            if loaded != nil, tinted != model.tinted {
                controller.tint(model.tinted)
                tinted = model.tinted
            }
        }
    }
}
