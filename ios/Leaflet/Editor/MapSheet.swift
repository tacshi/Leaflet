import LeafletCore
import SwiftUI

/// The editor of a map block: its title, its places a line each, and how
/// its route goes from the first to the last. Saving asks the map
/// services for the places and the route, off the main thread.
struct MapSheet: View {
    var editing: DocNode?
    var author = ""
    /// Takes the map's new data: puts a new map in the page, or saves an
    /// edited one into its block.
    let done: (_ data: [String: JSONValue]) throws -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @Environment(AppModel.self) private var app
    @State private var title = ""
    @State private var places = ""
    @State private var mode = ""
    @State private var busy = false
    @State private var failure: String?
    @State private var original: [String]?
    /// The search Save started, which closing the sheet stops.
    @State private var search: Task<Void, Never>?

    private var fields: [String] { [title, places, mode] }
    private var dirty: Bool { original.map { $0 != fields } ?? false }

    private struct Fields: Codable {
        var title: String
        var places: String
        var mode: String
    }

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    Section {
                        TextField(L.dialogFieldTitle, text: $title)
                    }
                    Section {
                        TextField(L.dialogMapPlaces, text: $places, axis: .vertical)
                            .lineLimit(4...12)
                            .textInputAutocapitalization(.words)
                    } footer: {
                        Text(L.dialogMapSubtitle)
                    }
                    Section {
                        Picker(L.dialogMapRoute, selection: $mode) {
                            Text(L.dialogMapNoRoute).tag("")
                            Text(L.mapModeWalk).tag("walk")
                            Text(L.mapModeCycle).tag("cycle")
                            Text(L.mapModeDrive).tag("drive")
                            Text(L.mapModeTransit).tag("transit")
                        }
                    } footer: {
                        if busy {
                            Text(L.dialogMapFinding)
                        } else if let failure {
                            Text(failure).foregroundStyle(palette.dangerText)
                        } else {
                            Text(L.dialogMapPrivacy)
                        }
                    }
                }
                .listRowBackground(palette.surface)
                .disabled(busy)
            }
            .themedForm(palette)
            .navigationTitle(L.dialogMapTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if busy {
                        ProgressView()
                    } else {
                        Button(L.dialogMapSave) { search = Task { await apply() } }
                    }
                }
            }
        }
        .interactiveDismissDisabled(dirty || busy)
        .onAppear(perform: load)
        // Cancel while it searches: the search stops, and its map goes nowhere.
        .onDisappear { search?.cancel() }
    }

    private func load() {
        if let data = editing?.data, let f = try? coreValue(data, as: Fields.self, MobileMapFields)
        {
            (title, places, mode) = (f.title, f.places, f.mode)
        }
        original = fields
    }

    private func apply() async {
        guard !busy else { return }
        if editing != nil, !dirty { return dismiss() }
        guard let client = app.client else { return }
        busy = true
        failure = nil
        let input = (try? jsonString(Fields(title: title, places: places, mode: mode))) ?? ""
        let was = editing?.data.flatMap { try? jsonString($0) } ?? ""
        let raw: String
        do {
            raw = try await client.buildMap(input, was: was, language: L.language)
        } catch {
            if Task.isCancelled { return }
            busy = false
            failure = error.localizedDescription
            return
        }
        if Task.isCancelled { return }
        busy = false
        guard var data = try? JSONDecoder().decode([String: JSONValue].self, from: Data(raw.utf8))
        else {
            failure = L.appErrorInternal(error: raw)
            return
        }
        // A new map is the person's; an edited one keeps its author, as
        // saving it leaves that as it is.
        if editing == nil { data["author"] = .string(author) }
        do {
            try done(data)
            dismiss()
        } catch {
            failure = L.appErrorSave(error: error.localizedDescription)
        }
    }
}
