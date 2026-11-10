import DeviceKit
import Foundation
import LeafletCore
import Network
import Synchronization
import UIKit
import os

final class PlatformBridge: NSObject, MobilePlatformProtocol, Sendable {
    private let keychain: Keychain
    private let reachable = Reachable()
    private let monitor = NWPathMonitor()
    private let name: String
    private let iCloud = Mutex(ICloudWatch())
    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "Leaflet", category: "core")

    init(service: String) {
        keychain = Keychain(service: service)
        name = Self.label(of: DeviceKit.Device.current)
        super.init()
        monitor.pathUpdateHandler = { [reachable] path in
            reachable.online.withLock { $0 = path.status == .satisfied }
        }
        monitor.start(queue: DispatchQueue(label: "app.capyvibe.leaflet.network"))
    }

    deinit { monitor.cancel() }

    func secretGet(_ name: String?) throws -> Data {
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

    static func label(of device: DeviceKit.Device) -> String {
        if case .unknown(let identifier) = device {
            let kind = identifier.prefix { $0.isLetter }
            return kind.isEmpty ? "iOS" : String(kind)
        }
        return device.description
    }

    func openURL(_ url: String?) {
        guard let url = url.flatMap(URL.init(string:)) else { return }
        Task { @MainActor in UIApplication.shared.open(url) }
    }

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
            if on, w.inFront, let folder = w.folder, let onChange = w.onChange {
                w.watcher = FolderWatcher(folder, onChange: onChange)
            }
            return (old, w.watcher)
        }
        old?.stop()
        new?.start()
    }

    func setInFront(_ front: Bool) {
        iCloud.withLock { $0.inFront = front }
        if !front { watchICloud(false) }
    }

    func startDownload(_ path: String?) throws {
        try FileManager.default.startDownloadingUbiquitousItem(
            at: URL(fileURLWithPath: path ?? ""))
    }

    func log(_ line: String?) {
        logger.info("\(line ?? "", privacy: .public)")
    }
}

private struct ICloudWatch {
    var folder: URL?
    var onChange: (@Sendable () -> Void)?
    var watcher: FolderWatcher?
    var inFront = false
}

private final class Reachable: Sendable {
    let online = Mutex(true)
}
