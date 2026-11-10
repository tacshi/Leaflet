import UIKit

@MainActor
final class EditorMenus {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }

    init(_ editor: EditorController) { self.editor = editor }

    func editMenu(for range: NSRange, suggested: [UIMenuElement]) -> UIMenu? {
        guard textView.isEditable, range.length > 0 else {
            if range.length > 0 { return UIMenu(children: withCopyPlainText(suggested)) }
            let (_, info) = editor.text.paragraph(at: range.location)
            guard textView.isEditable, info.type == "todo_list" else {
                return UIMenu(children: suggested)
            }
            let date = UIAction(
                title: Due(info)?.label() ?? L.editorAddDate, image: UIImage(systemName: "calendar")
            ) { [weak self] _ in self?.editor.due.openAtCaret(info) }
            return UIMenu(children: [date] + suggested)
        }
        return UIMenu(
            children: [agentMenu(), formatMenu(), colorMenu()] + withCopyPlainText(suggested))
    }

    private func agentMenu() -> UIMenu {
        UIMenu(
            options: .displayInline,
            children: [
                UIAction(title: L.editorAskAgent, image: UIImage(systemName: "sparkles")) {
                    [weak self] _ in
                    guard let editor = self?.editor else { return }
                    editor.onAsk?(editor.focusBlockIDs(), editor.selectedText(), nil)
                },
                UIAction(title: L.panelTabComments, image: UIImage(systemName: "text.bubble")) {
                    [weak self] _ in
                    guard let editor = self?.editor else { return }
                    let (_, info) = editor.text.paragraph(
                        at: editor.textView.selectedRange.location)
                    editor.onComment?(info.id, editor.selectedText())
                },
            ])
    }

    private func withCopyPlainText(_ actions: [UIMenuElement]) -> [UIMenuElement] {
        let item = UIAction(
            title: L.editorCopyPlainText, image: UIImage(systemName: "doc.plaintext")
        ) { [weak self] _ in self?.editor.clipboard.copyPlainText() }
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

    private func formatMenu() -> UIMenu {
        func toggle(_ key: NSAttributedString.Key) -> UIActionHandler {
            { [weak self] _ in self?.editor.format.toggle(key) }
        }
        return UIMenu(
            title: L.editorFormat, image: UIImage(systemName: "textformat"),
            options: .displayInline,
            children: [
                UIAction(
                    title: L.editorBold, image: UIImage(systemName: "bold"),
                    handler: toggle(.leafletBold)),
                UIAction(
                    title: L.editorItalic, image: UIImage(systemName: "italic"),
                    handler: toggle(.leafletItalic)),
                UIAction(
                    title: LI.underline, image: UIImage(systemName: "underline"),
                    handler: toggle(.leafletUnderline)),
                UIAction(
                    title: LI.strikethrough, image: UIImage(systemName: "strikethrough"),
                    handler: toggle(.leafletStrike)),
                UIAction(
                    title: L.editorInlineCode,
                    image: UIImage(systemName: "chevron.left.forwardslash.chevron.right"),
                    handler: toggle(.leafletCode)),
                UIAction(title: L.editorLink, image: UIImage(systemName: "link")) { [weak self] _ in
                    self?.editor.format.editLink()
                },
            ])
    }

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
                self?.editor.format.set(.leafletColor, hex + "ff")
            }
        }
        let highlight = Self.swatches.map { name, hex in
            UIAction(title: name, image: dot(hex)) { [weak self] _ in
                self?.editor.format.set(.leafletBackground, hex + "40")
            }
        }
        return UIMenu(
            title: L.editorColor, image: UIImage(systemName: "paintpalette"),
            children: [
                UIMenu(
                    title: L.editorColorText, options: .displayInline,
                    children: text + [
                        UIAction(title: L.editorNoTextColor) { [weak self] _ in
                            self?.editor.format.set(.leafletColor, nil)
                        }
                    ]),
                UIMenu(
                    title: L.editorColorHighlight,
                    children: highlight + [
                        UIAction(title: L.editorNoHighlight) { [weak self] _ in
                            self?.editor.format.set(.leafletBackground, nil)
                        }
                    ]),
            ])
    }

    private func blockMenu() -> UIMenu {
        func item(
            _ title: String, _ symbol: String, _ type: String, _ data: [String: JSONValue] = [:]
        ) -> UIAction {
            UIAction(title: title, image: UIImage(systemName: symbol)) { [weak self] _ in
                self?.editor.commands.setType(type, data)
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

    func installKeyboardBar() {
        let insert = Self.menuItem(
            LI.insert, "plus.circle",
            UIMenu(children: [
                UIDeferredMenuElement.uncached { [weak self] done in
                    guard let self else { return done([]) }
                    self.editor.caretMenu.insertMenu(done)
                }
            ]))
        let todo = UIBarButtonItem(
            image: UIImage(systemName: "checklist"),
            primaryAction: UIAction(title: L.hostSlashTodo) { [weak self] _ in
                self?.editor.commands.setType("todo_list", ["checked": .bool(false)])
            })
        let outdent = UIBarButtonItem(
            image: UIImage(systemName: "decrease.indent"),
            primaryAction: UIAction(title: LI.outdent) { [weak self] _ in
                self?.editor.commands.indent(by: -1)
            })
        let indent = UIBarButtonItem(
            image: UIImage(systemName: "increase.indent"),
            primaryAction: UIAction(title: LI.indent) { [weak self] _ in
                self?.editor.commands.indent(by: 1)
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
            let bar = KeyboardBar()
            let undo = UIBarButtonItem(
                image: UIImage(systemName: "arrow.uturn.backward"),
                primaryAction: UIAction(title: L.commonUndo) { [weak self] _ in
                    self?.textView.undoManager?.undo()
                })
            if #available(iOS 27.0, *) {
                undo.visibilityPriority = .high
                for item in [outdent, indent] { item.visibilityPriority = .low }
            }
            bar.toolbar.items = [insert, format, todo, outdent, indent, .flexibleSpace(), undo]
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
        button.widthAnchor.constraint(equalToConstant: button.intrinsicContentSize.width + 12)
            .isActive = true
        return UIBarButtonItem(customView: button)
    }
}

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
    var topmost: UIViewController { presentedViewController?.topmost ?? self }
}

final class KeyboardBar: UIView {
    let toolbar = UIToolbar()

    override init(frame: CGRect) {
        super.init(frame: frame)
        autoresizingMask = .flexibleHeight
        toolbar.translatesAutoresizingMaskIntoConstraints = false
        addSubview(toolbar)
        NSLayoutConstraint.activate([
            toolbar.topAnchor.constraint(equalTo: topAnchor),
            toolbar.leadingAnchor.constraint(equalTo: leadingAnchor),
            toolbar.trailingAnchor.constraint(equalTo: trailingAnchor),
            toolbar.bottomAnchor.constraint(equalTo: safeAreaLayoutGuide.bottomAnchor),
        ])
    }

    convenience init() { self.init(frame: CGRect(x: 0, y: 0, width: 390, height: 44)) }

    required init?(coder: NSCoder) { fatalError() }

    override var intrinsicContentSize: CGSize {
        let height = toolbar.sizeThatFits(
            CGSize(width: max(bounds.width, 320), height: .greatestFiniteMagnitude)
        ).height
        return CGSize(width: UIView.noIntrinsicMetric, height: max(height, 44))
    }

    override func safeAreaInsetsDidChange() {
        super.safeAreaInsetsDidChange()
        invalidateIntrinsicContentSize()
    }
}
