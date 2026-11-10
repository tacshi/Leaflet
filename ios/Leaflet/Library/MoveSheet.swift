import SwiftUI

struct MoveSheet: View {
    private let page: PageRow?
    private let folder: FolderRow?
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var targets: [String] = []

    init(page: PageRow) {
        self.page = page
        folder = nil
    }

    init(folder: FolderRow) {
        page = nil
        self.folder = folder
    }

    var body: some View {
        NavigationStack {
            List {
                if let folder {
                    folderTargets(folder)
                } else if let page {
                    pageTargets(page)
                }
            }
            .searchable(
                text: $query, placement: .navigationBarDrawer(displayMode: .always),
                prompt: folder == nil ? L.dialogMoveSearch : L.dialogMoveSearchFolders
            )
            .navigationTitle(L.dialogMoveTitle(title: page?.displayTitle ?? folder?.name ?? ""))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L.commonCancel) { dismiss() }
                }
            }
            .task {
                guard let page else { return }
                do {
                    targets = try await app.client?.moveTargets(page.id) ?? []
                } catch {
                    app.show(
                        Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
                }
            }
        }
    }

    @ViewBuilder private func pageTargets(_ page: PageRow) -> some View {
        if query.isEmpty, app.library.own(page) {
            target(L.dialogMoveTopLevel, systemImage: "doc.text") {
                await app.move(page, parentID: "", folderID: "")
            }
            ForEach(app.library.folderOrder, id: \.folder.id) { row in
                target(row.folder.name, systemImage: "folder", depth: row.depth) {
                    await app.move(page, parentID: "", folderID: row.folder.id)
                }
            }
        }
        ForEach(parents) { parent in
            target(parent.displayTitle, systemImage: "doc.text") {
                await app.move(page, parentID: parent.id, folderID: "")
            }
        }
    }

    @ViewBuilder private func folderTargets(_ folder: FolderRow) -> some View {
        if query.isEmpty {
            target(L.dialogMoveTopLevel, systemImage: "doc.text") {
                await app.moveFolder(folder.id, into: "")
            }
        }
        let q = query.lowercased()
        ForEach(
            app.library.folderOrder.filter {
                !app.library.folder($0.folder.id, isIn: folder.id)
                    && (q.isEmpty || $0.folder.name.lowercased().contains(q))
            }, id: \.folder.id
        ) { row in
            target(row.folder.name, systemImage: "folder", depth: q.isEmpty ? row.depth : 0) {
                await app.moveFolder(folder.id, into: row.folder.id)
            }
        }
    }

    private func target(
        _ title: String, systemImage: String, depth: Int = 0,
        move: @escaping () async -> Void
    ) -> some View {
        Button {
            Task {
                await move()
                dismiss()
            }
        } label: {
            Label(title, systemImage: systemImage)
                .padding(.leading, CGFloat(depth) * 16)
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
