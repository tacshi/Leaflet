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
            Button(L.editorLink + "…") { editor?.editLink() }
                .keyboardShortcut("k")
            Divider()
            Menu(LI.turnInto) {
                Button(L.hostSlashText) { editor?.setType("paragraph") }
                    .keyboardShortcut("0", modifiers: [.command, .option])
                Button(L.hostSlashHeading1) { editor?.setType("heading", ["level": .int(1)]) }
                    .keyboardShortcut("1", modifiers: [.command, .option])
                Button(L.hostSlashHeading2) { editor?.setType("heading", ["level": .int(2)]) }
                    .keyboardShortcut("2", modifiers: [.command, .option])
                Button(L.hostSlashHeading3) { editor?.setType("heading", ["level": .int(3)]) }
                    .keyboardShortcut("3", modifiers: [.command, .option])
                Button(L.hostSlashBulleted) { editor?.setType("bulleted_list") }
                    .keyboardShortcut("8", modifiers: [.command, .shift])
                Button(L.hostSlashNumbered) { editor?.setType("numbered_list") }
                    .keyboardShortcut("7", modifiers: [.command, .shift])
                Button(L.hostSlashTodo) { editor?.setType("todo_list", ["checked": .bool(false)]) }
                    .keyboardShortcut("l", modifiers: [.command, .shift])
                Button(L.hostSlashQuote) { editor?.setType("quote") }
                Button(L.hostSlashCode) { editor?.setType("code") }
            }
            Button(LI.moveBlockUp) { editor?.moveBlock(by: -1) }
                .keyboardShortcut(.upArrow, modifiers: [.option, .shift])
            Button(LI.moveBlockDown) { editor?.moveBlock(by: 1) }
                .keyboardShortcut(.downArrow, modifiers: [.option, .shift])
        }
        .disabled(editor?.textView.isEditable != true)
    }
}
