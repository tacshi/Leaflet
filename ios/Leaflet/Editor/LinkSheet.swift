import LeafletCore
import SwiftUI

/// LinkSheet asks for the link of a block to put at the caret, as the
/// desktop's link dialog does: a picture, a video or a link card, of kind
/// "image", "video" or "link_preview". A picture can be one on the device
/// instead.
struct LinkSheet: View {
    let kind: String
    /// insert gives the block made from the link.
    let insert: (DocNode) -> Void
    /// upload picks a picture on the device instead, for "image".
    var upload: (() -> Void)?
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var link = ""
    @State private var failure: String?
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
                    Button(L.dialogInsert, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear { focused = true }
    }

    /// dirty says a link was typed or pasted, which Cancel asks before
    /// throwing away.
    private var dirty: Bool { !link.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    /// apply makes the block as the core does, or says what is wrong with
    /// the link.
    private func apply() {
        var error: NSError?
        let raw = MobileLinkBlock(kind, link, &error)
        guard error == nil, let node = try? JSONDecoder().decode(DocNode.self, from: Data(raw.utf8))
        else {
            failure = error?.localizedDescription
            return
        }
        insert(node)
        dismiss()
    }
}

/// EmbedSheet makes an embed, a page of HTML that runs in the page, as the
/// desktop's dialog does: its title, its HTML and how tall it is. Given
/// an embed in the page, it changes that one.
struct EmbedSheet: View {
    /// author is who made it, as the block keeps it.
    let author: String
    var editing: DocNode?
    /// insert gives the embed made, or the one changed, keeping its id
    /// and who made it.
    let insert: (DocNode) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var title = L.dialogEmbedTitle
    @State private var html = MobileEmbedStart()
    @State private var height = "320"
    @State private var failure: String?
    /// original is the embed as the sheet opened with it.
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
