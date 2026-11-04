import LeafletCore
import SwiftUI

/// NotesSheet writes a video block's notes, as the desktop's dialog does: a
/// line each, a time starting a line playing the video from there. Saved
/// empty, the block has none.
struct NotesSheet: View {
    /// notes are the block's as the sheet opens.
    let notes: String
    /// save gives the notes as the block keeps them (mobile.NormalizeNotes).
    let save: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @FocusState private var focused: Bool

    private var kept: String { MobileNormalizeNotes(text) }
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
                        Text(L.videoNotesHint)
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(L.videoNotesTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.commonSave) {
                        save(kept)
                        dismiss()
                    }
                    .disabled(tooLong || !dirty)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear {
            text = notes
            focused = true
        }
    }
}
