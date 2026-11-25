import LeafletCore
import MapKit
import SwiftUI

/// A map block as the app draws it, read by the Go core: its places, its
/// legs' points, its route in words, whose data it shows, and the box
/// around it all.
struct MapLook: Decodable, Equatable, Sendable {
    struct Place: Decodable, Equatable, Sendable {
        let name: String
        let lat: Double
        let lng: Double
        var coordinate: CLLocationCoordinate2D { .init(latitude: lat, longitude: lng) }
    }

    struct Leg: Decodable, Equatable, Sendable {
        let walk: Bool
        let color: String
        let coords: [[Double]]
        var coordinates: [CLLocationCoordinate2D] {
            coords.compactMap { $0.count == 2 ? .init(latitude: $0[0], longitude: $0[1]) : nil }
        }
    }

    let title: String
    let places: [Place]
    let legs: [Leg]
    let summary: String
    let steps: String
    let credit: String
    let sources: String
    let apple: String
    let south: Double
    let west: Double
    let north: Double
    let east: Double

    @MainActor private static var read: [String: (DocNode, String, MapLook)] = [:]

    @MainActor static func of(_ node: DocNode) -> MapLook? {
        let language = L.language
        if let (kept, lang, look) = read[node.id], kept == node, lang == language { return look }
        let raw = (try? JSONEncoder().encode(node)).map { String(decoding: $0, as: UTF8.self) }
        var error: NSError?
        let json = MobileMapView(raw ?? "{}", language, &error)
        guard error == nil,
            let look = try? JSONDecoder().decode(MapLook.self, from: Data(json.utf8))
        else { return nil }
        if read.count >= 64 { read.removeAll(keepingCapacity: true) }
        read[node.id] = (node, language, look)
        return look
    }

    /// The part of the map that shows it all, with room around it.
    var rect: MKMapRect {
        let a = MKMapPoint(CLLocationCoordinate2D(latitude: north, longitude: west))
        let b = MKMapPoint(CLLocationCoordinate2D(latitude: south, longitude: east))
        var r = MKMapRect(
            x: min(a.x, b.x), y: min(a.y, b.y), width: abs(a.x - b.x), height: abs(a.y - b.y))
        let least = MKMapPointsPerMeterAtLatitude((north + south) / 2) * 600
        r = r.insetBy(dx: -max(r.width * 0.18, least), dy: -max(r.height * 0.22, least))
        return r
    }
}

/// The map of a map block: its route's legs in their lines' colors, walks
/// dotted, and its places pinned and numbered, on Apple's map; it moves
/// only where it is interactive, filling the screen.
struct MapCanvas: View {
    let look: MapLook
    let interactive: Bool
    @Environment(\.palette) private var palette
    @State private var position: MapCameraPosition

    init(look: MapLook, interactive: Bool) {
        self.look = look
        self.interactive = interactive
        _position = State(initialValue: .rect(look.rect))
    }

    var body: some View {
        Map(
            position: $position,
            interactionModes: interactive ? [.pan, .zoom, .rotate] : []
        ) {
            ForEach(Array(look.legs.enumerated()), id: \.offset) { _, leg in
                MapPolyline(coordinates: leg.coordinates)
                    .stroke(
                        leg.walk ? Color.gray : color(leg.color),
                        style: StrokeStyle(
                            lineWidth: leg.walk ? 3 : 5, lineCap: .round, lineJoin: .round,
                            dash: leg.walk ? [1, 6] : []))
            }
            ForEach(Array(look.places.enumerated()), id: \.offset) { i, place in
                Annotation(place.name, coordinate: place.coordinate) {
                    Text("\(i + 1)")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(.white)
                        .frame(width: 22, height: 22)
                        .background(palette.accent, in: .circle)
                        .overlay(Circle().stroke(.white, lineWidth: 2))
                        .shadow(radius: 1.5)
                }
            }
        }
        .mapStyle(.standard(pointsOfInterest: .excludingAll))
        .mapControls {
            if interactive {
                MapCompass()
                MapScaleView()
            }
        }
        // A map that changes, edited or written again in an answer, shows
        // all of itself anew.
        .onChange(of: look) { _, now in position = .rect(now.rect) }
    }

