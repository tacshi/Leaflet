import LeafletCore
import SwiftUI
import UIKit

struct EditorStyle {
    var textSize: CGFloat
    var palette: Palette
    /// The selection while the page is being edited: a formula it is at
    /// shows its source.
    var mathOpen: NSRange?

    static let indent: CGFloat = 26
    static let marker: CGFloat = 26

    private var metrics: UIFontMetrics { UIFontMetrics(forTextStyle: .body) }

    func baseFont(_ info: BlockInfo) -> UIFont {
        let size: CGFloat
        var weight = UIFont.Weight.regular
        switch info.type {
        case "heading":
            weight = .semibold
            switch info.level {
            case 1: size = textSize * 1.75
            case 2: size = textSize * 1.4
            case 3: size = textSize * 1.18
            default: size = textSize * 1.05
            }
        case "code":
            return metrics.scaledFont(
                for: .monospacedSystemFont(ofSize: textSize * 0.85, weight: .regular))
        default:
            size = textSize
        }
        return metrics.scaledFont(
            for: font(family: Fonts.page, size: size, weight: weight, italic: info.type == "quote"))
    }

    static func spacing(_ info: BlockInfo) -> (top: CGFloat, bottom: CGFloat) {
        switch info.type {
        case "heading":
            switch info.level {
            case 1: (22, 6)
            case 2: (18, 4)
            case 3: (14, 2)
            default: (10, 2)
            }
        case "quote": (6, 6)
        case "bulleted_list", "numbered_list", "todo_list", "toggle_list": (2, 2)
        default: (3, 3)
        }
    }

    private func font(family: String, size: CGFloat, weight: UIFont.Weight, italic: Bool = false)
        -> UIFont
    {
        var traits: [UIFontDescriptor.TraitKey: Any] = [.weight: weight]
        if italic { traits[.symbolic] = UIFontDescriptor.SymbolicTraits.traitItalic.rawValue }
        let d = UIFontDescriptor(fontAttributes: [.family: family, .traits: traits])
        let f = UIFont(descriptor: d, size: size)
        return f.familyName == family ? f : .systemFont(ofSize: size, weight: weight)
    }

    static func leading(_ info: BlockInfo) -> CGFloat {
        var x = CGFloat(info.depth) * indent
        switch info.type {
        case "bulleted_list", "numbered_list", "todo_list", "toggle_list": x += marker
        case "quote": x += 16
        case "callout": x += 40
        case "code": x += 14
        default: break
        }
        return x
    }

    func paragraphStyle(_ info: BlockInfo, cjk: Bool = false) -> NSParagraphStyle {
        let p = NSMutableParagraphStyle()
        let lead = Self.leading(info)
        p.firstLineHeadIndent = lead
        p.headIndent = lead
        if info.type == "code" {
            p.lineHeightMultiple = 1.1
        } else {
            let font = baseFont(info)
            let line = font.pointSize * (info.type == "heading" ? 1.25 : cjk ? 1.8 : 1.5)
            p.lineHeightMultiple = max(line / font.lineHeight, 1)
        }
        (p.paragraphSpacingBefore, p.paragraphSpacing) = Self.spacing(info)
        switch info.type {
        case "code", "callout":
            p.tailIndent = -14
            p.paragraphSpacingBefore = 16 + (info.type == "code" ? BlockLayoutFragment.codeBar : 0)
            p.paragraphSpacing = 18
        case "todo_list":
            p.tailIndent = -DueChip.room(info)
        default:
            break
        }
        return p
    }

    /// The formula at a place in a paragraph, typeset: its whole run and its
    /// picture. Nil where its source shows instead: with the caret or the
    /// selection at it, and for what is no formula yet.
    @MainActor
    private func typeset(
        _ s: NSAttributedString, at location: Int, in para: NSRange, size: CGFloat, ink: UIColor
    ) -> (run: NSRange, picture: MathPicture)? {
        var run = NSRange()
        _ = s.attribute(.leafletMath, at: location, longestEffectiveRange: &run, in: para)
        if let open = mathOpen, open.location <= NSMaxRange(run),
            NSMaxRange(open) >= run.location
        {
            return nil
        }
        let tex = (s.string as NSString).substring(with: run)
        guard
            let picture = MathPictures.picture(
                tex, size: size, ink: ink, scale: max(UITraitCollection.current.displayScale, 2))
        else { return nil }
        return (run, picture)
    }

    @MainActor
    func apply(to storage: NSMutableAttributedString, in range: NSRange) {
        let ns = storage.string as NSString
        guard storage.length > 0 else { return }
        let whole = ns.paragraphRange(
            for: NSIntersectionRange(range, NSRange(location: 0, length: ns.length)))
        var at = whole.location
        var numbers: [Int: Int] = [:]
        while at < NSMaxRange(whole) {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            if let info = storage.attribute(.leafletBlock, at: para.location, effectiveRange: nil)
                as? BlockInfo
            {
                style(storage, para, info, &numbers)
            }
            at = NSMaxRange(para)
            if para.length == 0 { break }
        }
    }

