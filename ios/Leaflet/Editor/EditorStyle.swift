import SwiftUI
import UIKit

/// EditorStyle is how the editor shows blocks: their fonts, indents and
/// colors, from the page text size and the palette. It sets the
/// attributes that show a paragraph from the ones that say what it is
/// (BlockInfo and the inline formats), which it never changes.
struct EditorStyle {
    var textSize: CGFloat
    var palette: Palette

    /// indent is how far each level of nesting goes in.
    static let indent: CGFloat = 26
    /// marker is the room a list's bullet, number or checkbox takes.
    static let marker: CGFloat = 26

    private var metrics: UIFontMetrics { UIFontMetrics(forTextStyle: .body) }

    func baseFont(_ info: BlockInfo) -> UIFont {
        let size: CGFloat
        var weight = UIFont.Weight.regular
        switch info.type {
        case "heading":
            // The desktop's sizes (internal/editor/layout.go).
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

    /// spacing is the room above and below a block, as the desktop's.
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

    /// leading is where a block's text starts, from the page's edge.
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
            // Lines as high as the desktop's: one and a half times the
            // text, or none extra for a heading; more for CJK text, whose
            // characters fill their square.
            let font = baseFont(info)
            let line = font.pointSize * (info.type == "heading" ? 1.25 : cjk ? 1.8 : 1.5)
            p.lineHeightMultiple = max(line / font.lineHeight, 1)
        }
        (p.paragraphSpacingBefore, p.paragraphSpacing) = Self.spacing(info)
        switch info.type {
        case "code", "callout":
            // Room for the card's padding (BlockLayoutFragment.cardPadding)
            // on each side, a code card's bar, and a gap between two cards.
            p.tailIndent = -14
            p.paragraphSpacingBefore = 16 + (info.type == "code" ? BlockLayoutFragment.codeBar : 0)
            p.paragraphSpacing = 18
        case "todo_list":
            // Room at the ends of its lines for its due date's chip
            // (DueChip), which goes after its text.
            p.tailIndent = -DueChip.room(info)
        default:
            break
        }
        return p
    }

    /// apply shows the paragraphs in range as their blocks and formats say.
    @MainActor
    func apply(to storage: NSMutableAttributedString, in range: NSRange) {
        let ns = storage.string as NSString
        guard storage.length > 0 else { return }
        let whole = ns.paragraphRange(
            for: NSIntersectionRange(range, NSRange(location: 0, length: ns.length)))
        var at = whole.location
        while at < NSMaxRange(whole) {
            let para = ns.paragraphRange(for: NSRange(location: at, length: 0))
            if let info = storage.attribute(.leafletBlock, at: para.location, effectiveRange: nil)
                as? BlockInfo
            {
                style(storage, para, info)
            }
            at = NSMaxRange(para)
            if para.length == 0 { break }
        }
    }

    @MainActor
    private func style(_ s: NSMutableAttributedString, _ para: NSRange, _ info: BlockInfo) {
        let base = baseFont(info)
        let ordinal = info.type == "numbered_list" ? Self.ordinal(s, at: para.location, info) : nil
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
        s.enumerateAttributes(in: para) { a, r, _ in
            var font = base
            var traits = font.fontDescriptor.symbolicTraits
            if a[.leafletBold] as? Bool == true { traits.insert(.traitBold) }
            if a[.leafletItalic] as? Bool == true { traits.insert(.traitItalic) }
            if a[.leafletCode] as? Bool == true {
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
            } else if a[.leafletCode] as? Bool == true {
                update[.backgroundColor] = UIColor(palette.code)
            } else if a[.leafletTinted] as? Bool == true {
                update[.backgroundColor] = UIColor(palette.agentSection)
            }
            // The lines under and through the text show its formats, a
            // link and a checked to-do: they are drawn here alone, so
            // what no longer asks for one loses it.
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
            // A link the page can open is one to VoiceOver and to a tap
            // on a page that can't be changed: a page's, or the web's.
            if let target = Self.linkTarget(a) { update[.link] = target }
            s.addAttributes(update, range: r)
            for key: NSAttributedString.Key in [
                .backgroundColor, .underlineStyle, .underlineColor, .strikethroughStyle,
                .strikethroughColor, .link,
            ] where update[key] == nil {
                s.removeAttribute(key, range: r)
            }
        }
        if info.type == "code", para.length > 0 {
            // A code block's text in its language's colors, its line
            // breaks read as the newlines they are.
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

    /// linkTarget is where text of attributes a links to, when it is a link
    /// the page opens: a page, by its mention or its leaflet:// link, or a
    /// web or mail link, as doc.SafeHref lets through. A link to a file or
    /// an app, as a shared page or an agent can write one, opens nothing.
    static func linkTarget(_ a: [NSAttributedString.Key: Any]) -> URL? {
        if let m = a[.leafletMention] as? Mention {
            return m.kind == "page" ? URL(string: "leaflet://page/\(m.id)") : nil
        }
        guard let href = a[.leafletHref] as? String,
            let url = URL(string: href.trimmingCharacters(in: .whitespaces))
        else { return nil }
        switch url.scheme?.lowercased() {
        case "http", "https", "mailto": return url
        case "leaflet": return url.host == "page" ? url : nil
        default: return nil
        }
    }

    /// mostlyCJK reports whether text is mostly Chinese, Japanese or
    /// Korean, whose lines the desktop sets higher: one of their
    /// characters counts as much as a word of four letters.
    static func mostlyCJK(_ text: String) -> Bool {
        var cjk = 0
        var other = 0
        for scalar in text.unicodeScalars {
            switch scalar.properties.generalCategory {
            case .otherLetter where Self.isCJK(scalar): cjk += 1
            case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter,
                .otherLetter:
                other += 1
            default: break
            }
        }
        return cjk > 0 && cjk * 4 >= other
    }

    private static func isCJK(_ s: Unicode.Scalar) -> Bool {
        switch s.value {
        case 0x3040...0x30FF, 0x3400...0x4DBF, 0x4E00...0x9FFF, 0xF900...0xFAFF,
            0xAC00...0xD7AF, 0x1100...0x11FF, 0x3130...0x318F, 0x20000...0x2FA1F:
            true
        default: false
        }
    }

    /// ordinal is a numbered item's number: one more than the numbered
    /// item before it at its depth, with nothing shallower between.
    static func ordinal(_ s: NSAttributedString, at location: Int, _ info: BlockInfo) -> Int {
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
                n += 1
            }
            at = prev.location
        }
        return n
    }
}

extension UIColor {
    /// init(hex:) reads "#rrggbb" or "#rrggbbaa", as the core keeps colors.
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
