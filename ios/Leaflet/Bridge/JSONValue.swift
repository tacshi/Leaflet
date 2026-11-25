import Foundation

enum JSONValue: Codable, Hashable, Sendable {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let v = try? c.decode(Bool.self) {
            self = .bool(v)
        } else if let v = try? c.decode(Int64.self) {
            self = .int(v)
        } else if let v = try? c.decode(Double.self) {
            self = .double(v)
        } else if let v = try? c.decode(String.self) {
            self = .string(v)
        } else if let v = try? c.decode([JSONValue].self) {
            self = .array(v)
        } else {
            self = .object(try c.decode([String: JSONValue].self))
        }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let v): try c.encode(v)
        case .int(let v): try c.encode(v)
        case .double(let v): try c.encode(v)
        case .string(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .object(let v): try c.encode(v)
        }
    }

    var string: String? {
        if case .string(let v) = self { return v }
        return nil
    }

    var bool: Bool? {
        if case .bool(let v) = self { return v }
        return nil
    }

    var int: Int64? {
        switch self {
        case .int(let v): return v
        case .double(let v): return Int64(exactly: v)
        default: return nil
        }
    }

    var double: Double? {
        switch self {
        case .int(let v): return Double(v)
        case .double(let v): return v
        default: return nil
        }
    }

    var array: [JSONValue]? {
        if case .array(let v) = self { return v }
        return nil
    }

    var object: [String: JSONValue]? {
        if case .object(let v) = self { return v }
        return nil
    }

    subscript(key: String) -> JSONValue? { object?[key] }
}

/// A value as JSON text, as the Go core takes it.
func jsonString(_ value: some Encodable) throws -> String {
    String(decoding: try JSONEncoder().encode(value), as: UTF8.self)
}

/// What a function of the Go core makes of a value: the value goes in as
/// JSON, and the JSON the function answers comes back read as T. It throws
/// the function's error, or why either JSON would not do.
func coreValue<T: Decodable>(
    _ value: some Encodable, as type: T.Type = T.self,
    _ call: (String?, NSErrorPointer) -> String
) throws -> T {
    var error: NSError?
    let raw = call(try jsonString(value), &error)
    if let error { throw error }
    return try JSONDecoder().decode(type, from: Data(raw.utf8))
}

/// What is made of blocks, kept by each block's id until the block changes
/// or what else it was made with does (`with`, as a language): past `most`
/// blocks, all are let go at once.
struct NodeCache<Value> {
    private var kept: [String: (node: DocNode, with: String, value: Value)] = [:]
    private let most: Int

    init(most: Int) { self.most = most }

    mutating func value(
        of node: DocNode, with: String = "", make: (DocNode) -> Value?
    ) -> Value? {
        if let k = kept[node.id], k.node == node, k.with == with { return k.value }
        guard let value = make(node) else { return nil }
        if kept.count >= most { kept.removeAll(keepingCapacity: true) }
        kept[node.id] = (node, with, value)
        return value
    }
}
