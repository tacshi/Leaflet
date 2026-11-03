import LeafletCore
import UIKit

/// BlockInfo is the block a paragraph of the editor is: its ID, type and
/// data besides its text, and how deep it is nested. Every paragraph
/// carries one, as the .leafletBlock attribute; a paragraph's blocks are
/// told apart by identity, so a changed block gets a new BlockInfo.
final class BlockInfo: NSObject, @unchecked Sendable {
    let id: String
    let type: String
    /// data is the block's data without its text (delta).
    let data: [String: JSONValue]
    /// depth is how many nestable blocks the block is nested in.
    let depth: Int

    init(id: String, type: String, data: [String: JSONValue] = [:], depth: Int = 0) {
        self.id = id
        self.type = type
        self.data = data
        self.depth = depth
    }

    /// with returns the block with a change.
    func with(
        id: String? = nil, type: String? = nil, data: [String: JSONValue]? = nil, depth: Int? = nil
    ) -> BlockInfo {
        BlockInfo(
            id: id ?? self.id, type: type ?? self.type, data: data ?? self.data,
            depth: depth ?? self.depth)
    }

    func setting(_ key: String, _ value: JSONValue?) -> BlockInfo {
        var d = data
        d[key] = value
        return with(data: d)
    }

    var level: Int { Int(data["level"]?.int ?? 1) }
    var checked: Bool { data["checked"]?.bool ?? false }

    /// isText reports whether the block holds text, as the core's
    /// doc.TextTypes.
    var isText: Bool { Self.textTypes.contains(type) }
    var isList: Bool { Self.nestable.contains(type) }

    /// textTypes are the types that hold text, and nestable those that
    /// hold nested blocks, as the core has them (mobile.BlockTypes), read
    /// once.
    static let textTypes = types.text
    static let nestable = types.nestable
    private static let types: (text: Set<String>, nestable: Set<String>) = {
        struct Types: Decodable {
            var text: [String]
            var nestable: [String]
        }
        let t = try? JSONDecoder().decode(Types.self, from: Data(MobileBlockTypes().utf8))
        return (Set(t?.text ?? []), Set(t?.nestable ?? []))
    }()

    /// newID returns a block ID as the core makes them.
    static func newID() -> String { MobileNewBlockID() }

    static func paragraph() -> BlockInfo { BlockInfo(id: newID(), type: "paragraph") }

    /// newLine is the block a new line after this one starts as, as the
    /// core's rules have it (mobile.NewLine): a list goes on as a list, a
    /// to-do unchecked, and anything else ends with the line, for a
    /// paragraph.
    func newLine() -> BlockInfo {
        let made = try? JSONDecoder().decode(NewBlock.self, from: Data(MobileNewLine(type).utf8))
        return BlockInfo(
            id: Self.newID(), type: made?.type ?? "paragraph", data: made?.data ?? [:],
            depth: depth)
    }
}

/// NewBlock is a new block's type and data, as the core's rules give them.
struct NewBlock: Decodable {
    var type: String?
    var data: [String: JSONValue]?
}

/// Mention is a mention of a person, an agent or a page in a block's text.
final class Mention: NSObject, @unchecked Sendable {
    let kind: String
    let id: String
    let name: String

    init(kind: String, id: String, name: String) {
        self.kind = kind
        self.id = id
        self.name = name
    }

    override func isEqual(_ object: Any?) -> Bool {
        guard let o = object as? Mention else { return false }
        return o.kind == kind && o.id == id && o.name == name
    }

    override var hash: Int { id.hashValue }
}

extension NSAttributedString.Key {
    /// leafletBlock is the BlockInfo of the paragraph.
    static let leafletBlock = NSAttributedString.Key("leaflet.block")
    // The inline formats, kept apart from the fonts and colors that show
    // them, so that they read back as they were.
    static let leafletBold = NSAttributedString.Key("leaflet.bold")
    static let leafletItalic = NSAttributedString.Key("leaflet.italic")
    static let leafletCode = NSAttributedString.Key("leaflet.code")
    /// leafletUnderline and leafletStrike are the text's own underline and
    /// strikethrough: the style draws a link's underline and a checked
    /// to-do's line through too, which are not the text's.
    static let leafletUnderline = NSAttributedString.Key("leaflet.underline")
    static let leafletStrike = NSAttributedString.Key("leaflet.strike")
    static let leafletHref = NSAttributedString.Key("leaflet.href")
    static let leafletMention = NSAttributedString.Key("leaflet.mention")
    /// leafletColor and leafletBackground are the text's color and
    /// highlight, as hex ("#rrggbbaa").
    static let leafletColor = NSAttributedString.Key("leaflet.color")
    static let leafletBackground = NSAttributedString.Key("leaflet.background")
    /// leafletOrdinal is a numbered item's number, as the editor counts it.
    static let leafletOrdinal = NSAttributedString.Key("leaflet.ordinal")
    /// leafletTinted marks the blocks an agent run last changed.
    static let leafletTinted = NSAttributedString.Key("leaflet.tinted")
}

/// lineBreak is how a line break inside a block is kept in the editor's
/// text: a line separator, which TextKit doesn't take for the end of a
/// paragraph.
let lineBreak: Character = "\u{2028}"
