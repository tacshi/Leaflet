import SwiftUI

struct LeafletCommands: Commands {
    let app: AppModel
    @FocusedValue(\.scene) private var focused

    private var scene: SceneModel? { focused ?? app.activeScene }
    @Environment(\.openWindow) private var openWindow

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            Button(L.appMenuNewPage) {
                Task {
                    if let id = await app.createPage() { scene?.open(id) }
                }
            }
            .keyboardShortcut("n")
            Button(L.appMenuNewFolder) { scene?.newFolder() }
                .keyboardShortcut("n", modifiers: [.command, .shift])
                .disabled(scene == nil)
            Button(LI.newWindow) { openWindow(id: "main") }
                .keyboardShortcut("n", modifiers: [.command, .option])
            Divider()
            Button(L.appMenuSearch) { scene?.search() }
                .keyboardShortcut("p")
                .disabled(scene == nil)
        }

        CommandGroup(after: .pasteboard) {
            Button(L.editorCopyPlainText) { scene?.editor?.copyPlainText() }
                .keyboardShortcut("c", modifiers: [.command, .option, .shift])
                .disabled(scene?.editor == nil)
        }

        CommandGroup(replacing: .appSettings) {
            Button(L.settingsTitle + "…") { scene?.openSettings() }
                .keyboardShortcut(",")
                .disabled(scene == nil)
        }

        CommandGroup(replacing: .sidebar) {
            Group {
                Button(L.appMenuToggleSidebar) { scene?.toggleSidebar() }
                    .keyboardShortcut("\\")
                Button(L.appMenuToggleAgentPanel) { scene?.showPanel(.agent) }
                    .keyboardShortcut("j")
                Button(L.appMenuComments) { scene?.showPanel(.comments) }
                    .keyboardShortcut("m", modifiers: [.command, .shift])
                Button(L.panelTabOutline) { scene?.showPanel(.outline) }
                    .keyboardShortcut("o", modifiers: [.command, .shift])
            }
            .disabled(scene == nil)
        }

        CommandMenu(LI.goMenu) {
            Button(L.shellHistoryBack) { scene?.goBack() }
                .keyboardShortcut("[")
                .disabled(!(scene?.canGoBack ?? false))
            Button(L.shellHistoryForward) { scene?.goForward() }
                .keyboardShortcut("]")
                .disabled(!(scene?.canGoForward ?? false))
        }

        CommandGroup(after: .textFormatting) {
            FormatCommands(editor: scene?.editor)
        }
    }
}

struct FormatCommands: View {
    let editor: EditorController?

    var body: some View {
        Group {
            Button(LI.strikethrough) { editor?.toggleInline(.leafletStrike) }
                .keyboardShortcut("x", modifiers: [.command, .shift])
            Button(L.editorInlineCode) { editor?.toggleInline(.leafletCode) }
                .keyboardShortcut("e")
            Button(L.editorInlineMath) { editor?.toggleInline(.leafletMath) }
            Button(L.editorLink + "…") { editor?.editLink() }
                .keyboardShortcut("k")
            Divider()
            Menu(LI.turnInto) {
                ForEach(Array(TurnInto.all.enumerated()), id: \.offset) { _, t in
                    Button(t.title) { editor?.setType(t.type, t.data) }
                        .keyboardShortcut(t.shortcut)
                }
            }
            Button(LI.moveBlockUp) { editor?.moveBlock(by: -1) }
                .keyboardShortcut(.upArrow, modifiers: [.option, .shift])
            Button(LI.moveBlockDown) { editor?.moveBlock(by: 1) }
                .keyboardShortcut(.downArrow, modifiers: [.option, .shift])
        }
        .disabled(editor?.textView.isEditable != true)
    }
}

extension TurnInto {
    /// The shortcut as a menu of the app's takes it.
    var shortcut: KeyboardShortcut? {
        guard let key = key?.first else { return nil }
        var flags: EventModifiers = []
        if modifiers.contains(.command) { flags.insert(.command) }
        if modifiers.contains(.alternate) { flags.insert(.option) }
        if modifiers.contains(.shift) { flags.insert(.shift) }
        return KeyboardShortcut(KeyEquivalent(key), modifiers: flags)
    }
}
