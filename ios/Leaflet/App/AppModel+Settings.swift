import Foundation

extension AppModel {
    func loadSettings() async {
        let decode = { (raw: String) in
            try JSONDecoder().decode(LookSettings.self, from: Data(raw.utf8))
        }
        guard let client, let raw = await read("settings", client.settings),
            let s = await read("settings", { try decode(raw) })
        else { return }
        theme = ThemeMode(rawValue: s.theme ?? "") ?? .system
        accent = s.accent ?? ""
        if let size = s.textSize, size > 0 { textSize = size }
        allPagesOffline = s.allPagesOffline ?? true
        workOffline = s.workOffline ?? false
        agentsReadOtherPages = s.agentsReadOtherPages ?? true
        agentsUseWeb = s.agentsUseWeb ?? true
        if (s.language ?? "") != language || L.language != shown {
            language = s.language ?? ""
            await showLanguage()
        }
    }

    func saveLook() {
        save(["theme": theme.rawValue, "accent": accent, "text_size": textSize])
    }

    func setLanguage(_ tag: String) {
        guard tag != language else { return }
        language = tag
        save(["language": tag])
        if tag.isEmpty {
            UserDefaults.standard.removeObject(forKey: "AppleLanguages")
        } else {
            UserDefaults.standard.set([tag], forKey: "AppleLanguages")
        }
        Task { await showLanguage() }
    }

    func showLanguage() async {
        L.use(language)
        await client?.setLocale(L.language)
        shown = L.language
        await reload()
        remind()
    }

    func save(_ settings: [String: any Sendable]) {
        guard let client,
            let data = try? JSONSerialization.data(withJSONObject: settings)
        else { return }
        let json = String(decoding: data, as: UTF8.self)
        saving = Task { [previous = saving] in
            await previous?.value
            try? await client.saveSettings(json)
        }
    }
}

private struct LookSettings: Decodable {
    var theme: String?
    var accent: String?
    var textSize: Double?
    var language: String?
    var allPagesOffline: Bool?
    var workOffline: Bool?
    var agentsReadOtherPages: Bool?
    var agentsUseWeb: Bool?

    enum CodingKeys: String, CodingKey {
        case theme, accent, language
        case textSize = "text_size"
        case allPagesOffline = "all_pages_offline"
        case workOffline = "work_offline"
        case agentsReadOtherPages = "agents_read_other_pages"
        case agentsUseWeb = "agents_use_web"
    }
}
