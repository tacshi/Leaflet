import Foundation

extension CoreClient {
    /// setICloud turns iCloud sync on or off, through the Leaflet folder
    /// at root; nil keeps the folder the core has. It fails, with why in
    /// words for the user, when iCloud sync can't turn on.
    nonisolated(nonsending) func setICloud(root: URL?, on: Bool) async throws {
        let path = root?.path(percentEncoded: false) ?? ""
        try await call(waiting: true) { try $0.setICloud(path, on: on) }
    }

    /// useICloudFolder gives the Leaflet folder the core watches while
    /// iCloud sync is on, to sync soon after another device writes to it.
    func useICloudFolder(_ root: URL?) {
        platform.useICloud(root) { [weak self] in
            Task { await self?.kick() }
        }
    }

    /// stopWatchingICloud stops watching the folder as the app goes to
    /// the background, where a file presenter must not stay; Start
    /// watches it again.
    func stopWatchingICloud() { platform.watchICloud(false) }
}
