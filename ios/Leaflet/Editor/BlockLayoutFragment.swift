import SwiftUI
import UIKit

/// BlockLayoutFragment draws what a block shows besides its text, in the
/// room EditorStyle leaves before it: a list's bullet, number or checkbox,
/// a toggle's chevron, a quote's bar, and the card of a callout or code.
final class BlockLayoutFragment: NSTextLayoutFragment {
    var palette: Palette?

    /// attributes are the paragraph's first, read once: a fragment draws
    /// one paragraph as it is, TextKit making another for a changed one,
    /// and reading them is costly enough to slow scrolling.
    private lazy var attributes: [NSAttributedString.Key: Any] = {
        guard let p = textElement as? NSTextParagraph, p.attributedString.length > 0 else {
            return [:]
        }
        return p.attributedString.attributes(at: 0, effectiveRange: nil)
    }()

    private lazy var info: BlockInfo? = attributes[.leafletBlock] as? BlockInfo

    /// look is a callout's tint, border, icon color and icon for its tone,
    /// as the desktop's CalloutLook: an unknown tone is info.
    static func look(_ tone: String?, _ p: Palette) -> (
        bg: Color, border: Color, fg: Color, symbol: String
    ) {
        switch tone?.trimmingCharacters(in: .whitespaces).lowercased() {
        case "tip": (p.okBg, p.okBorder, p.okText, "lightbulb")
        case "warning": (p.warnBg, p.warnBorder, p.warnText, "exclamationmark.triangle")
        case "danger": (p.dangerBg, p.dangerBorder, p.dangerText, "exclamationmark.octagon")
        default: (p.accentSoft, p.accentSoftBorder, p.accentText, "info.circle")
        }
    }

    /// cardPadding is the room a callout's or code's card leaves above and
    /// below its text; EditorStyle spaces those blocks to keep cards apart.
    static let cardPadding: CGFloat = 10
    /// codeBar is the height of a code card's bar above its code: its
    /// language and a button that copies it, as the desktop's.
    static let codeBar: CGFloat = 28

    /// columnWidth is the text column's width, which cards span.
    private var columnWidth: CGFloat {
        textLayoutManager?.textContainer?.size.width ?? layoutFragmentFrame.maxX
    }

    override var renderingSurfaceBounds: CGRect {
        // The markers and cards draw left of the text, from the page's
        // edge, and cards across the column, padded above and below.
        let b = super.renderingSurfaceBounds
        let left = layoutFragmentFrame.minX
        let pad = Self.cardPadding
        // A code card's bar stands above its code.
        let bar = info?.type == "code" ? Self.codeBar : 0
        // A heading's fold arrow stands in the page's margin, left of it.
        let margin: CGFloat = 24
        return b.union(
            CGRect(
                x: -left - margin, y: b.minY - pad - bar, width: columnWidth + 14 + margin,
                height: b.height + pad * 2 + bar))
    }

    /// Trailing is the page's last block when it is empty, which no
    /// character holds: TextKit lays it out as the last fragment's extra,
    /// empty line, where its marker goes.
    struct Trailing {
        let info: BlockInfo
        let font: UIFont
        let ordinal: Int
    }
    var trailing: Trailing?

    /// dateOffer is the to-do that offers a due date, which the caret is
    /// in, as the desktop's focused one.
    var dateOffer: String?

    /// dueChips are where the to-dos of the fragment show their due dates,
    /// or offer one, after their text: its own block's and a trailing
    /// to-do's. An offer that has no room on its line is left out; a date
    /// has room, which EditorStyle leaves at the ends of its lines.
    func dueChips() -> [(info: BlockInfo, rect: CGRect)] {
        var out: [(info: BlockInfo, rect: CGRect)] = []
        let right = columnWidth - layoutFragmentFrame.minX
        func add(_ info: BlockInfo, x: CGFloat, baseline: CGFloat, font: UIFont) {
            let due = Due(info)
            guard info.type == "todo_list", due != nil || dateOffer == info.id else { return }
            let size = DueChip.size(DueChip.label(info))
            let midY = baseline - font.capHeight / 2
            let rect = CGRect(
                x: x + DueChip.gap, y: midY - size.height / 2, width: size.width,
                height: size.height)
            if due == nil, rect.maxX > right { return }
            out.append((info, rect))
        }
        let font = attributes[.font] as? UIFont ?? .preferredFont(forTextStyle: .body)
        if let info, info.type == "todo_list", let p = textElement as? NSTextParagraph,
            let line = textLineFragments.last(where: { $0.characterRange.length > 0 })
                ?? textLineFragments.first
        {
            // After the text, without the paragraph's separator.
            var end = p.attributedString.length
            if end > 0, (p.attributedString.string as NSString).character(at: end - 1) == 0x0A {
                end -= 1
            }
            let at = line.locationForCharacter(at: max(end, line.characterRange.location))
            add(
                info, x: line.typographicBounds.minX + at.x,
                baseline: line.typographicBounds.minY + line.glyphOrigin.y, font: font)
        }
        if let trailing, textLineFragments.count > 1, let line = textLineFragments.last,
            line.characterRange.length == 0
        {
            add(
                trailing.info, x: EditorStyle.leading(trailing.info) - layoutFragmentFrame.minX,
                baseline: line.typographicBounds.minY + line.glyphOrigin.y, font: trailing.font)
        }
        return out
    }

