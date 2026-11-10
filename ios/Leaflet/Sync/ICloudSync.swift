import Foundation

@MainActor
@Observable
final class ICloudSync {
    private(set) var folderName: String?
    private(set) var lost: Bool
    private(set) var turning: Bool?
    private(set) var failure: String?
    var picking = false

    @ObservationIgnored private var client: CoreClient?
    @ObservationIgnored private let folder: ICloudFolder
    @ObservationIgnored private var onAfterPicking = false

    init(folder: ICloudFolder) {
        self.folder = folder
        folderName = folder.picked?.lastPathComponent
        lost = folder.lost
    }

    func attach(_ client: CoreClient) {
        self.client = client
        client.useICloudFolder(folder.root)
    }

    func set(_ on: Bool) async {
        guard turning == nil else { return }
        if on && folder.root == nil {
            onAfterPicking = true
            picking = true
            return
        }
        await turn(on, root: on ? folder.root : nil)
    }

    func picked(_ url: URL, syncing: Bool) async {
        let on = syncing || onAfterPicking
        onAfterPicking = false
        let before: URL?
        do {
            before = try folder.use(url)
        } catch {
            failure = error.localizedDescription
            return
        }
        defer { before?.stopAccessingSecurityScopedResource() }
        folderName = url.lastPathComponent
        lost = false
        client?.useICloudFolder(folder.root)
        await turn(on, root: folder.root)
    }

    func choose() {
        onAfterPicking = false
        picking = true
    }

    private func turn(_ on: Bool, root: URL?) async {
        guard let client else { return }
        turning = on
        defer { turning = nil }
        do {
            try await client.setICloud(root: root, on: on)
            failure = nil
        } catch {
            failure = error.localizedDescription
        }
    }
}
