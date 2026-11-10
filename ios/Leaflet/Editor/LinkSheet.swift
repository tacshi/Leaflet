import LeafletCore
import SwiftUI

struct LinkSheet: View {
    let kind: String
    var current: String?
    let insert: (DocNode) -> Void
    var upload: (() -> Void)?
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var link = ""
    @State private var failure: String?
    @State private var making = false
    @FocusState private var focused: Bool

    private var title: String {
        switch kind {
        case "video": L.dialogLinkVideoTitle
        case "image": L.dialogLinkImageTitle
        default: L.dialogLinkCardTitle
        }
    }

    private var subtitle: String {
        switch kind {
        case "video": L.dialogLinkVideoSubtitle
        case "image": L.dialogLinkImageSubtitle
        default: L.dialogLinkCardSubtitle
        }
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(
                        kind == "video" ? "https://www.youtube.com/watch?v=…" : "https://",
                        text: $link
                    )
                    .keyboardType(.URL)
                    .textContentType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .focused($focused)
                    .onSubmit(apply)
                } footer: {
                    if let failure {
                        Text(failure).foregroundStyle(palette.dangerText)
                    } else {
                        Text(subtitle)
                    }
                }
                .listRowBackground(palette.surface)
                if let upload {
                    Section {
                        Button(L.dialogLinkImageUpload, systemImage: "photo.on.rectangle") {
                            dismiss()
                            upload()
                        }
                    }
                    .listRowBackground(palette.surface)
                }
            }
            .themedForm(palette)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if making {
                        ProgressView()
                    } else {
                        Button(current == nil ? L.dialogInsert : L.commonSave, action: apply)
                    }
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear {
            link = current ?? ""
            focused = true
        }
    }

    private var dirty: Bool {
        let typed = link.trimmingCharacters(in: .whitespacesAndNewlines)
        return current.map { typed != $0 } ?? !typed.isEmpty
    }

    private func apply() {
        guard !making else { return }
        making = true
        let kind = kind
        let link = link
        Task {
            let made = await Task.detached { () -> (raw: String, failure: String?) in
                var error: NSError?
                let raw = MobileLinkBlock(kind, link, &error)
                return (raw, error?.localizedDescription)
            }.value
            making = false
            guard made.failure == nil,
                let node = try? JSONDecoder().decode(DocNode.self, from: Data(made.raw.utf8))
            else {
                failure = made.failure
                return
            }
            insert(node)
            dismiss()
        }
    }
}

struct EmbedSheet: View {
    let author: String
    var editing: DocNode?
    let insert: (DocNode) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var title = L.dialogEmbedTitle
    @State private var html = MobileEmbedStart()
    @State private var height = "320"
    @State private var failure: String?
    @State private var original: [String]?

    private var fields: [String] { [title, html, height] }
    private var dirty: Bool { original.map { $0 != fields } ?? false }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(L.dialogFieldTitle, text: $title)
                }
                .listRowBackground(palette.surface)
                Section {
                    CodeTextEditor(text: $html, focused: true)
                        .frame(minHeight: 240)
                } footer: {
                    if let failure {
                        Text(failure).foregroundStyle(palette.dangerText)
                    } else {
                        Text(L.dialogEmbedSubtitle)
                    }
                }
                .listRowBackground(palette.surface)
                Section {
                    LabeledContent(L.dialogEmbedHeight) {
                        TextField("320", text: $height)
                            .keyboardType(.numberPad)
                            .multilineTextAlignment(.trailing)
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(L.dialogEmbedTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(editing == nil ? L.dialogInsert : L.commonSave, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear {
            defer { original = fields }
            guard let d = editing?.data else { return }
            title = d["title"]?.string ?? ""
            html = d["html"]?.string ?? ""
            height = String(d["height"]?.int ?? 320)
        }
    }

    private func apply() {
        var error: NSError?
        let raw = MobileEmbedBlock(title, html, Int(height) ?? 0, author, &error)
        guard error == nil, var node = try? JSONDecoder().decode(DocNode.self, from: Data(raw.utf8))
        else {
            failure = error?.localizedDescription
            return
        }
        if let editing {
            node.id = editing.id
            node.data?["author"] = editing.data?["author"]
        }
        insert(node)
        dismiss()
    }
}