    /// trailingLine is the empty line the trailing block is laid out on,
    /// after the paragraph's own.
    private var trailingLine: NSTextLineFragment? {
        guard trailing != nil, textLineFragments.count > 1, let line = textLineFragments.last,
            line.characterRange.length == 0
        else { return nil }
        return line
    }

    /// trailingBaseline is where the trailing block's line stands.
    var trailingBaseline: CGFloat? {
        trailingLine.map { $0.typographicBounds.minY + $0.glyphOrigin.y }
    }

    override func draw(at point: CGPoint, in context: CGContext) {
        let left = layoutFragmentFrame.minX
        if let info, let palette {
            let lines = ownLines
            let font = attributes[.font] as? UIFont ?? .preferredFont(forTextStyle: .body)
            Self.drawDecoration(
                info, palette, in: context, at: point, left: left, width: columnWidth,
                line: Line(
                    baseline: baseline, font: font,
                    ordinal: attributes[.leafletOrdinal] as? Int ?? 1,
                    top: lines.first?.typographicBounds.minY ?? 0,
                    bottom: lines.last?.typographicBounds.maxY ?? layoutFragmentFrame.height))
        }
        if let trailing, let palette, let line = trailingLine {
            Self.drawDecoration(
                trailing.info, palette, in: context, at: point, left: left, width: columnWidth,
                line: Line(
                    baseline: line.typographicBounds.minY + line.glyphOrigin.y, font: trailing.font,
                    ordinal: trailing.ordinal, top: line.typographicBounds.minY,
                    bottom: line.typographicBounds.maxY))
        }
        super.draw(at: point, in: context)
        if let palette {
            UIGraphicsPushContext(context)
            for chip in dueChips() {
                DueChip.draw(
                    chip.info, in: chip.rect.offsetBy(dx: point.x, dy: point.y), palette: palette)
            }
            UIGraphicsPopContext()
        }
    }

    /// markerRect is where a block's marker goes: in the room before its
    /// text, centered on its first line, or on the line at baseline.
    func markerRect(_ info: BlockInfo, baseline: CGFloat? = nil, font: UIFont? = nil) -> CGRect {
        Self.markerRect(
            info, left: layoutFragmentFrame.minX, baseline: baseline ?? self.baseline,
            font: font ?? attributes[.font] as? UIFont ?? .preferredFont(forTextStyle: .body))
    }

    /// markerRect is where a block's marker goes in a fragment whose left
    /// edge is at left in the text column.
    static func markerRect(_ info: BlockInfo, left: CGFloat, baseline: CGFloat, font: UIFont)
        -> CGRect
    {
        let lead = EditorStyle.leading(info) - left
        // A heading's fold arrow stands in the page's margin, which is
        // narrower than a list's room for its marker.
        let side = info.type == "heading" ? 20 : EditorStyle.marker
        // Centered on the first line's capitals, as the text's own marks
        // would be, whatever room the line spacing adds above them.
        let midY = baseline - font.capHeight / 2
        return CGRect(x: lead - side, y: midY - side / 2, width: side, height: side)
    }

    /// baseline is where the first line's text stands, in the fragment.
    var baseline: CGFloat {
        guard let line = textLineFragments.first else { return 17 }
        return line.typographicBounds.minY + line.glyphOrigin.y
    }

