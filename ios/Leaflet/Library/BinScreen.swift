import LeafletCore
import SwiftUI

/// The Trash: what was deleted, kept for a while to see, restore or delete for good.
struct BinScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @State private var items: [BinItem]?
    @State private var shown: BinItem?
    @State private var purging: BinItem?
    @State private var emptying = false

    var body: some View {
        List {
            Section {
                Text(L.binHint(days: Int(MobileBinDays)))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .listRowBackground(Color.clear)
            }
            .listSectionSeparator(.hidden)
            if let items, items.isEmpty {
                ContentUnavailableView(L.binNothing(bin: L.binName), systemImage: "trash")
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            Section {
                ForEach(items ?? []) { item in
                    Button {
                        if item.kind == .page { shown = item }
                    } label: {
                        BinRow(item: item)
                    }
                    .tint(.primary)
                    .swipeActions(edge: .leading) {
                        Button(L.binRestore, systemImage: "arrow.uturn.backward") {
                            Task { await restore(item) }
                        }
                        .tint(.blue)
                    }
                    .swipeActions(edge: .trailing) {
                        Button(L.binDeleteForever, systemImage: "trash", role: .destructive) {
                            purging = item
                        }
                    }
                    .contextMenu {
                        Button(L.binRestore, systemImage: "arrow.uturn.backward") {
                            Task { await restore(item) }
                        }
                        Divider()
                        Button(L.binDeleteForever, systemImage: "trash", role: .destructive) {
                            purging = item
                        }
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
        .background { palette.surface.ignoresSafeArea() }
        .navigationTitle(L.binName)
        .navigationBarTitleDisplayMode(.large)
        .toolbar {
            if items?.isEmpty == false {
                ToolbarItem(placement: .primaryAction) {
                    Button(L.binEmptyButton(bin: L.binName), role: .destructive) {
                        emptying = true
                    }
                }
            }
        }
        .task(id: app.libraryVersion) { await load() }
        .sheet(item: $shown) { item in
            BinPageSheet(
                item: item,
                restore: {
                    shown = nil
                    Task { await restore(item) }
                },
                purge: {
                    shown = nil
                    purging = item
                })
        }
        .confirmationDialog(
            L.binDeleteForeverTitle(title: purging?.title ?? ""),
            isPresented: Binding(get: { purging != nil }, set: { if !$0 { purging = nil } }),
            titleVisibility: .visible, presenting: purging
        ) { item in
            Button(L.binDeleteForever, role: .destructive) {
                Task {
                    await app.purge(item)
                    await load()
                }
            }
            Button(L.commonCancel, role: .cancel) {}
        } message: { _ in
            Text(L.binDeleteForeverMessage)
        }
        .confirmationDialog(
            L.binEmptyTitle(bin: L.binName), isPresented: $emptying, titleVisibility: .visible
        ) {
            Button(L.binEmptyButton(bin: L.binName), role: .destructive) {
                Task {
                    await app.emptyBin()
                    await load()
                }
            }
            Button(L.commonCancel, role: .cancel) {}
        } message: {
            Text(L.binEmptyMessage)
        }
    }

    private func load() async {
        guard let client = app.client else { return }
        if let list = await app.read("trash", client.bin) {
            items = list
        } else if items == nil {
            items = []
        }
    }

    private func restore(_ item: BinItem) async {
        let scene = scene
        await app.restore(item) { scene.open($0) }
        await load()
    }
}

/// A page or file in the Trash: its name, when it was deleted and how long it has left there.
private struct BinRow: View {
    let item: BinItem

    var body: some View {
        HStack(spacing: 12) {
            if let icon = item.icon, !icon.isEmpty {
                Text(icon).frame(width: 24)
            } else {
                Image(systemName: item.symbol).foregroundStyle(.secondary).frame(width: 24)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(item.title).lineLimit(1)
                Text(item.when).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
            if item.pages > 1 {
                Text(L.binPages(count: item.pages))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// A page of the Trash as it was, which can only be read, with its history.
private struct BinPageSheet: View {
    let item: BinItem
    let restore: () -> Void
    let purge: () -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var markdown: String?
    @State private var showsHistory = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    Text(item.when).font(.subheadline).foregroundStyle(.secondary)
                    if item.pages > 1 {
                        Text(L.binPages(count: item.pages))
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                    if let markdown {
                        MarkdownPreview(markdown: markdown, size: app.textSize, simpleTables: false)
                            .foregroundStyle(palette.text)
                    } else {
                        ProgressView()
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(24)
            }
            .background { palette.surface.ignoresSafeArea() }
            .navigationTitle(item.title)
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .top, spacing: 0) {
                Label(L.binInBin(bin: L.binName), systemImage: "trash")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
                    .background(.bar)
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L.commonClose) { dismiss() }
                }
                ToolbarItemGroup(placement: .primaryAction) {
                    Button(L.binHistory, systemImage: "clock") { showsHistory = true }
                    Button(L.binRestore, systemImage: "arrow.uturn.backward", action: restore)
                }
                ToolbarItem(placement: .bottomBar) {
                    Button(L.binDeleteForever, role: .destructive, action: purge)
                }
            }
            .sheet(isPresented: $showsHistory) {
                HistorySheet(pageID: item.id, canEdit: false).presentationSizing(.page)
            }
        }
        .task { markdown = (try? await app.client?.currentMarkdown(item.id)) ?? "" }
    }
}
