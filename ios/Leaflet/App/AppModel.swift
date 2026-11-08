import LeafletCore
import SwiftUI
import UIKit
import os

/// Toast is a short message at the bottom of the window, with an action
/// such as Undo.
struct Toast: Identifiable {
    let id = UUID()
    var message: String
    var actionLabel: String?
    var action: (@MainActor () async -> Void)?
}

/// AppModel is the app's state, shared by its windows: the core, the
/// library it lists, how sync stands, and the settings that change how
/// the app looks. Here it opens the core and hears its events; what the
/// library's menus do is in AppModel+Library, the settings in
/// AppModel+Settings, and sync's starts and stops as the windows come and
/// go in AppModel+Lifecycle.
@MainActor
@Observable
final class AppModel {
    /// client is the core, once it is open: it opens off the main thread,
    /// as the app launches, and opened waits for it.
    private(set) var client: CoreClient?
    /// openFailure says why the core could not open, when it couldn't.
    private(set) var openFailure: String?
    let iCloud: ICloudSync

    var library = LibrarySnapshot.empty
    /// templates are what a blank page can start from, and new pages in a
    /// place: the user's, then the starters.
    var templates: [TemplateRow] = []
    /// todos are the To-dos tab's sections, as the core sorts them.
    var todos: [TodoGroup] = []
    /// conflicts are the blocks two devices changed at once, for Settings
    /// › Sync and storage to settle.
    var conflicts: [Conflict] = []
    var status: CoreStatus?
    var syncing = false
    var syncFailure: String?
    var toast: Toast?
    /// voiceNote is the page a voice note records in, started from outside
    /// the app, for the window in front to open: at once, or as the app
    /// comes to the front.
    var voiceNote: String?

    // The settings the look comes from, as the core keeps them.
    var theme = ThemeMode.system
    var accent = ""
    var textSize = 17.0
    /// language is the tag of the language chosen in the settings, or ""
    /// for the system's; shown is the one the app shows, which the windows
    /// draw again in as it changes (showLanguage sets it).
    var language = ""
    var shown = L.language

    // The settings of sync and agents the app shows, as the core keeps
    // them.
    var allPagesOffline = true
    var workOffline = false
    var agentsReadOtherPages = true
    var agentsUseWeb = true

    /// activeScene is the window that last came to the front, which the
    /// menu bar's commands act on while no view in a window has focus.
    @ObservationIgnored weak var activeScene: SceneModel?
    /// libraryLoaded says the library has been read from the core once:
    /// before then it is empty for not being read, not for having no
    /// pages.
    private(set) var libraryLoaded = false
    /// scenes are the windows that came to the front, as their scene
    /// models, and front those not in the background since: sync stops
    /// only once none is. Both hold them weakly, so a window torn down
    /// without going to the background first keeps nothing running.
    @ObservationIgnored let scenes = NSHashTable<SceneModel>.weakObjects()
    @ObservationIgnored let front = NSHashTable<SceneModel>.weakObjects()

    /// pageListeners hear the events of the pages open in the windows.
    @ObservationIgnored private var pageListeners: [UUID: (CoreEvent) -> Void] = [:]
    /// opening opens the core, and gives it, or nil when it couldn't.
    @ObservationIgnored private var opening: Task<CoreClient?, Never>?
    /// reloading is the read of the library running, and reloadAgain
    /// asks it to read once more, for a change that came meanwhile, what
    /// toReload says.
    @ObservationIgnored private var reloading: Task<Void, Never>?
    @ObservationIgnored private var reloadAgain = false
    @ObservationIgnored private var toReload: Reload = []
    /// todosShown are the windows' sidebars that show the To-dos tab, by
    /// their own IDs: the to-dos are read again as pages change only while
    /// one does, each autosave of a page being typed in a change, and once
    /// as one comes to show them.
    @ObservationIgnored private var todosShown = Set<UUID>()
    @ObservationIgnored private var remindTask: Task<Void, Never>?
    /// saving is the last of the settings on their way to the core, which
    /// go one after another, as they were changed.
    @ObservationIgnored var saving: Task<Void, Never>?
    /// lifecycle is the last of sync's starts and stops, as the windows
    /// come and go, which run in that order: a stop runs after a start
    /// asked for before it, and the other way round.
    @ObservationIgnored var lifecycle: Task<Void, Never>?
    /// leavings are the times the app went to the background.
    @ObservationIgnored var leavings = Leavings()
    @ObservationIgnored private var clockChanges: [any NSObjectProtocol] = []

