import LeafletCore
import SwiftUI

/// How a sheet saves its change to the block it opened on: into the block as
/// the page has it now, which an agent or another device may have changed
/// in the meantime. It throws when the block is gone, or the change can't
/// be made of it.
typealias BlockChange = (_ change: (DocNode) throws -> DocNode) throws -> Void

/// The data keys a sheet's change goes into: those of the fields that are no
/// longer as the sheet opened on them, each field's keys as given.
func changedKeys(_ keys: [[String]], from original: [String], to fields: [String]) -> [String] {
    zip(keys, zip(original, fields)).flatMap { keys, field in field.0 == field.1 ? [] : keys }
}

struct SourceSheet: View {
    let node: DocNode
    let save: BlockChange
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @State private var original: String?
    @State private var failure: String?

    private var diagram: Bool { node.type == "diagram" }
    private var key: String { MobileSourceKey(node.type) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    CodeTextEditor(
                        text: $text, label: diagram ? L.renderedDiagram : L.renderedMath,
                        focused: true
                    )
                    .frame(minHeight: 220)
                } footer: {
                    if let failure {
                        Text(failure).foregroundStyle(palette.dangerText)
                    } else {
                        Text(diagram ? L.renderedDiagramHelp : L.renderedMathHelp)
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(diagram ? L.renderedDiagram : L.renderedMath)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.commonSave, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear {
            text = node.data?[key]?.string ?? ""
            original = text
        }
    }

    private var dirty: Bool { original.map { $0 != text } ?? false }

    private func apply() {
        guard dirty else { return dismiss() }
        let source: [String: JSONValue] = [key: .string(text.trimmingCharacters(in: .newlines))]
        do {
            try save { $0.setting(source.keys, from: source) }
            dismiss()
        } catch {
            failure = L.appErrorSave(error: error.localizedDescription)
        }
    }
}

struct CancelButton: View {
    let dirty: Bool
    @Environment(\.dismiss) private var dismiss
    @State private var asking = false

    var body: some View {
        Button(L.commonCancel) {
            if dirty { asking = true } else { dismiss() }
        }
        .confirmationDialog(L.dialogDiscardChanges, isPresented: $asking) {
            Button(L.dialogDiscardChanges, role: .destructive) { dismiss() }
            Button(L.dialogKeepEditing, role: .cancel) {}
        }
    }
}

struct CodeTextEditor: UIViewRepresentable {
    @Binding var text: String
    /// What the field holds, as VoiceOver says it: its sheet's title.
    let label: String
    var focused = false

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.font = UIFontMetrics(forTextStyle: .callout).scaledFont(
            for: .monospacedSystemFont(ofSize: 16, weight: .regular))
        view.adjustsFontForContentSizeCategory = true
        view.backgroundColor = .clear
        view.autocapitalizationType = .none
        view.autocorrectionType = .no
        view.spellCheckingType = .no
        view.smartDashesType = .no
        view.smartQuotesType = .no
        view.smartInsertDeleteType = .no
        view.delegate = context.coordinator
        view.accessibilityLabel = label
        view.text = text
        if focused { DispatchQueue.main.async { view.becomeFirstResponder() } }
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        if view.text != text { view.text = text }
        view.accessibilityLabel = label
    }

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

    final class Coordinator: NSObject, UITextViewDelegate {
        var text: Binding<String>
        init(text: Binding<String>) { self.text = text }
        func textViewDidChange(_ view: UITextView) { text.wrappedValue = view.text }
    }
}
