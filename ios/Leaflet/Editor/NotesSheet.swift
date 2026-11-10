import LeafletCore
import SwiftUI

/// NotesSheet writes a video block's notes, as the desktop's dialog does: a
/// line each, a time starting a line playing the video from there. Saved
/// empty, the block has none.
struct NotesSheet: View {
    /// notes are the block's as the sheet opens.
    let notes: String
    /// file says they are a file's, not a video's.
    var file = false
    /// save gives the notes as the block keeps them (mobile.NormalizeNotes).
    let save: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @FocusState private var focused: Bool

    /// kept is text as the block would keep it, worked out once it pauses:
    /// a transcript is long, and this goes through the core.
    @State private var kept = ""
    /// tooLong counts code points, as the core and the server do: an emoji
    /// or a Devanagari syllable is one character here but several there.
    private var tooLong: Bool { kept.unicodeScalars.count > Int(MobileMaxNotes) }
    private var dirty: Bool { kept != MobileNormalizeNotes(notes) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextEditor(text: $text)
                        .font(.body)
                        .frame(minHeight: 240)
                        .focused($focused)
                        .overlay(alignment: .topLeading) {
                            if text.isEmpty {
                                Text(L.videoNotesPlaceholder)
                                    .foregroundStyle(palette.textMuted)
                                    .padding(.top, 8).padding(.leading, 5)
                                    .allowsHitTesting(false)
                            }
                        }
                } footer: {
                    if tooLong {
                        Text(L.videoNotesTooLong(max: Int(MobileMaxNotes)))
                            .foregroundStyle(palette.dangerText)
                    } else {
                        Text(file ? L.cardNotesHint : L.videoNotesHint)
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(file ? L.cardNotesTitle : L.videoNotesTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.commonSave) {
                        // What is there now: kept is of the last pause,
                        // before what was typed or pasted since.
                        let now = MobileNormalizeNotes(text)
                        kept = now
                        guard now.unicodeScalars.count <= Int(MobileMaxNotes) else { return }
                        save(now)
                        dismiss()
                    }
                    .disabled(tooLong || !dirty)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear {
            text = notes
            kept = MobileNormalizeNotes(notes)
            focused = true
        }
        .task(id: text) {
            try? await Task.sleep(for: .milliseconds(150))
            guard !Task.isCancelled else { return }
            kept = MobileNormalizeNotes(text)
        }
    }
}