    init() {
        Fonts.register()
        // Before the core starts: it can't find the device's zone itself.
        Self.useTimeZone()
        let folder = ICloudFolder()
        iCloud = ICloudSync(folder: folder)
        // As the app launches, as the system has it, for passes that run
        // once the core is open.
        BackgroundSync.register(
            inFront: { [weak self] in self?.front.allObjects.isEmpty == false },
            opened: { [weak self] in await self?.opened() })
        let root = folder.root?.path(percentEncoded: false)
        opening = Task { [weak self] in await self?.open(iCloudRoot: root) }
        // For the intents iOS runs, as the Action Button's, which may
        // launch the app to run one.
        VoiceNotes.app = self
        // The zone the device is in changes as it travels, and its offset
        // as the clocks change. The core's dates follow the clocks at once,
        // and a new zone from the next time the core opens.
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

    /// useTimeZone gives the core the zone the device is in now. Once the
    /// core is open it keeps the zone it opened with: a new one applies
    /// from the next time it opens, though a zone known by name follows
    /// its own clock changes meanwhile.
    private static func useTimeZone() {
        NSTimeZone.resetSystemTimeZone()
        let zone = TimeZone.current
        MobileSetTimeZone(zone.identifier, zone.secondsFromGMT())
    }

    /// open opens the core over the database in Application Support, off
    /// the main thread, which opening and migrating it would hold up.
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

    /// opened returns the core, once it is open, or nil when it couldn't
    /// open: what runs as the app launches waits for it.
    func opened() async -> CoreClient? {
        if let client { return client }
        return await opening?.value
    }

    // MARK: Events

    /// listen has the windows' open pages hear the core's events, until
    /// the returned token is given to stopListening.
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
            // A speech key saved or removed, or signing in or out, changes
            // the providers a transcription is picked from.
            if ["key_values", "session", "*"].contains(table) { Recorder.shared.reloadChoices() }
            if ["pages", "folders", "spaces", "session"].contains(table) {
                Task { await reload() }
            } else if table == "files" {
                Task { await reload(.library) }
            } else if table == "page_contents" {
                // A page's content saved, as each autosave does: its time,
                // to-dos and what waits to go up change, and a template's
                // hint; not the rest of the library.
                var parts: Reload = [.library, .todos, .status]
                if templates.contains(where: { $0.pageID == pageID }) { parts.insert(.templates) }
                Task { await reload(parts) }
            } else if table == "conflicts" {
                Task { await refreshConflicts() }
            } else if table == "recordings" {
                Recorder.shared.reload()
            } else if table == "*" {
                // Everything at once: shared pages taken off, a space
                // moved, or the account's pages gone as it signed out or
                // the device was wiped. The settings may have gone too.
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
        default:
            break
        }
        for fn in pageListeners.values { fn(event) }
    }

    // MARK: Reminders

    /// remind schedules the to-dos' reminders again, after a moment for
    /// the changes that come together, or as a save follows a save. Only
    /// with a window in front may it ask for permission.
    func remind(after delay: Duration = .zero) {
        remindTask?.cancel()
        remindTask = Task { [weak self] in
            try? await Task.sleep(for: delay)
            guard !Task.isCancelled, let self, let client = await opened() else { return }
            await Reminders.schedule(client, asking: !front.allObjects.isEmpty)
        }
    }

    // MARK: Library

    /// Reload is what reload reads again.
    struct Reload: OptionSet {
        let rawValue: Int
        static let library = Reload(rawValue: 1 << 0)
        static let todos = Reload(rawValue: 1 << 1)
        static let templates = Reload(rawValue: 1 << 2)
        static let conflicts = Reload(rawValue: 1 << 3)
        static let status = Reload(rawValue: 1 << 4)
        static let all: Reload = [.library, .todos, .templates, .conflicts, .status]
    }

    /// reload reads the library, the to-dos and the status again, or the
    /// parts of them asked, and returns once they are read as they are
    /// now: called while a read runs, which may have begun before the
    /// change it is called for, it has that read go once more, and waits
    /// for it.
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
            // At once, as it ends: a reload called after can't find it
            // running and wait on it for nothing.
            reloading = nil
        }
        reloading = task
        await task.value
    }

    /// showsTodos says the sidebar of id shows the To-dos tab, or no longer
    /// does: one coming to show it reads them, as they may have changed
    /// while none did.
    func showsTodos(_ id: UUID, _ shown: Bool) {
        guard shown else {
            todosShown.remove(id)
            return
        }
        if todosShown.insert(id).inserted { Task { await reload(.todos) } }
    }

    /// read returns what body reads of the core, or nil, saying why in
    /// the log: what was read before stays, rather than an empty sidebar
    /// with nothing said, as when the core and the app disagree on what
    /// the JSON holds.
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
    }

    /// syncNow runs a pass, as pull to refresh does. How it ended comes as
    /// the pass's state, in words for the user, which syncFailure keeps.
    func syncNow() async {
        try? await client?.syncNow()
        await reload()
    }

    /// show shows toast in every window, and VoiceOver says it, once.
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
