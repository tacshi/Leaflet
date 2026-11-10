import LeafletCore
import SwiftUI
import UIKit
import os

struct Toast: Identifiable {
    let id = UUID()
    var message: String
    var actionLabel: String?
    var action: (@MainActor () async -> Void)?
}

@MainActor
@Observable
final class AppModel {
    private(set) var client: CoreClient?
    private(set) var openFailure: String?
    let iCloud: ICloudSync

    var library = LibrarySnapshot.empty
    var templates: [TemplateRow] = []
    var todos: [TodoGroup] = []
    var conflicts: [Conflict] = []
    var status: CoreStatus?
    var syncing = false
    var syncFailure: String?
    var toast: Toast?
    var voiceNote: String?

    var theme = ThemeMode.system
    var accent = ""
    var textSize = 17.0
    var language = ""
    var shown = L.language

    var allPagesOffline = true
    var workOffline = false
    var agentsReadOtherPages = true
    var agentsUseWeb = true

    @ObservationIgnored weak var activeScene: SceneModel?
    private(set) var libraryLoaded = false
    @ObservationIgnored let scenes = NSHashTable<SceneModel>.weakObjects()
    @ObservationIgnored let front = NSHashTable<SceneModel>.weakObjects()

    @ObservationIgnored private var pageListeners: [UUID: (CoreEvent) -> Void] = [:]
    @ObservationIgnored private var opening: Task<CoreClient?, Never>?
    @ObservationIgnored private var reloading: Task<Void, Never>?
    @ObservationIgnored private var reloadAgain = false
    @ObservationIgnored private var toReload: Reload = []
    @ObservationIgnored private var todosShown = Set<UUID>()
    @ObservationIgnored private var remindTask: Task<Void, Never>?
    @ObservationIgnored var saving: Task<Void, Never>?
    @ObservationIgnored var lifecycle: Task<Void, Never>?
    @ObservationIgnored var leavings = Leavings()
    /// The pages, and the workspace, whose agents did something since the app opened.
    @ObservationIgnored private(set) var agentPages = Set<String>()
    @ObservationIgnored private var clockChanges: [any NSObjectProtocol] = []

