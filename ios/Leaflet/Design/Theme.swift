import CoreText
import SwiftUI

/// ThemeMode is the theme the settings keep: the system's appearance,
/// or light, sepia or dark whatever it is.
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

    /// colorScheme is the appearance the theme asks of the system, or nil
    /// to follow it.
    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light, .sepia: .light
        case .dark: .dark
        }
    }
}

extension Palette {
    /// key tells palettes apart: by the colors a page is drawn in.
    var key: String { surface.hex + text.hex + accent.hex + code.hex }

    /// of returns the palette of accent (a name of accents, or "" or
    /// "system" for Blue, the system's own: iOS lets no other be chosen)
    /// in mode, as the system's appearance is scheme.
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
    /// themedForm sets a Form or List on the theme's ground, as the
    /// desktop's settings are: the system's grouped background stays
    /// white in sepia. Its rows take the surface with listRowBackground.
    func themedForm(_ palette: Palette) -> some View {
        scrollContentBackground(.hidden).background(palette.ground)
    }
}

extension EnvironmentValues {
    /// palette is the color tokens of the theme the settings choose.
    @Entry var palette: Palette = Palette.palettes["Blue"]!["light"]!
}

/// Fonts are Leaflet's own: Source Serif 4 for pages, scaled with Dynamic
/// Type. The interface is in the system's font, as iOS apps are.
enum Fonts {
    static let page = "Source Serif 4"

    /// register makes the bundled fonts usable by name, once, at launch.
    static func register() {
        let urls = Bundle.main.urls(forResourcesWithExtension: "ttf", subdirectory: nil) ?? []
        CTFontManagerRegisterFontURLs(urls as CFArray, .process, true, nil)
    }

    /// pageText is the font of a page's text at the size the settings
    /// choose, scaled as the system's text size is.
    static func pageText(size: Double) -> Font {
        .custom(page, size: size, relativeTo: .body)
    }

    /// pageTitle is the font of a page's title, as the desktop's is: 2.15
    /// times its text.
    static func pageTitle(size: Double) -> Font {
        .custom(page, size: size * 2.15, relativeTo: .largeTitle).weight(.semibold)
    }
}
