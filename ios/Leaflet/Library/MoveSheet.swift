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
    /// targets are the pages it can go under, as the core finds them, the
    /// one changed last first.
    @State private var targets: [String] = []

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
            .task {
                do {
                    targets = try await app.client?.moveTargets(page.id) ?? []
                } catch {
                    app.show(
                        Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
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

    /// parents are the pages the page can go under whose titles have what
    /// is typed.
    private var parents: [PageRow] {
        let q = query.lowercased()
        return targets.compactMap(app.library.page).filter {
            q.isEmpty || $0.displayTitle.lowercased().contains(q)
        }
    }
}
