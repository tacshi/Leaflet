import UIKit

/// EditorMenus are the editor's menus: the edit menu of the text chosen,
/// with Ask agent, Comments, the formats and the colors, and the commands
/// over the keyboard on iPhone, and in the shortcuts bar on iPad.
@MainActor
final class EditorMenus {
    private unowned let editor: EditorController
    private var textView: EditorTextView { editor.textView }

    init(_ editor: EditorController) { self.editor = editor }

    /// editMenu is the edit menu of the text in range, made of the system's
    /// suggested actions and the editor's.
    func editMenu(for range: NSRange, suggested: [UIMenuElement]) -> UIMenu? {
        guard textView.isEditable, range.length > 0 else {
            // Text chosen on a page that can't change is copied.
            if range.length > 0 { return UIMenu(children: withCopyPlainText(suggested)) }
            // At the caret in a to-do, its due date, which its chip may
            // have no room to offer.
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

    /// agentMenu asks an agent about the text chosen, or comments on it.
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

    /// withCopyPlainText puts Copy as Plain Text after the system's Copy
    /// in an edit menu's actions, or first when they have no Copy: a page
    /// of plain text, not Copy's own symbol.
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

    /// blockMenu turns the block at the caret into another kind, as the
    /// menu bar's Turn Into does.
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

    // MARK: Keyboard bar

    /// installKeyboardBar puts the editor's commands over the keyboard on
    /// iPhone, and in the shortcuts bar on iPad.
    func installKeyboardBar() {
        // Insert offers what "/" does, and puts it in as "/" does: Turn
        // Into is the Format menu's.
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