    private func color(_ hex: String) -> Color {
        hex.isEmpty ? palette.accent : (UIColor(hex: hex).map(Color.init) ?? palette.accent)
    }
}

/// A map block: its map, still in the page, its title and its route in
/// words; a tap fills the screen with it, to move about in, and Open in
/// Maps opens its route in Apple's Maps.
struct MapBlockView: View {
    let node: DocNode
    var onHeight: (@MainActor (CGSize) -> Void)?
    @Environment(\.palette) private var palette
    @Environment(\.openURL) private var openURL
    @State private var full = false

    static let mapHeight: CGFloat = 240

    var body: some View {
        let look = MapLook.of(node)
        VStack(alignment: .leading, spacing: 0) {
            if let look {
                MapCanvas(look: look, interactive: false)
                    .frame(height: Self.mapHeight)
                    .clipShape(.rect(cornerRadius: 7))
                    .contentShape(.rect)
                    .onTapGesture { full = true }
                    .accessibilityAddTraits(.isButton)
                    .accessibilityLabel(look.title.isEmpty ? L.hostSlashMap : look.title)
                    .overlay(alignment: .topTrailing) {
                        if let url = URL(string: look.apple), !look.apple.isEmpty {
                            Button(L.mapOpenInMaps, systemImage: "arrow.up.forward.app") {
                                openURL(url)
                            }
                            .labelStyle(.iconOnly)
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                            .padding(8)
                        }
                    }
                MapCaption(look: look)
            }
        }
        .padding(5)
        .background(palette.surface, in: .rect(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.divider))
        .fixedSize(horizontal: false, vertical: true)
        .onGeometryChange(for: CGSize.self) {
            $0.size
        } action: {
            onHeight?(CGSize(width: $0.width, height: $0.height + 2))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .fullScreenCover(isPresented: $full) {
            if let look { FullMap(look: look) }
        }
    }
}

/// A map's title, its route in a line and its legs in another, and whose
/// data it shows, a link to the page that credits it.
struct MapCaption: View {
    let look: MapLook
    @Environment(\.palette) private var palette

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            if !look.title.isEmpty {
                Text(look.title).font(.subheadline.weight(.semibold)).foregroundStyle(palette.text)
            }
            if !look.summary.isEmpty {
                Text(look.summary).font(.footnote.weight(.medium)).foregroundStyle(
                    palette.textSecondary)
                Text(look.steps).font(.footnote).foregroundStyle(palette.textMuted)
            }
            if let url = URL(string: look.sources) {
                Link(look.credit, destination: url)
                    .font(.caption2)
                    .foregroundStyle(palette.textMuted)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 6)
        .padding(.top, 8)
        .padding(.bottom, 4)
    }
}

/// A map filling the screen, to move about in, with its route in words.
struct FullMap: View {
    let look: MapLook
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @Environment(\.palette) private var palette

    var body: some View {
        NavigationStack {
            MapCanvas(look: look, interactive: true)
                .ignoresSafeArea(edges: .bottom)
                .safeAreaInset(edge: .bottom) {
                    if !look.summary.isEmpty {
                        MapCaption(look: look)
                            .padding(10)
                            .background(.bar, in: .rect(cornerRadius: 14))
                            .padding(.horizontal, 12)
                    }
                }
                .navigationTitle(look.title.isEmpty ? L.hostSlashMap : look.title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button(L.commonClose) { dismiss() }
                    }
                    if let url = URL(string: look.apple), !look.apple.isEmpty {
                        ToolbarItem(placement: .primaryAction) {
                            Button(L.mapOpenInMaps, systemImage: "arrow.up.forward.app") {
                                openURL(url)
                            }
                        }
                    }
                }
        }
    }
}