    @MainActor
    private func style(
        _ s: NSMutableAttributedString, _ para: NSRange, _ info: BlockInfo,
        _ numbers: inout [Int: Int]
    ) {
        let base = baseFont(info)
        let ordinal =
            info.type == "numbered_list"
            ? Self.ordinal(s, at: para.location, info, known: numbers) : nil
        if let ordinal { numbers[para.location] = ordinal }
        s.addAttribute(
            .paragraphStyle,
            value: paragraphStyle(
                info, cjk: Self.mostlyCJK((s.string as NSString).substring(with: para))),
            range: para)
        if let ordinal {
            s.addAttribute(.leafletOrdinal, value: ordinal, range: para)
        } else {
            s.removeAttribute(.leafletOrdinal, range: para)
        }
        let checked = info.type == "todo_list" && info.checked
        var pictures: [(at: Int, picture: MathPicture)] = []
        s.enumerateAttributes(in: para) { a, r, _ in
            var font = base
            var traits = font.fontDescriptor.symbolicTraits
            if a[.leafletBold] as? Bool == true { traits.insert(.traitBold) }
            if a[.leafletItalic] as? Bool == true { traits.insert(.traitItalic) }
            let math = a[.leafletMath] as? Bool == true
            if a[.leafletCode] as? Bool == true || math {
                font = metrics.scaledFont(
                    for: .monospacedSystemFont(ofSize: textSize * 0.85, weight: .regular))
            } else if traits != font.fontDescriptor.symbolicTraits,
                let d = font.fontDescriptor.withSymbolicTraits(traits)
            {
                font = UIFont(descriptor: d, size: font.pointSize)
            }
            var color = UIColor(checked ? palette.textMuted : palette.text)
            if a[.leafletHref] != nil || a[.leafletMention] != nil {
                color = UIColor(palette.accentText)
            }
            if let hex = a[.leafletColor] as? String, let c = UIColor(hex: hex) { color = c }
            var update: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: color]
            if let hex = a[.leafletBackground] as? String, let c = UIColor(hex: hex) {
                update[.backgroundColor] = c
            } else if a[.leafletCode] as? Bool == true || math {
                update[.backgroundColor] = UIColor(palette.code)
            } else if a[.leafletTinted] as? Bool == true {
                update[.backgroundColor] = UIColor(palette.agentSection)
            }
            let link = a[.leafletHref] != nil
            if a[.leafletUnderline] as? Bool == true || link {
                update[.underlineStyle] = NSUnderlineStyle.single.rawValue
                if !(a[.leafletUnderline] as? Bool == true) {
                    update[.underlineColor] = UIColor(palette.accentText).withAlphaComponent(0.4)
                }
            }
            if a[.leafletStrike] as? Bool == true || checked {
                update[.strikethroughStyle] = NSUnderlineStyle.single.rawValue
                if checked { update[.strikethroughColor] = UIColor(palette.textMuted) }
            }
            if let target = Self.linkTarget(a) { update[.link] = target }
            if math, info.type != "code",
                let (run, picture) = typeset(
                    s, at: r.location, in: para, size: base.pointSize, ink: color)
            {
                // The source stays in the text, too small to see: its first
                // character takes the room the formula is drawn in.
                update[.font] = UIFont.systemFont(ofSize: 0.1)
                update[.foregroundColor] = UIColor.clear
                update[.backgroundColor] = nil
                if r.location == run.location { pictures.append((r.location, picture)) }
            }
            s.addAttributes(update, range: r)
            for key: NSAttributedString.Key in [
                .backgroundColor, .underlineStyle, .underlineColor, .strikethroughStyle,
                .strikethroughColor, .link, .kern, .leafletMathPicture,
            ] where update[key] == nil {
                s.removeAttribute(key, range: r)
            }
        }
        for (at, picture) in pictures {
            s.addAttributes(
                [.kern: picture.width, .leafletMathPicture: picture],
                range: NSRange(location: at, length: 1))
        }
        if info.type == "code", para.length > 0 {
            let code = (s.string as NSString).substring(with: para)
                .replacingOccurrences(of: String(lineBreak), with: "\n")
            let language = info.data["language"]?.string ?? ""
            for (range, color) in CodeHighlighter.spans(
                code, language: language, dark: palette.isDark)
            {
                s.addAttribute(
                    .foregroundColor, value: color,
                    range: NSRange(location: para.location + range.location, length: range.length))
            }
        }
    }

    static func linkTarget(_ a: [NSAttributedString.Key: Any]) -> URL? {
        if let m = a[.leafletMention] as? Mention {
            return m.kind == "page" ? URL(string: "leaflet://page/\(m.id)") : nil
        }
        guard let href = a[.leafletHref] as? String else { return nil }
        let target = MobileLinkTarget(href)
        return target.isEmpty ? nil : URL(string: target)
    }

    static func mostlyCJK(_ text: String) -> Bool { MobileMostlyCJK(text) }

    static func ordinal(
        _ s: NSAttributedString, at location: Int, _ info: BlockInfo, known: [Int: Int] = [:]
    ) -> Int {
        let ns = s.string as NSString
        var n = 1
        var at = location
        while at > 0 {
            let prev = ns.paragraphRange(for: NSRange(location: at - 1, length: 0))
            guard
                let p = s.attribute(.leafletBlock, at: prev.location, effectiveRange: nil)
                    as? BlockInfo
            else { break }
            if p.depth < info.depth { break }
            if p.depth == info.depth {
                guard p.type == "numbered_list" else { break }
                if let k = known[prev.location] { return n + k }
                n += 1
            }
            at = prev.location
        }
        return n
    }
}

extension UIColor {
    convenience init?(hex: String) {
        var s = hex
        if s.hasPrefix("#") { s.removeFirst() }
        guard s.count == 6 || s.count == 8, let v = UInt64(s, radix: 16) else { return nil }
        let rgba = s.count == 6 ? v << 8 | 0xFF : v
        self.init(
            red: CGFloat(rgba >> 24 & 0xFF) / 255, green: CGFloat(rgba >> 16 & 0xFF) / 255,
            blue: CGFloat(rgba >> 8 & 0xFF) / 255, alpha: CGFloat(rgba & 0xFF) / 255)
    }
}
