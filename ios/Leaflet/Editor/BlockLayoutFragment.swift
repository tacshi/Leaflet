import SwiftUI
import UIKit

final class BlockLayoutFragment: NSTextLayoutFragment {
    var palette: Palette?

    private lazy var attributes: [NSAttributedString.Key: Any] = {
        guard let p = textElement as? NSTextParagraph, p.attributedString.length > 0 else {
            return [:]
        }
        return p.attributedString.attributes(at: 0, effectiveRange: nil)
    }()

    private lazy var info: BlockInfo? = attributes[.leafletBlock] as? BlockInfo

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

    static let cardPadding: CGFloat = 10
    static let codeBar: CGFloat = 28

    private var columnWidth: CGFloat {
        textLayoutManager?.textContainer?.size.width ?? layoutFragmentFrame.maxX
    }

    override var renderingSurfaceBounds: CGRect {
        let b = super.renderingSurfaceBounds
        let left = layoutFragmentFrame.minX
        let pad = Self.cardPadding
        let bar = info?.type == "code" ? Self.codeBar : 0
        let margin: CGFloat = 24
        return b.union(
            CGRect(
                x: -left - margin, y: b.minY - pad - bar, width: columnWidth + 14 + margin,
                height: b.height + pad * 2 + bar))
    }

    struct Trailing {
        let info: BlockInfo
        let font: UIFont
        let ordinal: Int
    }
    var trailing: Trailing?

    var dateOffer: String?

    func dueChips() -> [(info: BlockInfo, rect: CGRect)] {
        chips(fitting: true)
    }

    var offerOverflows: Bool {
        let right = columnWidth - layoutFragmentFrame.minX
        return chips(fitting: false).contains { Due($0.info) == nil && $0.rect.maxX > right }
    }

    private func chips(fitting: Bool) -> [(info: BlockInfo, rect: CGRect)] {
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
            if fitting, due == nil, rect.maxX > right { return }
            out.append((info, rect))
        }
        let font = attributes[.font] as? UIFont ?? .preferredFont(forTextStyle: .body)
        if let info, info.type == "todo_list", let p = textElement as? NSTextParagraph,
            let line = textLineFragments.last(where: { $0.characterRange.length > 0 })
                ?? textLineFragments.first
        {
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

    private var trailingLine: NSTextLineFragment? {
        guard trailing != nil, textLineFragments.count > 1, let line = textLineFragments.last,
            line.characterRange.length == 0
        else { return nil }
        return line
    }

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
        drawFormulas(at: point, in: context)
        if let palette {
            UIGraphicsPushContext(context)
            for chip in dueChips() {
                DueChip.draw(
                    chip.info, in: chip.rect.offsetBy(dx: point.x, dy: point.y), palette: palette)
            }
            UIGraphicsPopContext()
        }
    }

    /// Draws each typeset formula where the first character of its source
    /// is, on that line's baseline.
    private func drawFormulas(at point: CGPoint, in context: CGContext) {
        guard let p = textElement as? NSTextParagraph else { return }
        let text = p.attributedString
        var found: [(at: Int, picture: MathPicture)] = []
        text.enumerateAttribute(
            .leafletMathPicture, in: NSRange(location: 0, length: text.length)
        ) { value, range, _ in
            if let picture = value as? MathPicture { found.append((range.location, picture)) }
        }
        guard !found.isEmpty else { return }
        UIGraphicsPushContext(context)
        defer { UIGraphicsPopContext() }
        for (at, picture) in found {
            guard
                let line = textLineFragments.first(where: {
                    NSLocationInRange(at, $0.characterRange)
                })
            else { continue }
            let x = line.typographicBounds.minX + line.locationForCharacter(at: at).x
            let baseline = line.typographicBounds.minY + line.glyphOrigin.y
            picture.draw(x: point.x + x, baseline: point.y + baseline)
        }
    }

    func markerRect(_ info: BlockInfo, baseline: CGFloat? = nil, font: UIFont? = nil) -> CGRect {
        Self.markerRect(
            info, left: layoutFragmentFrame.minX, baseline: baseline ?? self.baseline,
            font: font ?? attributes[.font] as? UIFont ?? .preferredFont(forTextStyle: .body))
    }

    static func markerRect(_ info: BlockInfo, left: CGFloat, baseline: CGFloat, font: UIFont)
        -> CGRect
    {
        let lead = EditorStyle.leading(info) - left
        let side = info.type == "heading" ? 20 : EditorStyle.marker
        let midY = baseline - font.capHeight / 2
        return CGRect(x: lead - side, y: midY - side / 2, width: side, height: side)
    }

    var baseline: CGFloat {
        guard let line = textLineFragments.first else { return 17 }
        return line.typographicBounds.minY + line.glyphOrigin.y
    }

    static func codeBarParts(_ card: CGRect) -> (language: CGRect, copy: CGRect) {
        let row = CGRect(x: card.minX, y: card.minY + 4, width: card.width, height: codeBar)
        return (
            CGRect(
                x: row.minX + 14, y: row.minY, width: min(160, row.width / 2), height: row.height),
            CGRect(x: row.maxX - 40, y: row.minY, width: 32, height: row.height)
        )
    }

    private var ownLines: [NSTextLineFragment] {
        var lines = textLineFragments
        if trailingLine != nil { lines.removeLast() }
        return lines
    }

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

    struct Line {
        let baseline: CGFloat
        let font: UIFont
        let ordinal: Int
        let top: CGFloat
        let bottom: CGFloat
    }

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
            let (top, bottom) = (line.top, line.bottom)
            UIColor(palette.accentSoftBorder).setFill()
            UIBezierPath(
                roundedRect: CGRect(
                    x: depthX + 2, y: point.y + top + 2, width: 3, height: bottom - top - 4),
                cornerRadius: 1.5
            )
            .fill()
        case "callout", "code":
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
