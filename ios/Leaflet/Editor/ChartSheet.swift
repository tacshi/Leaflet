import LeafletCore
import SwiftUI

struct ChartSheet: View {
    let node: DocNode
    let save: ([String: JSONValue]) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var title = ""
    @State private var kind = "bar"
    @State private var text = ""
    @State private var caption = ""
    @State private var failure: String?
    @State private var original: [String]?

    private var fields: [String] { [title, kind, text, caption] }
    private var dirty: Bool { original.map { $0 != fields } ?? false }

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
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.dialogChartSave, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear(perform: load)
    }

    private func load() {
        let d = node.data ?? [:]
        title = d["title"]?.string ?? ""
        kind = d["chart_type"]?.string ?? "bar"
        caption = d["caption"]?.string ?? ""
        let raw = (try? JSONEncoder().encode(d)).map { String(decoding: $0, as: UTF8.self) } ?? "{}"
        text = MobileChartText(raw)
        original = fields
    }

    private func apply() {
        var error: NSError?
        let raw = MobileChartData(kind, title, caption, text, &error)
        if let error {
            failure = error.localizedDescription
            return
        }
        do {
            var data = try JSONDecoder().decode([String: JSONValue].self, from: Data(raw.utf8))
            data["author"] = node.data?["author"]
            save(data)
            dismiss()
        } catch {
            failure = L.appErrorInternal(error: error.localizedDescription)
        }
    }
}