    init() {
        Fonts.register()
        Self.useTimeZone()
        let folder = ICloudFolder()
        iCloud = ICloudSync(folder: folder)
        BackgroundSync.register(
            inFront: { [weak self] in self?.front.allObjects.isEmpty == false },
            opened: { [weak self] in await self?.opened() })
        let root = folder.root?.path(percentEncoded: false)
        opening = Task { [weak self] in await self?.open(iCloudRoot: root) }
        VoiceNotes.app = self
        for name in [
            NSNotification.Name.NSSystemTimeZoneDidChange,
            UIApplication.significantTimeChangeNotification,
        ] {
            clockChanges.append(
                NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) {
                    _ in
                    MainActor.assumeIsolated { Self.useTimeZone() }
                })
        }
    }

    private static func useTimeZone() {
        NSTimeZone.resetSystemTimeZone()
        let zone = TimeZone.current
        MobileSetTimeZone(zone.identifier, zone.secondsFromGMT())
    }

    private func open(iCloudRoot: String?) async -> CoreClient? {
        let locale = L.language
        let opened = await Task.detached(priority: .userInitiated) {
            Result {
                let dir = try FileManager.default.url(
                    for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil,
                    create: true
                ).appending(path: "Leaflet", directoryHint: .isDirectory)
                return try CoreClient.open(dataDir: dir, iCloudRoot: iCloudRoot, locale: locale)
            }
        }.value
        let client: CoreClient
        switch opened {
        case .success(let c): client = c
        case .failure(let error):
            openFailure = error.localizedDescription
            return nil
        }
        self.client = client
        iCloud.attach(client)
        Recorder.shared.attach(client) { [weak self] in self?.show(Toast(message: $0)) }
        ImageBlock.client = client
        Task { [weak self] in
            for await event in client.events {
                self?.handle(event)
            }
        }
        Task {
            await loadSettings()
            await reload()
        }
        return client
    }

    func opened() async -> CoreClient? {
        if let client { return client }
        return await opening?.value
    }

    func listen(_ fn: @escaping (CoreEvent) -> Void) -> UUID {
        let id = UUID()
        pageListeners[id] = fn
        return id
    }

    func stopListening(_ id: UUID) { pageListeners[id] = nil }

    private func handle(_ event: CoreEvent) {
        switch event {
        case .changed(let table, let pageID):
            if ["pages", "page_contents", "*"].contains(table) { remind(after: .seconds(2)) }
            if ["key_values", "session", "*"].contains(table) { Recorder.shared.reloadChoices() }
            if ["pages", "folders", "spaces", "session"].contains(table) {
                Task { await reload() }
            } else if table == "files" {
                Task { await reload(.library) }
            } else if table == "page_contents" {
                var parts: Reload = [.library, .todos, .status]
                if templates.contains(where: { $0.pageID == pageID }) { parts.insert(.templates) }
                Task { await reload(parts) }
            } else if table == "conflicts" {
                Task { await refreshConflicts() }
            } else if table == "recordings" {
                Recorder.shared.reload()
            } else if table == "*" {
                Task {
                    await loadSettings()
                    await reload()
                }
                Recorder.shared.reload()
            }
        case .syncState(let running, let failure):
            syncing = running
            if !running {
                syncFailure = failure
                Task { await refreshStatus() }
            }
        case .notice(let message):
            show(Toast(message: message))
        case .recording(let state):
            Recorder.shared.update(state)
        case .recordingLevels(let levels):
            Recorder.shared.update(levels: levels)
        case .agentsChanged(let pageID):
            if !pageID.isEmpty { agentPages.insert(pageID) }
        default:
            break
        }
        for fn in pageListeners.values { fn(event) }
    }

    func remind(after delay: Duration = .zero) {
        remindTask?.cancel()
        remindTask = Task { [weak self] in
            try? await Task.sleep(for: delay)
            guard !Task.isCancelled, let self, let client = await opened() else { return }
            await Reminders.schedule(client, asking: !front.allObjects.isEmpty)
        }
    }

    struct Reload: OptionSet {
        let rawValue: Int
        static let library = Reload(rawValue: 1 << 0)
        static let todos = Reload(rawValue: 1 << 1)
        static let templates = Reload(rawValue: 1 << 2)
        static let conflicts = Reload(rawValue: 1 << 3)
        static let status = Reload(rawValue: 1 << 4)
        static let all: Reload = [.library, .todos, .templates, .conflicts, .status]
    }

    func reload(_ parts: Reload = .all) async {
        guard let client = await opened() else { return }
        toReload.formUnion(parts)
        if let running = reloading {
            reloadAgain = true
            await running.value
            return
        }
        let task = Task {
            repeat {
                reloadAgain = false
                let parts = toReload
                toReload = []
                if parts.contains(.library), let lib = await read("library", client.library) {
                    libraryLoaded = true
                    library = lib
                }
                if parts.contains(.todos), !todosShown.isEmpty,
                    let list = await read("to-dos", client.todos)
                {
                    todos = list
                }
                if parts.contains(.templates), let list = await read("templates", client.templates)
                {
                    templates = list
                }
                if parts.contains(.conflicts) { await refreshConflicts() }
                if parts.contains(.status) { await refreshStatus() }
            } while reloadAgain
            reloading = nil
        }
        reloading = task
        await task.value
    }

    func showsTodos(_ id: UUID, _ shown: Bool) {
        guard shown else {
            todosShown.remove(id)
            return
        }
        if todosShown.insert(id).inserted { Task { await reload(.todos) } }
    }

    func read<T>(_ what: String, _ body: () async throws -> T) async -> T? {
        do {
            return try await body()
        } catch {
            Logger.app.error("Could not read the \(what, privacy: .public): \(error)")
            return nil
        }
    }

    func refreshConflicts() async {
        guard let client else { return }
        if let list = await read("conflicts", client.conflicts) { conflicts = list }
    }

    func refreshStatus() async {
        guard let client else { return }
        status = await read("status", client.status)
        if let status { Reminders.keep(to: status.userID) }
    }

    func syncNow() async {
        try? await client?.syncNow()
        await reload()
    }

    func show(_ toast: Toast) {
        self.toast = toast
        AccessibilityNotification.Announcement(toast.message).post()
        let id = toast.id
        Task {
            try? await Task.sleep(for: .seconds(toast.action == nil ? 3 : 6))
            if self.toast?.id == id { self.toast = nil }
        }
    }
}
