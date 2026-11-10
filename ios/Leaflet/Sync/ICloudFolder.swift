import Foundation
import os

@MainActor
final class ICloudFolder {
    private(set) var picked: URL?
    private(set) var lost = false

    private let defaults: UserDefaults
    private static let key = "icloud.folder"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        resolve()
    }

    var root: URL? { picked.map(Self.root(of:)) }

    static func root(of folder: URL) -> URL {
        folder.lastPathComponent == "Leaflet"
            ? folder : folder.appending(path: "Leaflet", directoryHint: .isDirectory)
    }

    func use(_ folder: URL) throws -> URL? {
        guard folder.startAccessingSecurityScopedResource() else {
            throw CocoaError(.fileReadNoPermission, userInfo: [NSURLErrorKey: folder])
        }
        let bookmark: Data
        do {
            bookmark = try folder.bookmarkData()
        } catch {
            folder.stopAccessingSecurityScopedResource()
            throw error
        }
        let before = picked
        defaults.set(bookmark, forKey: Self.key)
        picked = folder
        lost = false
        return before
    }

    private func resolve() {
        guard let bookmark = defaults.data(forKey: Self.key) else { return }
        var stale = false
        guard
            let folder = try? URL(
                resolvingBookmarkData: bookmark, bookmarkDataIsStale: &stale),
            folder.startAccessingSecurityScopedResource()
        else {
            Logger.app.info("The iCloud Drive folder's bookmark no longer opens")
            lost = true
            return
        }
        if stale {
            guard let fresh = try? folder.bookmarkData() else {
                folder.stopAccessingSecurityScopedResource()
                lost = true
                return
            }
            defaults.set(fresh, forKey: Self.key)
        }
        picked = folder
    }
}
