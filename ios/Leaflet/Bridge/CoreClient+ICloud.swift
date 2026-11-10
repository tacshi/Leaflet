import Foundation

extension CoreClient {
    nonisolated(nonsending) func setICloud(root: URL?, on: Bool) async throws {
        let path = root?.path(percentEncoded: false) ?? ""
        try await call(waiting: true) { try $0.setICloud(path, on: on) }
    }

    func useICloudFolder(_ root: URL?) {
        platform.useICloud(root) { [weak self] in
            Task { await self?.kick() }
        }
    }

    func stopWatchingICloud() { platform.setInFront(false) }
}
