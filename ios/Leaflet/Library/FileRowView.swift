import QuickLook
import SwiftUI

struct FileRowView: View {
    let file: FileRow
    @Environment(AppModel.self) private var app
    @State private var shown: URL?
    @State private var deleting = false

    var body: some View {
        Button {
            Task { await open() }
        } label: {
            Label {
                Text(file.name).lineLimit(1)
            } icon: {
                Image(systemName: "doc")
            }
        }
        .tint(.primary)
        .selectionDisabled()
        .contextMenu {
            Button(L.commonOpen, systemImage: "doc.text.magnifyingglass") {
                Task { await open() }
            }
            Divider()
            Button(L.sidebarDeleteFile, systemImage: "trash", role: .destructive) {
                deleting = true
            }
        }
        .quickLookPreview($shown)
        .alert(L.sidebarDeleteFileTitle(name: file.name), isPresented: $deleting) {
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonDelete, role: .destructive) {
                Task { await app.delete(file) }
            }
        } message: {
            Text(L.sidebarDeleteFileMessage)
        }
    }

    private func open() async {
        do {
            guard let path = try await app.client?.localFile(file.id) else { return }
            shown = URL(fileURLWithPath: path)
        } catch {
            app.show(Toast(message: error.localizedDescription))
        }
    }
}

struct PageDrops: ViewModifier {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @State private var height: CGFloat = 0
    @State private var targeted = false

    func body(content: Content) -> some View {
        let row =
            content
            .onGeometryChange(for: CGFloat.self, of: \.size.height) { height = $0 }
            .dropDestination(for: String.self) { ids, at in
                guard let id = ids.first else { return false }
                let before = at.y < height / 2
                Task { await app.place(id, beside: page, before: before) }
                return true
            } isTargeted: {
                targeted = $0
            }
            .listRowBackground(targeted ? Color.accentColor.opacity(0.15) : nil)
        if page.canEdit && !page.template {
            row.draggable(page.id) {
                Label(page.displayTitle, systemImage: "doc.text")
                    .padding(8)
            }
        } else {
            row
        }
    }
}
