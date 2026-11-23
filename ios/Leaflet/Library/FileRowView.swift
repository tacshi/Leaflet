import QuickLook
import SwiftUI

struct FileRowView: View {
    let file: FileRow
    @Environment(AppModel.self) private var app
    @State private var shown: URL?

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
                Task { await app.delete(file) }
            }
        }
        .quickLookPreview($shown)
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

    func body(content: Content) -> some View {
        let row = content.modifier(SidebarDropRow(key: SidebarDrops.pageKey(page.id)))
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
