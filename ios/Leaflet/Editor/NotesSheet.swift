import LeafletCore
import SwiftUI

struct NotesSheet: View {
    let notes: String
    var file = false
    let save: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @FocusState private var focused: Bool

    @State private var kept = ""
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
