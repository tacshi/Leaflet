import LeafletCore
import SwiftUI

/// ChartSheet edits a chart's data, as the desktop's dialog does: its
/// title, its kind, a line per label with a value per series, and its
/// caption.
struct ChartSheet: View {
    let node: DocNode
    /// save gives the chart's new data.
    let save: ([String: JSONValue]) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var title = ""
    @State private var kind = "bar"
    @State private var text = ""
    @State private var caption = ""
    @State private var failure: String?

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    Section {
                        TextField(L.dialogFieldTitle, text: $title)
                        Picker(L.dialogChartTitle, selection: $kind) {
                            Text(L.dialogChartBar).tag("bar")
                            Text(L.dialogChartLine).tag("line")
                            Text(L.dialogChartPie).tag("pie")
                        }
                        .pickerStyle(.segmented)
                    }
                    Section {
                        CodeTextEditor(text: $text)
                            .frame(minHeight: 180)
                    } footer: {
                        if let failure {
                            Text(failure).foregroundStyle(palette.dangerText)
                        } else {
                            Text(L.dialogChartSubtitle)
                        }
                    }
                    Section {
                        TextField(L.dialogChartCaption, text: $caption)
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(L.dialogChartTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L.commonCancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.dialogChartSave, action: apply)
                }
            }
        }
        .onAppear(perform: load)
    }

    private func load() {
        let d = node.data ?? [:]
        title = d["title"]?.string ?? ""
        kind = d["chart_type"]?.string ?? "bar"
        caption = d["caption"]?.string ?? ""
        let raw = (try? JSONEncoder().encode(d)).map { String(decoding: $0, as: UTF8.self) } ?? "{}"
        text = MobileChartText(raw)
    }

    /// apply reads the chart as the core does, and saves it, or says what
    /// is wrong with it.
    private func apply() {
        var error: NSError?
        let raw = MobileChartData(kind, title, caption, text, &error)
        guard error == nil,
            var data = try? JSONDecoder().decode(
                [String: JSONValue].self, from: Data(raw.utf8))
        else {
            failure = error?.localizedDescription
            return
        }
        // Who made it stays, as on the desktop.
        data["author"] = node.data?["author"]
        save(data)
        dismiss()
    }
}
