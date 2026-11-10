import CoreText
import SwiftUI

enum ThemeMode: String, CaseIterable, Identifiable, Sendable {
    case system, light, sepia, dark
    var id: String { rawValue }

    var label: String {
        switch self {
        case .system: L.settingsSystem
        case .light: L.settingsThemeLight
        case .sepia: L.settingsThemeSepia
        case .dark: L.settingsThemeDark
        }
    }

    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light, .sepia: .light
        case .dark: .dark
        }
    }
}

extension Palette {
    var key: String { surface.hex + text.hex + accent.hex + code.hex }

    static func of(accent: String, mode: ThemeMode, scheme: ColorScheme) -> Palette {
        let byMode = palettes[accent] ?? palettes["Blue"]!
        let key: String
        switch mode {
        case .sepia: key = "sepia"
        case .dark: key = "dark"
        case .light: key = "light"
        case .system: key = scheme == .dark ? "dark" : "light"
        }
        return byMode[key]!
    }
}

extension View {
    func themedForm(_ palette: Palette) -> some View {
        scrollContentBackground(.hidden).background(palette.ground)
    }
}

extension EnvironmentValues {
    @Entry var palette: Palette = Palette.palettes["Blue"]!["light"]!
}

enum Fonts {
    static let page = "Source Serif 4"

    static func register() {
        let urls = Bundle.main.urls(forResourcesWithExtension: "ttf", subdirectory: nil) ?? []
        CTFontManagerRegisterFontURLs(urls as CFArray, .process, true, nil)
    }

    static func pageText(size: Double) -> Font {
        .custom(page, size: size, relativeTo: .body)
    }

    static func pageTitle(size: Double) -> Font {
        .custom(page, size: size * 2.15, relativeTo: .largeTitle).weight(.semibold)
    }
}
