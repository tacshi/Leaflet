import Foundation
import os

/// ICloudFolder is the folder in iCloud Drive pages sync through, as the
/// user picked it: the Leaflet folder the Mac syncs through, or iCloud
/// Drive itself to start one. It keeps a bookmark of the folder to open
/// it again at the next launch, and holds access to it while the app
/// runs, for the core's own reads and writes in it.
@MainActor
final class ICloudFolder {
    /// picked is the folder the user picked, while the app holds access
    /// to it.
    private(set) var picked: URL?
    /// lost says the folder picked before can't be opened again: the
    /// user picks it again.
    private(set) var lost = false

    private let defaults: UserDefaults
    private static let key = "icloud.folder"  // mobsf-ignore: ios_hardcoded_secret -- a name.

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        resolve()
    }

    /// root is the Leaflet folder: the one picked, or a Leaflet folder
    /// in it when the user picked iCloud Drive itself, which the first
    /// pass makes.
    var root: URL? { picked.map(Self.root(of:)) }

    static func root(of folder: URL) -> URL {
        folder.lastPathComponent == "Leaflet"
            ? folder : folder.appending(path: "Leaflet", directoryHint: .isDirectory)
    }

    /// use keeps folder, as the user picked it, in place of the one
    /// before.
    func use(_ folder: URL) throws {
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
        picked?.stopAccessingSecurityScopedResource()
        defaults.set(bookmark, forKey: Self.key)
        picked = folder
        lost = false
    }

    /// resolve opens the folder of the bookmark kept, if there is one. A
    /// stale bookmark still names the folder: it is made again while the
    /// app has access, as Apple recommends, and the user picks the folder
    /// again only when it can't be.
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
