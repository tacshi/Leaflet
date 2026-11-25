import Highlightr
import LeafletCore
import UIKit

@MainActor
enum CodeHighlighter {
    enum Kind: Sendable { case keyword, type, function, constant, string, comment, tag }

    typealias Span = (range: NSRange, kind: Kind)

    /// Code up to this long, in UTF-16 units, is colored as it is styled. Longer code is
    /// colored off the main thread, keeping the colors it has until then, and code longer
    /// than `most` stays plain.
    static let quick = 4_000
    static let most = 100_000

    /// Posted with a code block's ID as its object once colors worked out off the main thread
    /// for it are kept: styled again, it takes them.
    static let colored = Notification.Name("leaflet.codeColored")

    nonisolated private static let kinds: [String: Kind] = [
        "a626a4": .keyword,
        "c18401": .type,
        "4078f2": .function,
        "0184bb": .constant,
        "986801": .constant,
        "50a14f": .string,
        "a0a1a7": .comment,
        "e45649": .tag,
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

    private static var highlighter: Highlightr? = {
        let h = Highlightr()
        _ = h?.setTheme(to: "atom-one-light")
        return h
    }()

    private struct Key: Hashable {
        let code: String
        let language: String
    }

    /// The colors of a code block's text: nil while they are worked out off the main thread,
    /// as for a block longer than `quick`, after which `colored` is posted for the block and
    /// they are given from then on.
    static func spans(_ code: String, language: String, dark: Bool, block: String = "")
        -> [(NSRange, UIColor)]?
    {
        let lang = language.lowercased()
        let length = (code as NSString).length
        guard length > 0, length <= most, !lang.isEmpty, languages.contains(lang) else {
            return []
        }
        let key = Key(code: code, language: lang)
        if let kept = made[key] { return painted(kept, dark: dark) }
        guard length > quick, !block.isEmpty else {
            let out = highlight(code, as: lang, with: highlighter)
            keep(out, for: key)
            return painted(out, dark: dark)
        }
        waiting[block] = key
        work()
        return nil
    }

    private static let languages = Set(highlighter?.supportedLanguages() ?? [])
    private static var made: [Key: [Span]] = [:]
    /// The newest code asked for of each block whose colors wait for the worker.
    private static var waiting: [String: Key] = [:]
    private static var working = false
    private static let worker = Worker()

    private static func painted(_ spans: [Span], dark: Bool) -> [(NSRange, UIColor)] {
        spans.map { ($0.range, color($0.kind, dark: dark)) }
    }

    private static func keep(_ spans: [Span], for key: Key) {
        if made.count >= 64 { made.removeAll() }
        made[key] = spans
    }

    private static func work() {
        guard !working, let (block, key) = waiting.first else { return }
        waiting[block] = nil
        guard made[key] == nil else {
            NotificationCenter.default.post(name: colored, object: block)
            return work()
        }
        working = true
        worker.highlight(key.code, as: key.language) { spans in
            Task { @MainActor in
                keep(spans, for: key)
                working = false
                NotificationCenter.default.post(name: colored, object: block)
                work()
            }
        }
    }

    nonisolated fileprivate static func highlight(
        _ code: String, as lang: String, with h: Highlightr?
    ) -> [Span] {
        guard let h, let marked = h.highlight(code, as: lang, fastRender: true),
            marked.length == (code as NSString).length
        else { return [] }
        var out: [Span] = []
        marked.enumerateAttribute(
            .foregroundColor, in: NSRange(location: 0, length: marked.length)
        ) { value, range, _ in
            if let c = value as? UIColor, let kind = kinds[hex(c)] { out.append((range, kind)) }
        }
        if codeLike.contains(lang) {
            out += names(code, besides: out)
        }
        return out
    }

    nonisolated private static let codeLike: Set<String> = [
        "c", "cpp", "csharp", "dart", "go", "java", "javascript", "kotlin", "lua", "php",
        "python", "r", "ruby", "rust", "scala", "swift", "typescript", "zig",
    ]

    nonisolated private static func names(_ code: String, besides colored: [Span]) -> [Span] {
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
        var out: [Span] = []
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
                let called = k < units.count && units[k] == 0x28
                let member = i > 0 && units[i - 1] == 0x2E
                let capital = u >= 0x41 && u <= 0x5A
                let kind: Kind? =
                    called ? .function : member ? .constant : capital ? .type : nil
                if let kind { out.append((NSRange(location: i, length: j - i), kind)) }
                i = j
                continue
            }
            if "=+-*/<>!&|?%^~".utf16.contains(u) {
                var j = i + 1
                while j < units.count, !taken[j], "=+-*/<>!&|?%^~".utf16.contains(units[j]) {
                    j += 1
                }
                out.append((NSRange(location: i, length: j - i), .keyword))
                i = j
                continue
            }
            i += 1
        }
        return out
    }

    nonisolated private static func hex(_ c: UIColor) -> String {
        var r: CGFloat = 0
        var g: CGFloat = 0
        var b: CGFloat = 0
        var a: CGFloat = 0
        c.getRed(&r, green: &g, blue: &b, alpha: &a)
        return String(
            format: "%02x%02x%02x", Int(round(r * 255)), Int(round(g * 255)), Int(round(b * 255)))
    }
}

/// Colors long code on a queue of its own, one block at a time, with a highlighter of its own:
/// a highlighter takes one thread at a time.
private final class Worker: @unchecked Sendable {
    private let queue = DispatchQueue(label: "app.capyvibe.leaflet.code", qos: .userInitiated)
    private var highlighter: Highlightr?

    func highlight(
        _ code: String, as language: String,
        done: @escaping @Sendable ([CodeHighlighter.Span]) -> Void
    ) {
        queue.async {
            if self.highlighter == nil {
                self.highlighter = Highlightr()
                _ = self.highlighter?.setTheme(to: "atom-one-light")
            }
            done(CodeHighlighter.highlight(code, as: language, with: self.highlighter))
        }
    }
}

struct CodeLanguage: Decodable, Sendable {
    let id: String
    let name: String

    private static let read: [CodeLanguage] =
        (try? JSONDecoder().decode([CodeLanguage].self, from: Data(MobileCodeLanguages().utf8)))
        ?? []

    /// The languages a code block can be in, by name in the app's language
    /// as it is now: none, plain text, has its name from the app's own
    /// strings, the others theirs.
    static var all: [CodeLanguage] {
        read.map { $0.id.isEmpty ? CodeLanguage(id: "", name: L.editorCodePlainText) : $0 }
    }

    static func name(_ id: String) -> String {
        id.isEmpty ? L.editorCodePlainText : read.first { $0.id == id }?.name ?? id
    }
}