    /// codeBarParts are where a code card's bar shows its language, to
    /// choose another, and its copy button.
    static func codeBarParts(_ card: CGRect) -> (language: CGRect, copy: CGRect) {
        let row = CGRect(x: card.minX, y: card.minY + 4, width: card.width, height: codeBar)
        return (
            CGRect(
                x: row.minX + 14, y: row.minY, width: min(160, row.width / 2), height: row.height),
            CGRect(x: row.maxX - 40, y: row.minY, width: 32, height: row.height)
        )
    }

    /// ownLines are the paragraph's own lines, without the trailing
    /// block's.
    private var ownLines: [NSTextLineFragment] {
        var lines = textLineFragments
        if trailingLine != nil { lines.removeLast() }
        return lines
    }

    /// codeCard is a code block's card, in the fragment's coordinates.
    var codeCard: CGRect {
        let lines = ownLines
        let top = lines.first?.typographicBounds.minY ?? 0
        let bottom = lines.last?.typographicBounds.maxY ?? layoutFragmentFrame.height
        let depthX = CGFloat(info?.depth ?? 0) * EditorStyle.indent - layoutFragmentFrame.minX
        let right = columnWidth - layoutFragmentFrame.minX
        let pad = Self.cardPadding
        return CGRect(
            x: depthX, y: top - pad - Self.codeBar, width: max(right - depthX, 40),
            height: bottom - top + pad * 2 + Self.codeBar)
    }

    /// Line is where a block's text stands in a fragment: its first line's
    /// baseline, its font, the number a numbered item shows, and the top
    /// and bottom of its lines, which a quote's bar and a card go beside.
    struct Line {
        let baseline: CGFloat
        let font: UIFont
        let ordinal: Int
        let top: CGFloat
        let bottom: CGFloat
    }

