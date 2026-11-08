import LeafletCore
import QuickLook
import SwiftUI

/// FileDialogs are what a page's files ask for over it: the share sheet,
/// Quick Look, naming a speaker in a file's notes, and whether a transcript
/// may replace the notes a file has.
struct FileDialogs: ViewModifier {
    let scene: SceneModel

    func body(content: Content) -> some View {
        content
            // At the top, where no keyboard comes: the iPad's shortcuts bar
            // floats over the page's foot without moving what is there.
            .overlay(alignment: .top) {
                NowPlayingBar(editor: scene.editor)
            }
            .sheet(item: Binding(get: { scene.sharing }, set: { scene.sharing = $0 })) { file in
                ShareSheet(file: file).onDisappear { file.remove() }
            }
            .quickLookPreview(Binding(get: { scene.quickLook }, set: { scene.quickLook = $0 }))
            .alert(
                speakerTitle,
                isPresented: Binding(
                    get: { scene.speakerEdit != nil },
                    set: { if !$0 { scene.speakerEdit = nil } }),
                presenting: scene.speakerEdit
            ) { edit in
                TextField(
                    L.cardSpeakerName,
                    text: Binding(get: { scene.blockName }, set: { scene.blockName = $0 }))
                Button(L.commonCancel, role: .cancel) {}
                Button(L.commonSave) { save(edit) }
            }
            .alert(
                L.recReplaceNotesTitle,
                isPresented: Binding(
                    get: { scene.replacingNotes != nil },
                    set: { if !$0 { scene.replacingNotes = nil } }),
                presenting: scene.replacingNotes
            ) { node in
                Button(L.commonCancel, role: .cancel) {}
                Button(L.recReplaceNotesAction, role: .destructive) {
                    if let page = scene.editor?.pageID {
                        Recorder.shared.transcribeFile(
                            page: page, block: node.id, provider: scene.replacingWith)
                    }
                }
            } message: { _ in
                Text(L.recReplaceNotesMessage)
            }
    }

    private var speakerTitle: String {
        guard let from = scene.speakerEdit?.from else { return L.cardNewSpeaker }
        return L.cardRenameSpeaker(name: from)
    }

    /// save names the speaker, on all their lines or on the line asked for.
    private func save(_ edit: SpeakerEdit) {
        let name = scene.blockName
        FileBlock.editSpeakers(edit.node.id, in: scene.editor) { notes, names in
            if let from = edit.from {
                return MobileRenameSpeaker(notes, names, from, name)
            }
            return MobileSetLineSpeaker(notes, names, edit.place, name)
        }
    }
}
