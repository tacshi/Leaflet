import SwiftUI

struct MoveSheet: View {
    let page: PageRow
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var targets: [String] = []

    var body: some View {
        NavigationStack {
            List {
                if query.isEmpty, app.library.own(page) {
                    target(L.dialogMoveTopLevel, systemImage: "doc.text") {
                        await app.move(page, parentID: "", folderID: "")
                    }
                    ForEach(app.library.folders) { folder in
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

    private var parents: [PageRow] {
        let q = query.lowercased()
        return targets.compactMap(app.library.page).filter {
            q.isEmpty || $0.displayTitle.lowercased().contains(q)
        }
    }
}
