import Foundation

/// ICloudSync is iCloud sync as Settings shows it: the folder picked, and
/// turning sync through it on and off, one turn at a time.
@MainActor
@Observable
final class ICloudSync {
    /// folderName names the folder picked, while the app can open it.
    private(set) var folderName: String?
    /// lost says the folder picked before can't be opened again.
    private(set) var lost: Bool
    /// turning is the state the switch turns to, until it has.
    private(set) var turning: Bool?
    /// failure says why iCloud sync couldn't turn on, or the folder
    /// couldn't be used.
    private(set) var failure: String?
    /// picking shows the folder picker.
    var picking = false

    /// client is the core, once it is open.
    @ObservationIgnored private var client: CoreClient?
    @ObservationIgnored private let folder: ICloudFolder
    /// onAfterPicking turns sync on once the folder is picked, when
    /// turning it on asked for one.
    @ObservationIgnored private var onAfterPicking = false

    init(folder: ICloudFolder) {
        self.folder = folder
        folderName = folder.picked?.lastPathComponent
        lost = folder.lost
    }

    /// attach gives sync the core, as it opens, and the core the folder.
    func attach(_ client: CoreClient) {
        self.client = client
        client.useICloudFolder(folder.root)
    }

    /// set turns iCloud sync on or off. On with no folder to sync
    /// through, the user picks it first.
    func set(_ on: Bool) async {
        guard turning == nil else { return }
        if on && folder.root == nil {
            onAfterPicking = true
            picking = true
            return
        }
        await turn(on, root: on ? folder.root : nil)
    }

    /// picked uses the folder the user picked, with sync on through it
    /// when it was on, or turning on asked for it.
    func picked(_ url: URL, syncing: Bool) async {
        let on = syncing || onAfterPicking
        onAfterPicking = false
        do {
            try folder.use(url)
        } catch {
            failure = error.localizedDescription
            return
        }
        folderName = url.lastPathComponent
        lost = false
        client?.useICloudFolder(folder.root)
        await turn(on, root: folder.root)
    }

    /// choose has the user pick another folder, with sync left on or
    /// off as it is.
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
