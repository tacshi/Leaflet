import LeafletCore
import SwiftUI

struct SourceSheet: View {
    let node: DocNode
    let save: ([String: JSONValue]) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""
    @State private var original: String?

    private var diagram: Bool { node.type == "diagram" }
    private var key: String { MobileSourceKey(node.type) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    CodeTextEditor(text: $text, focused: true)
                        .frame(minHeight: 220)
                } footer: {
                    Text(diagram ? L.renderedDiagramHelp : L.renderedMathHelp)
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
                    Button(L.commonSave) {
                        var data = node.data ?? [:]
                        data[key] = .string(text.trimmingCharacters(in: .newlines))
                        save(data)
                        dismiss()
                    }
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
        view.text = text
        if focused { DispatchQueue.main.async { view.becomeFirstResponder() } }
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        if view.text != text { view.text = text }
    }

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

    final class Coordinator: NSObject, UITextViewDelegate {
        var text: Binding<String>
        init(text: Binding<String>) { self.text = text }
        func textViewDidChange(_ view: UITextView) { text.wrappedValue = view.text }
    }
}
