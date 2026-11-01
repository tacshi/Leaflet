import Foundation
import LeafletCore
import Synchronization

/// L says the messages the app shares with the desktop app, from the
/// catalogs in locales/, in the language the app shows. The core formats
/// them, plurals and dates as the desktop does; Generated/Strings.swift
/// has an accessor of L for each.
enum L {
    private static let current = Mutex(MobileLanguage(preferred()))

    /// language is the tag of the catalogs' language the app shows.
    static var language: String { current.withLock { $0 } }

    /// use shows the app in language, a tag of the catalogs, or with ""
    /// in the one that suits the system's languages best.
    static func use(_ language: String) {
        let tag = language.isEmpty ? MobileLanguage(preferred()) : language
        current.withLock { $0 = tag }
    }

    /// Language is a language the app can show: its tag, and its name in
    /// itself.
    struct Language: Decodable, Hashable {
        var tag: String
        var name: String
    }

    /// languages are the languages of the catalogs, as the desktop's
    /// settings list them.
    static let languages: [Language] =
        (try? JSONDecoder().decode([Language].self, from: Data(MobileLanguages().utf8))) ?? []

    /// bundle is the app's own strings (LI) in the language shown: the
    /// system's choice of the app's localizations need not be the same.
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

    /// text says the message key with args, its placeholders.
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
