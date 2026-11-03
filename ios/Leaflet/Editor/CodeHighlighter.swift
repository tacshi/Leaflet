import Highlightr
import LeafletCore
import UIKit

/// CodeHighlighter colors a code block's text by its language with
/// Highlightr (highlight.js), in the desktop's code colors (MyGo's, after
/// GitHub's Primer). Highlightr takes only the themes it bundles, and
/// its GitHub one is an old one, so it highlights with Atom One Light,
/// which gives each kind of token a color of its own, and each of those
/// becomes the desktop's color for that kind. Only colors are taken: the
/// text keeps the editor's font, and plain text the page's color.
@MainActor
enum CodeHighlighter {
    enum Kind { case keyword, type, function, constant, string, comment, tag }

    /// kinds are Atom One Light's colors, as hex, and the kind of token
    /// each colors (its CSS's classes).
    private static let kinds: [String: Kind] = [
        "a626a4": .keyword,  // keyword, doctag, formula
        "c18401": .type,  // built_in, class title
        "4078f2": .function,  // title, meta, symbol, bullet
        "0184bb": .constant,  // literal
        "986801": .constant,  // number, attr, type, variable
        "50a14f": .string,  // string, regexp, attribute, addition
        "a0a1a7": .comment,  // comment, quote
        "e45649": .tag,  // name, selector-tag, section, deletion
    ]

    static func color(_ kind: Kind, dark: Bool) -> UIColor {
        let hex: String
        switch kind {
        case .keyword: hex = dark ? "#ff7b72" : "#cf222e"
        case .type: hex = dark ? "#ffa657" : "#953800"
        case .function: hex = dark ? "#d2a8ff" : "#8250df"
        case .constant: hex = dark ? "#79c0ff" : "#0550ae"
        case .string: hex = dark ? "#a5d6ff" : "#0a3069"
        case .comment: hex = dark ? "#8b949e" : "#6e7781"
        case .tag: hex = dark ? "#7ee787" : "#116329"
        }
        return UIColor(hex: hex)!
    }

    /// highlighter is made once, as it loads highlight.js.
    private static var highlighter: Highlightr? = {
        let h = Highlightr()
        _ = h?.setTheme(to: "atom-one-light")
        return h
    }()

    /// spans returns the colored ranges of code, in its UTF-16 offsets,
    /// with their colors. Code of no language, or one highlight.js doesn't
    /// know, stays plain, as on the desktop.
    static func spans(_ code: String, language: String, dark: Bool) -> [(NSRange, UIColor)] {
        let lang = language.lowercased()
        guard !code.isEmpty, !lang.isEmpty, languages.contains(lang) else { return [] }
        let key = "\(dark ? 1 : 0)\(lang)\u{0}\(code)"
        if let kept = made[key] { return kept }
        let out = highlight(code, as: lang, dark: dark)
        // A few blocks' worth: those styled again as the page changes
        // around them, each of which highlight.js reads whole.
        if made.count >= 64 { made.removeAll() }
        made[key] = out
        return out
    }

    /// languages are those highlight.js knows, asked of it once, as each
    /// asking goes through JavaScriptCore.
    private static let languages = Set(highlighter?.supportedLanguages() ?? [])
    /// made are the spans of the code highlighted lately, by its look,
    /// language and text.
    private static var made: [String: [(NSRange, UIColor)]] = [:]

    private static func highlight(_ code: String, as lang: String, dark: Bool) -> [(
        NSRange, UIColor
    )] {
        guard let h = highlighter,
            let colored = h.highlight(code, as: lang, fastRender: true),
            colored.length == (code as NSString).length
        else { return [] }
        var out: [(NSRange, UIColor)] = []
        colored.enumerateAttribute(
            .foregroundColor, in: NSRange(location: 0, length: colored.length)
        ) { value, range, _ in
            if let c = value as? UIColor, let kind = kinds[hex(c)] {
                out.append((range, color(kind, dark: dark)))
            }
        }
        if codeLike.contains(lang) {
            out += names(code, besides: out, dark: dark)
        }
        return out
    }

    /// codeLike are the languages whose names and operators are colored
    /// as the desktop's lexers color them.
    private static let codeLike: Set<String> = [
        "c", "cpp", "csharp", "dart", "go", "java", "javascript", "kotlin", "lua", "php",
        "python", "r", "ruby", "rust", "scala", "swift", "typescript", "zig",
    ]

    /// names colors what highlight.js leaves plain and the desktop's
    /// lexers don't: a name called as a function, a member after a dot, a
    /// capitalized name as a type, and operators, outside the ranges
    /// colored already (strings, comments, keywords).
    private static func names(_ code: String, besides colored: [(NSRange, UIColor)], dark: Bool)
        -> [(NSRange, UIColor)]
    {
        let units = Array(code.utf16)
        var taken = [Bool](repeating: false, count: units.count)
        for (r, _) in colored {
            for i in r.location..<min(NSMaxRange(r), units.count) { taken[i] = true }
        }
        func isIdent(_ u: UInt16, first: Bool) -> Bool {
            switch u {
            case 0x41...0x5A, 0x61...0x7A, 0x5F, 0x24: true
            case 0x30...0x39: !first
            default: false
            }
        }
        var out: [(NSRange, UIColor)] = []
        var i = 0
        while i < units.count {
            let u = units[i]
            if taken[i] {
                i += 1
                continue
            }
            if isIdent(u, first: true) {
                var j = i + 1
                while j < units.count, isIdent(units[j], first: false), !taken[j] { j += 1 }
                var k = j
                while k < units.count, units[k] == 0x20 || units[k] == 0x09 { k += 1 }
                let called = k < units.count && units[k] == 0x28  // (
                let member = i > 0 && units[i - 1] == 0x2E  // .
                let capital = u >= 0x41 && u <= 0x5A
                let kind: Kind? =
                    called ? .function : member ? .constant : capital ? .type : nil
                if let kind {
                    out.append((NSRange(location: i, length: j - i), color(kind, dark: dark)))
                }
                i = j
                continue
            }
            if "=+-*/<>!&|?%^~".utf16.contains(u) {
                var j = i + 1
                while j < units.count, !taken[j], "=+-*/<>!&|?%^~".utf16.contains(units[j]) {
                    j += 1
                }
                out.append((NSRange(location: i, length: j - i), color(.keyword, dark: dark)))
                i = j
                continue
            }
            i += 1
        }
        return out
    }

    private static func hex(_ c: UIColor) -> String {
        var r: CGFloat = 0
        var g: CGFloat = 0
        var b: CGFloat = 0
        var a: CGFloat = 0
        c.getRed(&r, green: &g, blue: &b, alpha: &a)
        return String(
            format: "%02x%02x%02x", Int(round(r * 255)), Int(round(g * 255)), Int(round(b * 255)))
    }
}

/// CodeLanguage is a language a code block's bar offers, as the core lists
/// them for the desktop's bar.
struct CodeLanguage: Decodable, Sendable {
    let id: String
    let name: String

    static let all: [CodeLanguage] =
        (try? JSONDecoder().decode([CodeLanguage].self, from: Data(MobileCodeLanguages().utf8)))
        ?? []

    /// name is a language's name, or its ID when the core doesn't offer
    /// it, as a fence may name any.
    static func name(_ id: String) -> String {
        all.first { $0.id == id }?.name ?? id
    }
}
