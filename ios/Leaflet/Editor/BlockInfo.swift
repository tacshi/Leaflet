import LeafletCore
import UIKit

final class BlockInfo: NSObject, @unchecked Sendable {
    let id: String
    let type: String
    let data: [String: JSONValue]
    let depth: Int

    init(id: String, type: String, data: [String: JSONValue] = [:], depth: Int = 0) {
        self.id = id
        self.type = type
        self.data = data
        self.depth = depth
    }

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
    var isCollapsed: Bool { data["collapsed"]?.bool == true }

    var isText: Bool { Self.textTypes.contains(type) }
    var isList: Bool { Self.nestable.contains(type) }

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

    static func newID() -> String { MobileNewBlockID() }

    static func paragraph(depth: Int = 0) -> BlockInfo {
        BlockInfo(id: newID(), type: "paragraph", depth: depth)
    }

    func newLine() -> BlockInfo {
        let made = try? JSONDecoder().decode(NewBlock.self, from: Data(MobileNewLine(type).utf8))
        return BlockInfo(
            id: Self.newID(), type: made?.type ?? "paragraph", data: made?.data ?? [:],
            depth: depth)
    }
}

struct NewBlock: Decodable {
    var type: String?
    var data: [String: JSONValue]?
}

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
    static let leafletBlock = NSAttributedString.Key("leaflet.block")
    static let leafletBold = NSAttributedString.Key("leaflet.bold")
    static let leafletItalic = NSAttributedString.Key("leaflet.italic")
    static let leafletCode = NSAttributedString.Key("leaflet.code")
    /// A formula in the line: the run's text is its TeX source.
    static let leafletMath = NSAttributedString.Key("leaflet.math")
    static let leafletUnderline = NSAttributedString.Key("leaflet.underline")
    static let leafletStrike = NSAttributedString.Key("leaflet.strike")
    static let leafletHref = NSAttributedString.Key("leaflet.href")
    static let leafletMention = NSAttributedString.Key("leaflet.mention")
    static let leafletColor = NSAttributedString.Key("leaflet.color")
    static let leafletBackground = NSAttributedString.Key("leaflet.background")
    static let leafletOrdinal = NSAttributedString.Key("leaflet.ordinal")
    static let leafletTinted = NSAttributedString.Key("leaflet.tinted")
}

let lineBreak: Character = "\u{2028}"
