import Foundation

@testable import Leaflet

final class ScratchCore: Sendable {
    let client: CoreClient
    private let dir: URL

    init(_ name: String) throws {
        dir = FileManager.default.temporaryDirectory
            .appending(path: "\(name)-\(UUID().uuidString)", directoryHint: .isDirectory)
        client = try CoreClient.open(dataDir: dir, iCloudRoot: nil, locale: "en")
    }

    deinit { try? FileManager.default.removeItem(at: dir) }
}
