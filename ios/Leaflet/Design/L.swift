import Foundation
import LeafletCore
import Synchronization

enum L {
    private static let current = Mutex(MobileLanguage(preferred()))

    static var language: String { current.withLock { $0 } }

    static func use(_ language: String) {
        let tag = language.isEmpty ? MobileLanguage(preferred()) : language
        current.withLock { $0 = tag }
    }

    struct Language: Decodable, Hashable {
        var tag: String
        var name: String
    }

    static let languages: [Language] =
        (try? JSONDecoder().decode([Language].self, from: Data(MobileLanguages().utf8))) ?? []

    static var bundle: Bundle {
        let tag = language
        return bundles.withLock { cache in
            if let b = cache[tag] { return b }
            let b =
                Bundle.main.path(forResource: tag, ofType: "lproj").flatMap(Bundle.init(path:))
                ?? .main
            cache[tag] = b
            return b
        }
    }
    private static let bundles = Mutex<[String: Bundle]>([:])

    static func text(_ key: String, _ args: [String: any Sendable] = [:]) -> String {
        var json = ""
        if !args.isEmpty {
            var values: [String: Any] = [:]
            for (name, value) in args {
                if let date = value as? Date {
                    values[name] = ["date": Int64(date.timeIntervalSince1970 * 1000)]
                } else {
                    values[name] = value
                }
            }
            if let data = try? JSONSerialization.data(withJSONObject: values) {
                json = String(decoding: data, as: UTF8.self)
            }
        }
        return MobileText(language, key, json)
    }

    private static func preferred() -> String {
        let data = (try? JSONEncoder().encode(Locale.preferredLanguages)) ?? Data("[]".utf8)
        return String(decoding: data, as: UTF8.self)
    }
}