    /// drawDecoration draws a block's marker or card, for a fragment drawn
    /// at point whose left edge is at left in a text column width wide: a
    /// paragraph's, or the empty page's (EditorTextView), which has none.
    static func drawDecoration(
        _ info: BlockInfo, _ palette: Palette, in ctx: CGContext, at point: CGPoint,
        left: CGFloat, width columnWidth: CGFloat, line: Line
    ) {
        let depthX = point.x + CGFloat(info.depth) * EditorStyle.indent - left
        var marker = markerRect(info, left: left, baseline: line.baseline, font: line.font)
        marker.origin.x += point.x
        marker.origin.y += point.y
        let font = line.font
        let baseline = line.baseline
        UIGraphicsPushContext(ctx)
        defer { UIGraphicsPopContext() }

        switch info.type {
        case "bulleted_list":
            let r: CGFloat = 3
            let c = CGPoint(x: marker.midX, y: marker.midY)
            let dot = CGRect(x: c.x - r, y: c.y - r, width: r * 2, height: r * 2)
            UIColor(palette.text).setFill()
            UIColor(palette.text).setStroke()
            switch info.depth % 3 {
            case 0: UIBezierPath(ovalIn: dot).fill()
            case 1:
                let ring = UIBezierPath(ovalIn: dot.insetBy(dx: 0.5, dy: 0.5))
                ring.lineWidth = 1.2
                ring.stroke()
            default: UIBezierPath(rect: dot.insetBy(dx: 0.4, dy: 0.4)).fill()
            }
        case "numbered_list":
            let n = line.ordinal
            let label = NSAttributedString(
                string: "\(n).",
                attributes: [
                    .font: font.withSize(font.pointSize * 0.95),
                    .foregroundColor: UIColor(palette.textSecondary),
                ])
            // The number stands on the text's baseline.
            let size = label.size()
            let numberFont = font.withSize(font.pointSize * 0.95)
            label.draw(
                at: CGPoint(
                    x: marker.maxX - size.width - 6, y: point.y + baseline - numberFont.ascender))
        case "todo_list":
            let name = info.checked ? "checkmark.square.fill" : "square"
            let config = UIImage.SymbolConfiguration(
                pointSize: font.pointSize * 0.95, weight: .regular)
            let tint = info.checked ? UIColor(palette.accent) : UIColor(palette.icon)
            if let img = UIImage(systemName: name, withConfiguration: config)?.withTintColor(tint) {
                img.draw(
                    at: CGPoint(
                        x: marker.midX - img.size.width / 2, y: marker.midY - img.size.height / 2))
            }
        case "toggle_list":
            let collapsed = info.data["collapsed"]?.bool ?? false
            let config = UIImage.SymbolConfiguration(
                pointSize: font.pointSize * 0.7, weight: .semibold)
            if let img = UIImage(
                systemName: collapsed ? "chevron.right" : "chevron.down", withConfiguration: config)?
                .withTintColor(UIColor(palette.icon))
            {
                img.draw(
                    at: CGPoint(
                        x: marker.midX - img.size.width / 2, y: marker.midY - img.size.height / 2))
            }
        case "heading":
            // Its fold arrow: plain while folded, faint while open, as
            // touch has no hover to show it on.
            let collapsed = info.data["collapsed"]?.bool ?? false
            let config = UIImage.SymbolConfiguration(
                pointSize: min(font.pointSize * 0.45, 12), weight: .semibold)
            let tint = UIColor(palette.icon).withAlphaComponent(collapsed ? 1 : 0.35)
            if let img = UIImage(
                systemName: collapsed ? "chevron.right" : "chevron.down", withConfiguration: config)?
                .withTintColor(tint)
            {
                img.draw(
                    at: CGPoint(
                        x: marker.midX - img.size.width / 2, y: marker.midY - img.size.height / 2))
            }
        case "quote":
            // Beside the text's lines, in the accent's soft border, as the
            // desktop's.
            let (top, bottom) = (line.top, line.bottom)
            UIColor(palette.accentSoftBorder).setFill()
            UIBezierPath(
                roundedRect: CGRect(
                    x: depthX + 2, y: point.y + top + 2, width: 3, height: bottom - top - 4),
                cornerRadius: 1.5
            )
            .fill()
        case "callout", "code":
            // Across the column, and around the text's own lines rather
            // than the paragraph's spacing, which keeps it apart from the
            // next block.
            let (top, bottom) = (line.top, line.bottom)
            let right = point.x - left + columnWidth
            let pad = Self.cardPadding
            let bar = info.type == "code" ? Self.codeBar : 0
            let card = CGRect(
                x: depthX, y: point.y + top - pad - bar, width: max(right - depthX, 40),
                height: bottom - top + pad * 2 + bar)
            let shape = UIBezierPath(roundedRect: card, cornerRadius: 8)
            if info.type == "code" {
                UIColor(palette.code).setFill()
                shape.fill()
                let bar = Self.codeBarParts(card)
                let muted = UIColor(palette.textMuted)
                // In the text size chosen in Settings, as far as the bar
                // has room.
                let labelFont = UIFontMetrics(forTextStyle: .caption1).scaledFont(
                    for: .systemFont(ofSize: 12), maximumPointSize: 17)
                let label = NSAttributedString(
                    string: CodeLanguage.name(info.data["language"]?.string ?? ""),
                    attributes: [.font: labelFont, .foregroundColor: muted])
                let size = label.size()
                label.draw(
                    at: CGPoint(x: bar.language.minX, y: bar.language.midY - size.height / 2))
                let small = UIImage.SymbolConfiguration(pointSize: 9, weight: .semibold)
                if let chevron = UIImage(systemName: "chevron.down", withConfiguration: small)?
                    .withTintColor(muted)
                {
                    chevron.draw(
                        at: CGPoint(
                            x: bar.language.minX + size.width + 4,
                            y: bar.language.midY - chevron.size.height / 2))
                }
                let copy = UIImage.SymbolConfiguration(pointSize: 12, weight: .regular)
                if let icon = UIImage(systemName: "doc.on.doc", withConfiguration: copy)?
                    .withTintColor(muted)
                {
                    icon.draw(
                        at: CGPoint(
                            x: bar.copy.midX - icon.size.width / 2,
                            y: bar.copy.midY - icon.size.height / 2))
                }
            } else {
                // Tinted by its tone, as the desktop's (CalloutLook).
                let look = Self.look(info.data["tone"]?.string, palette)
                UIColor(look.bg).setFill()
                shape.fill()
                UIColor(look.border).setStroke()
                let edge = UIBezierPath(
                    roundedRect: card.insetBy(dx: 0.5, dy: 0.5), cornerRadius: 8)
                edge.lineWidth = 1
                edge.stroke()
                let config = UIImage.SymbolConfiguration(
                    pointSize: font.pointSize * 0.85, weight: .medium)
                if let img = UIImage(systemName: look.symbol, withConfiguration: config)?
                    .withTintColor(UIColor(look.fg))
                {
                    img.draw(at: CGPoint(x: depthX + 14, y: marker.midY - img.size.height / 2))
                }
            }
        default:
            break
        }
    }
}
