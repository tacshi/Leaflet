import LeafletCore
import SwiftUI

/// A card block as the app shows it, read by the Go core: its facts'
/// values and its note as deltas, and its sources by name and site.
struct CardLook: Decodable, Equatable, Sendable {
    struct Fact: Decodable, Equatable, Sendable {
        let label: String
        let separator: String
        let value: [JSONValue]
    }

    struct Source: Decodable, Equatable, Sendable {
        let url: String
        let name: String
        let site: String
    }

    let layout: String
    let icon: String
    let title: String
    let image: String
    let facts: [Fact]
    let note: [JSONValue]
    let sources: [Source]

    @MainActor private static var read = NodeCache<CardLook>(most: 256)

    @MainActor static func of(_ node: DocNode) -> CardLook? {
        read.value(of: node) { try? coreValue($0, as: CardLook.self, MobileCardView) }
    }

    /// The text of a delta, with its marks, as a page shows it.
    @MainActor static func text(_ delta: [JSONValue], palette: Palette) -> AttributedString {
        MarkdownPreview.text(delta, palette: palette)
    }

    static func plain(_ delta: [JSONValue]) -> String {
        delta.compactMap { $0["insert"]?.string }.joined()
    }
}

/// A card block: facts at a glance under a title with an icon, as figures
/// side by side or as details beside a picture, then a note and a chip for
/// the pages they come from.
struct FactCardView: View {
    let node: DocNode
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// Whether the card fetches and shows its picture: never for one that
    /// anyone may have written, as in a comment, whose picture's link could
    /// tell its writer who reads it.
    var showsPicture = true
    @Environment(\.palette) private var palette
    @Environment(\.displayScale) private var scale
    @State private var picture: UIImage?
    @ScaledMetric(relativeTo: .body) private var figureSize: CGFloat = 26

    var body: some View {
        let look = CardLook.of(node)
        let image = showsPicture && look?.layout == "details" ? look?.image ?? "" : ""
        VStack(alignment: .leading, spacing: 10) {
            if let look {
                if !look.title.isEmpty || !look.icon.isEmpty {
                    HStack(spacing: 8) {
                        if !look.icon.isEmpty {
                            Image(systemName: Lucide.symbol(look.icon))
                                .foregroundStyle(palette.textSecondary)
                                .accessibilityHidden(true)
                        }
                        Text(look.title).font(.headline).foregroundStyle(palette.text)
                            .accessibilityAddTraits(.isHeader)
                    }
                }
                if look.layout == "figures" {
                    figures(look)
                    note(look)
                } else {
                    details(look)
                }
            }
        }
        .padding(.vertical, 14)
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.surface, in: .rect(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.divider))
        .fixedSize(horizontal: false, vertical: true)
        .onGeometryChange(for: CGSize.self) {
            $0.size
        } action: {
            onHeight?(CGSize(width: $0.width, height: $0.height + 2))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        // Its title, each fact, its note and its sources' chip, which opens
        // them, each its own to VoiceOver.
        .accessibilityElement(children: .contain)
        .task(id: image) {
            await CardPictures.load(image, scale: scale) { picture = $0 }
        }
    }

