import SwiftUI

/// MoveSheet moves a page, as the desktop's Move to dialog: to the top
/// level or into a folder, for one of the user's own, or under another
/// page of its space, found by its title. A page stays in its space: the
/// server refuses a move out of it.
struct MoveSheet: View {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        NavigationStack {
            List {
                if query.isEmpty, app.library.own(page) {
                    target(L.dialogMoveTopLevel, systemImage: "doc.text") {
                        await app.move(page, parentID: "", folderID: "")
                    }
                    ForEach(app.library.sortedFolders) { folder in
                        target(folder.name, systemImage: "folder") {
                            await app.move(page, parentID: "", folderID: folder.id)
                        }
                    }
                }
                ForEach(parents) { parent in
                    target(parent.displayTitle, systemImage: "doc.text") {
                        await app.move(page, parentID: parent.id, folderID: "")
                    }
                }
            }
            .searchable(
                text: $query, placement: .navigationBarDrawer(displayMode: .always),
                prompt: L.dialogMoveSearch
            )
            .navigationTitle(L.dialogMoveTitle(title: page.displayTitle))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L.commonCancel) { dismiss() }
                }
            }
        }
    }

    private func target(
        _ title: String, systemImage: String, move: @escaping () async -> Void
    ) -> some View {
        Button {
            Task {
                await move()
                dismiss()
            }
        } label: {
            Label(title, systemImage: systemImage)
        }
        .tint(.primary)
    }

    /// parents are the pages the page can go under: those of its space the
    /// user may change, but not the page itself or one inside it.
    private var parents: [PageRow] {
        let lib = app.library
        let q = query.lowercased()
        return lib.pages
            .filter { p in
                p.canEdit && !p.template && p.spaceID == page.spaceID && !inside(p, lib)
                    && (q.isEmpty || p.displayTitle.lowercased().contains(q))
            }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    /// inside reports whether p is the page or nested in it.
    private func inside(_ p: PageRow, _ lib: LibrarySnapshot) -> Bool {
        var at: PageRow? = p
        var seen = Set<String>()
        while let cur = at, seen.insert(cur.id).inserted {
            if cur.id == page.id { return true }
            at = cur.parentID.flatMap(lib.page)
        }
        return false
    }
}
