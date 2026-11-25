import LeafletCore
import SwiftUI

/// The editor of a fact card: its layout, icon, title and picture, its
/// facts, its note and its sources.
struct CardSheet: View {
    let node: DocNode
    let save: BlockChange
    @Environment(\.dismiss) private var dismiss
    @Environment(\.palette) private var palette
    @State private var layout = "details"
    @State private var icon = ""
    @State private var title = ""
    @State private var image = ""
    @State private var facts: [Fact] = []
    @State private var note = ""
    @State private var sources: [Source] = []
    @State private var failure: String?
    @State private var original: [String]?

    struct Fact: Identifiable, Equatable, Codable {
        var id = UUID()
        var label = ""
        var value = ""

        private enum CodingKeys: String, CodingKey { case label, value }
    }

    struct Source: Identifiable, Equatable, Codable {
        var id = UUID()
        var url = ""
        var title = ""

        private enum CodingKeys: String, CodingKey { case url, title }
    }

    /// The data keys each of the fields saves into.
    static let keys = [
        ["layout"], ["icon"], ["title"], ["image"], ["facts"], ["note"], ["sources"],
    ]

    private var fields: [String] {
        [
            layout, icon, title, image, (try? jsonString(facts)) ?? "", note,
            (try? jsonString(sources)) ?? "",
        ]
    }
    private var dirty: Bool { original.map { $0 != fields } ?? false }

    private static let icons: [String] = {
        var error: NSError?
        let raw = MobileCardIcons(&error)
        return (try? JSONDecoder().decode([String].self, from: Data(raw.utf8))) ?? []
    }()

    /// A card icon's name, one of the core's card icons by Lucide's name, in
    /// the app's language, as the desktop's icon menu names it.
    static func iconName(_ icon: String) -> String {
        switch icon {
        case "bus": L.cardIconBus
        case "train-front": L.cardIconTrainFront
        case "tram-front": L.cardIconTramFront
        case "car": L.cardIconCar
        case "bike": L.cardIconBike
        case "footprints": L.cardIconFootprints
        case "plane": L.cardIconPlane
        case "ship": L.cardIconShip
        case "map-pin": L.cardIconMapPin
        case "clock": L.cardIconClock
        case "calendar": L.cardIconCalendar
        case "ticket": L.cardIconTicket
        case "banknote": L.cardIconBanknote
        case "wallet": L.cardIconWallet
        case "phone": L.cardIconPhone
        case "hospital": L.cardIconHospital
        case "utensils": L.cardIconUtensils
        case "bed-double": L.cardIconBedDouble
        case "building-2": L.cardIconBuilding2
        case "graduation-cap": L.cardIconGraduationCap
        case "shopping-bag": L.cardIconShoppingBag
        case "info": L.cardIconInfo
        case "star": L.cardIconStar
        case "sun": L.cardIconSun
        case "cloud-rain": L.cardIconCloudRain
        case "thermometer": L.cardIconThermometer
        case "globe": L.cardIconGlobe
        case "user": L.cardIconUser
        case "package": L.cardIconPackage
        case "trending-up": L.cardIconTrendingUp
        default: icon
        }
    }

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    Section {
                        Picker(L.cardLayout, selection: $layout) {
                            Text(L.dialogCardFigures).tag("figures")
                            Text(L.dialogCardDetails).tag("details")
                        }
                        .pickerStyle(.segmented)
                        Picker(L.dialogCardIcon, selection: $icon) {
                            Text(L.dialogCardNoIcon).tag("")
                            ForEach(Self.icons, id: \.self) { name in
                                Label(Self.iconName(name), systemImage: Lucide.symbol(name))
                                    .tag(name)
                            }
                        }
                        TextField(L.dialogFieldTitle, text: $title)
                        if layout == "details" {
                            TextField(L.dialogCardPicture, text: $image)
                                .keyboardType(.URL)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                        }
                    } footer: {
                        Text(L.dialogCardSubtitle)
                    }
                    Section(L.cardFacts) {
                        ForEach($facts) { $fact in
                            VStack(alignment: .leading, spacing: 6) {
                                TextField(L.hostCardLabel, text: $fact.label)
                                    .font(.footnote)
                                    .foregroundStyle(palette.textMuted)
                                TextField(L.hostCardValue, text: $fact.value, axis: .vertical)
                            }
                        }
                        .onDelete { facts.remove(atOffsets: $0) }
                        .onMove { facts.move(fromOffsets: $0, toOffset: $1) }
                        Button(L.cardAddFact, systemImage: "plus") { facts.append(Fact()) }
                    }
                    Section {
                        TextField(L.dialogCardNote, text: $note, axis: .vertical)
                    }
                    Section {
                        ForEach($sources) { $source in
                            VStack(alignment: .leading, spacing: 6) {
                                TextField(L.cardSourceTitle, text: $source.title)
                                TextField(L.cardSourceLink, text: $source.url)
                                    .keyboardType(.URL)
                                    .textInputAutocapitalization(.never)
                                    .autocorrectionDisabled()
                                    .font(.footnote)
                            }
                        }
                        .onDelete { sources.remove(atOffsets: $0) }
                        Button(L.cardAddSource, systemImage: "plus") { sources.append(Source()) }
                    } header: {
                        Text(L.cardSources)
                    } footer: {
                        if let failure {
                            Text(failure).foregroundStyle(palette.dangerText)
                        }
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .navigationTitle(L.dialogCardTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CancelButton(dirty: dirty)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.dialogCardSave, action: apply)
                }
            }
        }
        .interactiveDismissDisabled(dirty)
        .onAppear(perform: load)
    }

    private func load() {
        let d = node.data ?? [:]
        layout = d["layout"]?.string == "figures" ? "figures" : "details"
        icon = d["icon"]?.string ?? ""
        title = d["title"]?.string ?? ""
        image = d["image"]?.string ?? ""
        note = d["note"]?.string ?? ""
        facts = (d["facts"]?.array ?? []).map {
            Fact(label: $0["label"]?.string ?? "", value: $0["value"]?.string ?? "")
        }
        sources = (d["sources"]?.array ?? []).map {
            Source(url: $0["url"]?.string ?? "", title: $0["title"]?.string ?? "")
        }
        original = fields
    }

    private func apply() {
        guard dirty, let original else { return dismiss() }
        let spec: [String: JSONValue] = [
            "layout": .string(layout), "icon": .string(icon), "title": .string(title),
            "image": .string(layout == "details" ? image : ""), "note": .string(note),
            "facts": .array(
                facts.map { .object(["label": .string($0.label), "value": .string($0.value)]) }),
            "sources": .array(
                sources.map { .object(["url": .string($0.url), "title": .string($0.title)]) }),
        ]
        let data: [String: JSONValue]
        do {
            data = try coreValue(spec, MobileCardData)
        } catch {
            // The core says what is wrong with the fields; JSON that would
            // not do is the app's own fault.
            failure =
                error is DecodingError || error is EncodingError
                ? L.appErrorInternal(error: error.localizedDescription)
                : error.localizedDescription
            return
        }
        var now = fields
        if layout != "details" { now[3] = "" }
        let keys = changedKeys(Self.keys, from: original, to: now)
        do {
            try save { $0.setting(keys, from: data) }
            dismiss()
        } catch {
            failure = L.appErrorSave(error: error.localizedDescription)
        }
    }
}
