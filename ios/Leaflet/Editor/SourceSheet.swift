import SwiftUI

/// SourceSheet edits a formula's TeX or a diagram's Mermaid, as the
/// desktop's dialog does.
struct SourceSheet: View {
    let node: DocNode
    /// save gives the block's new data.
    let save: ([String: JSONValue]) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var text = ""

    private var diagram: Bool { node.type == "diagram" }
    /// key is the data the source is kept under.
    private var key: String { diagram ? "source" : "formula" }

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
                    Button(L.commonCancel) { dismiss() }
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
        .onAppear { text = node.data?[key]?.string ?? "" }
    }
}

/// CodeTextEditor is a text area for source: monospaced, with no smart
/// dashes, quotes or corrections, which would break "-->" or a TeX
/// command.
struct CodeTextEditor: UIViewRepresentable {
    @Binding var text: String
    var focused = false

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.font = .monospacedSystemFont(
            ofSize: UIFont.preferredFont(forTextStyle: .callout).pointSize, weight: .regular)
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