    private func figures(_ look: CardLook) -> some View {
        SpreadRows(spacing: 24, rowSpacing: 10) {
            ForEach(Array(look.facts.enumerated()), id: \.offset) { _, f in
                VStack(alignment: .leading, spacing: 2) {
                    if !f.label.isEmpty {
                        Text(f.label).font(.footnote).foregroundStyle(palette.textMuted)
                    }
                    Text(CardLook.text(f.value, palette: palette))
                        .font(.system(size: figureSize, weight: .bold))
                        .foregroundStyle(palette.text)
                        .minimumScaleFactor(0.6)
                        .lineLimit(2)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    private func details(_ look: CardLook) -> some View {
        HStack(alignment: .top, spacing: 14) {
            if let picture {
                Image(uiImage: picture).resizable().scaledToFill()
                    .frame(width: 110, height: 138)
                    .clipShape(.rect(cornerRadius: 12))
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(look.facts.enumerated()), id: \.offset) { _, f in
                    Text(
                        AttributedString(f.label + f.separator)
                            + CardLook.text(f.value, palette: palette)
                    )
                    .font(.subheadline)
                    .foregroundStyle(palette.text)
                    .fixedSize(horizontal: false, vertical: true)
                }
                note(look)
            }
        }
    }

    @ViewBuilder private func note(_ look: CardLook) -> some View {
        if !look.note.isEmpty || !look.sources.isEmpty {
            SpreadRows(spacing: 6, rowSpacing: 4, spread: false) {
                if !look.note.isEmpty {
                    Text(CardLook.text(look.note, palette: palette))
                        .font(.caption)
                        .foregroundStyle(palette.textMuted)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !look.sources.isEmpty {
                    SourceChip(sources: look.sources)
                }
            }
        }
    }
}

/// The chip naming a card's first source and how many more there are: a
/// tap opens the one, or a menu of them all.
struct SourceChip: View {
    let sources: [CardLook.Source]
    @Environment(\.palette) private var palette
    @Environment(\.openURL) private var openURL

    var body: some View {
        if sources.count == 1, let only = sources.first, let url = URL(string: only.url) {
            Button {
                openURL(url)
            } label: {
                chip
            }
            .buttonStyle(.plain)
            .accessibilityLabel(L.cardSources + ": " + only.name)
        } else {
            Menu {
                // By place, as a card may name a page twice.
                ForEach(Array(sources.enumerated()), id: \.offset) { _, s in
                    if let url = URL(string: s.url) {
                        Button {
                            openURL(url)
                        } label: {
                            Text(s.name)
                            Text(s.site)
                        }
                    }
                }
            } label: {
                chip
            }
            .accessibilityLabel(L.cardSources + ": " + sources.map(\.name).joined(separator: ", "))
        }
    }

    private var chip: some View {
        HStack(spacing: 4) {
            Text(sources.first?.name ?? "").lineLimit(1).truncationMode(.tail)
                .frame(maxWidth: 140, alignment: .leading)
                .fixedSize(horizontal: true, vertical: false)
            if sources.count > 1 { Text("+\(sources.count - 1)") }
        }
        .font(.caption2)
        .foregroundStyle(palette.textMuted)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(palette.ground, in: .capsule)
    }
}

/// Rows of views as wide as they ask, wrapping where they would not fit;
/// spread, those of a row are spread across it, the first at its start and
/// the last at its end, as a card's figures are.
struct SpreadRows: Layout {
    var spacing: CGFloat
    var rowSpacing: CGFloat
    var spread = true

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Void)
        -> CGSize
    {
        let width = proposal.width ?? .infinity
        let rows = Self.rows(subviews, width: width, spacing: spacing)
        var height: CGFloat = 0
        var widest: CGFloat = 0
        for row in rows {
            let sizes = row.map { Self.size(subviews[$0], in: width) }
            height += sizes.map(\.height).max() ?? 0
            widest = max(
                widest, sizes.map(\.width).reduce(0, +) + spacing * CGFloat(max(row.count - 1, 0)))
        }
        height += rowSpacing * CGFloat(max(rows.count - 1, 0))
        return CGSize(width: width.isFinite ? width : widest, height: height)
    }

    func placeSubviews(
        in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Void
    ) {
        var y = bounds.minY
        for row in Self.rows(subviews, width: bounds.width, spacing: spacing) {
            let sizes = row.map { Self.size(subviews[$0], in: bounds.width) }
            let used = sizes.map(\.width).reduce(0, +)
            let gap =
                spread && row.count > 1
                ? max((bounds.width - used) / CGFloat(row.count - 1), spacing) : spacing
            var x = bounds.minX
            for (k, i) in row.enumerated() {
                subviews[i].place(
                    at: CGPoint(x: x, y: y), anchor: .topLeading,
                    proposal: ProposedViewSize(width: sizes[k].width, height: sizes[k].height))
                x += sizes[k].width + gap
            }
            y += (sizes.map(\.height).max() ?? 0) + rowSpacing
        }
    }

    /// The size a view takes in a row width wide: as wide as it asks, at
    /// most the row, and as tall as it is at that width.
    private static func size(_ view: LayoutSubview, in width: CGFloat) -> CGSize {
        let w = min(view.sizeThatFits(.unspecified).width, width)
        return CGSize(
            width: w, height: view.sizeThatFits(ProposedViewSize(width: w, height: nil)).height)
    }

    private static func rows(_ subviews: Subviews, width: CGFloat, spacing: CGFloat) -> [[Int]] {
        var rows: [[Int]] = []
        var row: [Int] = []
        var used: CGFloat = 0
        for i in subviews.indices {
            let w = min(subviews[i].sizeThatFits(.unspecified).width, width)
            if !row.isEmpty, used + spacing + w > width {
                rows.append(row)
                row = []
                used = 0
            }
            used += row.isEmpty ? w : spacing + w
            row.append(i)
        }
        if !row.isEmpty { rows.append(row) }
        return rows
    }
}
