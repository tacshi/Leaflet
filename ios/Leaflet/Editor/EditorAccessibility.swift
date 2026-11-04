import UIKit

/// EditorAccessibility is what VoiceOver offers at the caret: what a tap
/// does on the block's marker, its due date's chip, its view or a link,
/// which VoiceOver can't reach in the text, and the choices of the menu at
/// the caret while it shows.
@MainActor
enum EditorAccessibility {
    static func actions(_ editor: EditorController) -> [UIAccessibilityCustomAction] {
        var out: [UIAccessibilityCustomAction] = []
        func offer(_ name: String, _ run: @escaping @MainActor (EditorController) -> Void) {
            out.append(
                UIAccessibilityCustomAction(name: name) { [weak editor] _ in
                    guard let editor else { return false }
                    run(editor)
                    return true
                })
        }
        if let menu = editor.caretMenu.menu {
            for item in menu.items { offer(item.label) { $0.caretMenu.pick(item) } }
            return out
        }
        let textView = editor.textView
        let storage = editor.text.storage
        let caret = textView.selectedRange.location
        let (para, info) = editor.text.paragraph(at: caret)
        let at = para.location
        let editable = textView.isEditable
        let collapsed = info.data["collapsed"]?.bool == true
        switch info.type {
        case "todo_list" where editable:
            offer(info.checked ? LI.markNotDone : LI.markDone) { $0.taps.tapMarker(info, at: at) }
            offer(Due(info)?.label() ?? L.editorAddDate) { $0.due.openAtCaret(info) }
        case "toggle_list":
            offer(collapsed ? LI.expand : LI.collapse) { $0.taps.tapMarker(info, at: at) }
        case "heading":
            offer(collapsed ? L.editorUnfoldSection : L.editorFoldSection) {
                $0.taps.tapMarker(info, at: at)
            }
        case "code":
            // What the code's bar offers, Copy and its language.
            offer(L.editorCopyCode) { $0.taps.tapCodeBar(info, at: at, copy: true, from: .zero) }
            if editable {
                let name = CodeLanguage.name(info.data["language"]?.string ?? "")
                offer(L.editorCodeLanguage(name: name)) { c in
                    let caret = c.textView.selectedTextRange.map {
                        c.textView.caretRect(for: $0.end)
                    }
                    c.taps.tapCodeBar(info, at: at, copy: false, from: caret?.origin ?? .zero)
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
            if editable, editor.onEditBlock != nil,
                BlockViews.editable.contains(node.type) || node.type == "table"
            {
                offer(node.type == "chart" ? L.editorEditData : L.commonEdit) {
                    $0.onEditBlock?(node)
                }
            }
            if node.type == "subpage", let id = node.data?["page_id"]?.string {
                offer(L.commonOpen) { $0.pageLinks?.open(id) }
            }
            if node.type == "video" {
                for action in VideoBlock.actions(node, canEdit: editable, editor: editor) {
                    offer(action.title) { _ in action.run() }
                }
            }
            if node.type == "recording", let id = node.data?["recording_id"]?.string {
                for action in RecordingBlock.actions(
                    id, block: node, canEdit: editable, editor: editor)
                {
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
}
