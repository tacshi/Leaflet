import Foundation
import LeafletCore
import Network
import Synchronization
import UIKit
import os

/// PlatformBridge is what the app gives the Go core: the keychain, the
/// network's state, the device's name, opening links, iCloud downloads
/// and the log. The core calls it from its own threads.
final class PlatformBridge: NSObject, MobilePlatformProtocol, Sendable {
    private let keychain: Keychain
    private let reachable = Reachable()
    private let monitor = NWPathMonitor()
    private let name: String
    private let iCloud = Mutex(ICloudWatch())
    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "Leaflet", category: "core")

    @MainActor
    init(service: String) {
        keychain = Keychain(service: service)
        name = UIDevice.current.name
        super.init()
        monitor.pathUpdateHandler = { [reachable] path in
            reachable.online.withLock { $0 = path.status == .satisfied }
        }
        monitor.start(queue: DispatchQueue(label: "app.capyvibe.leaflet.network"))
    }

    deinit { monitor.cancel() }

    func secretGet(_ name: String?) throws -> Data {
        // No secret is no bytes: the core reads that as none.
        try keychain.get(name ?? "") ?? Data()
    }

    func secretSet(_ name: String?, value: Data?) throws {
        try keychain.set(name ?? "", value ?? Data())
    }

    func secretDelete(_ name: String?) throws {
        try keychain.delete(name ?? "")
    }

    func online() -> Bool { reachable.online.withLock { $0 } }

    func deviceName() -> String { name }

    func openURL(_ url: String?) {
        guard let url = url.flatMap(URL.init(string:)) else { return }
        Task { @MainActor in UIApplication.shared.open(url) }
    }

    /// useICloud gives the Leaflet folder watchICloud watches, and what
    /// to do when another device writes to it.
    func useICloud(_ folder: URL?, onChange: @escaping @Sendable () -> Void) {
        iCloud.withLock {
            $0.folder = folder
            $0.onChange = onChange
        }
    }

    func watchICloud(_ on: Bool) {
        let (old, new) = iCloud.withLock { w in
            let old = w.watcher
            w.watcher = nil
            if on, let folder = w.folder, let onChange = w.onChange {
                w.watcher = FolderWatcher(folder, onChange: onChange)
            }
            return (old, w.watcher)
        }
        old?.stop()
        new?.start()
    }

    func startDownload(_ path: String?) throws {
        try FileManager.default.startDownloadingUbiquitousItem(
            at: URL(fileURLWithPath: path ?? ""))
    }

    func log(_ line: String?) {
        logger.info("\(line ?? "", privacy: .public)")
    }
}

/// ICloudWatch is the Leaflet folder in iCloud Drive and its watcher,
/// while watching it.
private struct ICloudWatch {
    var folder: URL?
    var onChange: (@Sendable () -> Void)?
    var watcher: FolderWatcher?
}

/// Reachable is whether the network can be reached, as the path monitor
/// last said.
private final class Reachable: Sendable {
    let online = Mutex(true)
}
