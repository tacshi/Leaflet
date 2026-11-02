import Foundation

@testable import Leaflet

/// ScratchCore is a core over a folder of its own, for a test. The folder
/// goes once the test lets the ScratchCore go, though the core is still
/// open: the bridge has no way to close it. Its database stays open over
/// files no longer in any folder, which the system frees as the test
/// process ends; nothing the test checks reads them after.
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
