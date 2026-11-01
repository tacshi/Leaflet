import LeafletCore
import SwiftUI
import UIKit

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
/// the app looks.
@MainActor
@Observable
final class AppModel {
    let client: CoreClient?
    /// openFailure says why the core could not open, when it couldn't.
    let openFailure: String?
    let iCloud: ICloudSync

    var library = LibrarySnapshot.empty
    /// templates are what a blank page can start from, and new pages in a
    /// place: the user's, then the starters.
    var templates: [TemplateRow] = []
    var todos: [Todo] = []
    /// conflicts are the blocks two devices changed at once, for Settings
    /// › Sync and storage to settle.
    var conflicts: [Conflict] = []
    var status: CoreStatus?
    var syncing = false
    var syncFailure: String?
    var toast: Toast?

    // The settings the look comes from, as the core keeps them.
    var theme = ThemeMode.system
    var accent = ""
    var textSize = 17.0
    /// language is the tag of the language chosen in the settings, or ""
    /// for the system's; shown is the one the app shows, which the windows
    /// draw again in as it changes.
    var language = ""
    private(set) var shown = L.language

    // The settings of sync and agents the app shows, as the core keeps
    // them.
    var allPagesOffline = true
    var workOffline = false
    var agentsReadOtherPages = true

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
    @ObservationIgnored private let scenes = NSHashTable<SceneModel>.weakObjects()
    @ObservationIgnored private let front = NSHashTable<SceneModel>.weakObjects()

    /// pageListeners hear the events of the pages open in the windows.
    @ObservationIgnored private var pageListeners: [UUID: (CoreEvent) -> Void] = [:]
    @ObservationIgnored private var reloading = false
    @ObservationIgnored private var reloadAgain = false

    init() {
        Fonts.register()
        // Before the core starts: it can't find the device's zone itself.
        let zone = TimeZone.current
        MobileSetTimeZone(zone.identifier, zone.secondsFromGMT())
        let folder = ICloudFolder()
        do {
            let dir = try FileManager.default.url(
                for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil,
                create: true
            ).appending(path: "Leaflet", directoryHint: .isDirectory)
            client = try CoreClient.open(
                dataDir: dir, iCloudRoot: folder.root?.path(percentEncoded: false),
                locale: L.language)
            openFailure = nil
        } catch {
            client = nil
            openFailure = error.localizedDescription
        }
        iCloud = ICloudSync(client: client, folder: folder)
        guard let client else { return }
        BackgroundSync.register(client)
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
        case .changed(let table, _):
            if ["pages", "folders", "page_contents", "spaces", "session"].contains(table) {
                Task { await reload() }
            } else if table == "conflicts" {
                Task { await refreshConflicts() }
            } else if table == "recordings" || table == "*" {
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
        default:
            break
        }
        for fn in pageListeners.values { fn(event) }
    }

    // MARK: Library

    /// reload reads the library, the to-dos and the status again. Changes
    /// that come while it reads read once more after.
    func reload() async {
        guard let client else { return }
        if reloading {
            reloadAgain = true
            return
        }
        reloading = true
        defer { reloading = false }
        repeat {
            reloadAgain = false
            if let lib = try? await client.library() {
                libraryLoaded = true
                library = lib
            }
            if let list = try? await client.todos() { todos = list }
            if let list = try? await client.templates() { templates = list }
            await refreshConflicts()
            await refreshStatus()
        } while reloadAgain
    }

    func refreshConflicts() async {
        if let list = try? await client?.conflicts() { conflicts = list }
    }

    func refreshStatus() async {
        status = try? await client?.status()
    }

    func syncNow() async {
        do {
            try await client?.syncNow()
            syncFailure = nil
        } catch {
            syncFailure = error.localizedDescription
        }
        await reload()
    }

    /// createPage makes a page, under parentID or in folderID, and returns
    /// its ID, for the window to open it; link puts a card for a subpage
    /// at the end of its parent. It starts from the template its parent or
    /// folder names for new pages, as on the desktop.
    func createPage(parentID: String = "", folderID: String = "", link: Bool = false) async
        -> String?
    {
        do {
            let page = try await client?.createPage(
                NewPage(parentID: parentID, folderID: folderID, link: link, start: true))
            await reload()
            return page?.id
        } catch {
            show(Toast(message: L.appErrorCreatePage(error: error.localizedDescription)))
            return nil
        }
    }

    func createFolder(named name: String) async {
        do {
            _ = try await client?.createFolder(name)
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
        }
    }

    func rename(_ page: PageRow, to title: String) async {
        try? await client?.renamePage(page.id, title: title)
        await reload()
    }

    func renameFolder(_ folder: FolderRow, to name: String) async {
        try? await client?.renameFolder(folder.id, name: name)
        await reload()
    }

    /// delete deletes a page and its subpages, with Undo in a toast: what
    /// was typed in each window goes first, for Undo to bring the pages
    /// back with it.
    func delete(_ page: PageRow) async {
        guard let client else { return }
        for scene in scenes.allObjects { await scene.flush() }
        do {
            let ids = try await client.deletePage(page.id)
            await reload()
            show(
                Toast(
                    message: L.appDeleted(
                        title: page.title.isEmpty ? L.commonUntitled : page.title),
                    actionLabel: L.commonUndo,
                    action: { [weak self] in
                        try? await client.restorePages(ids)
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.sidebarCouldNotDelete(error: error.localizedDescription)))
        }
    }

    func deleteFolder(_ folder: FolderRow) async {
        do {
            try await client?.deleteFolder(folder.id)
            await reload()
            show(Toast(message: L.sidebarFolderDeleted(name: folder.name)))
        } catch {
            show(Toast(message: L.sidebarCouldNotDelete(error: error.localizedDescription)))
        }
    }

    /// renameTag renames a tag on every page the user may change, and
    /// returns its name as kept, or nil when nothing was renamed.
    func renameTag(_ tag: String, to name: String) async -> String? {
        do {
            let kept = try await client?.renameTag(tag, to: name) ?? ""
            await reload()
            return kept.isEmpty ? nil : kept
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
            return nil
        }
    }

    /// deleteTag takes a tag off every page the user may change, with
    /// Undo in a toast to put each page's tags back as they were.
    func deleteTag(_ tag: String) async {
        guard let client else { return }
        do {
            let before = try await client.deleteTag(tag)
            await reload()
            show(
                Toast(
                    message: L.tagDeleted(name: tag), actionLabel: L.commonUndo,
                    action: { [weak self] in
                        do {
                            try await client.restoreTags(before)
                        } catch {
                            self?.show(
                                Toast(
                                    message: L.sidebarCouldNotSave(
                                        error: error.localizedDescription)))
                        }
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.sidebarCouldNotSave(error: error.localizedDescription)))
        }
    }

    // MARK: Templates and copies

    /// applyTemplate fills a blank page from a template; an untitled page
    /// takes its title.
    func applyTemplate(_ key: String, to pageID: String) async {
        do {
            _ = try await client?.applyTemplate(pageID, key: key)
            await reload()
        } catch {
            show(Toast(message: L.templateUseFailed(error: error.localizedDescription)))
        }
    }

    /// insertTemplate puts a template's blocks in a page after a block, as
    /// the / menu does.
    func insertTemplate(_ key: String, into pageID: String, after blockID: String) async {
        do {
            _ = try await client?.insertTemplate(pageID, after: blockID, key: key)
            await reload()
        } catch {
            show(Toast(message: L.templateUseFailed(error: error.localizedDescription)))
        }
    }

    /// editTemplate returns the page of a template to open and change.
    func editTemplate(_ template: TemplateRow) async -> String? {
        do {
            let id = try await client?.editTemplate(template.key)
            await reload()
            return id
        } catch {
            show(Toast(message: L.templateSaveFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// deleteTemplate deletes one of the user's templates as a page, or
    /// hides a starter on this device, with Undo.
    func deleteTemplate(_ template: TemplateRow) async {
        if let id = template.pageID, let page = library.page(id) {
            await delete(page)
            return
        }
        do {
            try await client?.setStarterDeleted(template.key, deleted: true)
            await reload()
            show(
                Toast(
                    message: L.appDeleted(title: template.title), actionLabel: L.commonUndo,
                    action: { [weak self] in
                        try? await self?.client?.setStarterDeleted(template.key, deleted: false)
                        await self?.reload()
                    }))
        } catch {
            show(Toast(message: L.appErrorDelete(error: error.localizedDescription)))
        }
    }

    /// setDefaultTemplate chooses the template new pages under a page or in
    /// a folder start from, nil for blank ones.
    func setDefaultTemplate(
        _ template: TemplateRow?, page: PageRow? = nil, folder: FolderRow? = nil
    ) async {
        do {
            let message =
                try await client?.setDefaultTemplate(
                    pageID: page?.id ?? "", folderID: folder?.id ?? "", key: template?.key ?? "")
                ?? ""
            await reload()
            show(Toast(message: message))
        } catch {
            show(Toast(message: L.templateDefaultFailed(error: error.localizedDescription)))
        }
    }

    /// defaultTemplate is the template a page or folder names for new
    /// pages, if the user has it.
    func defaultTemplate(_ id: String?) -> TemplateRow? {
        guard let id, !id.isEmpty else { return nil }
        return templates.first { $0.pageID == id }
    }

    /// duplicate copies a page and its subpages beside it, and returns the
    /// copy's ID.
    func duplicate(_ page: PageRow) async -> String? {
        do {
            let copy = try await client?.duplicatePage(page.id)
            await reload()
            return copy?.id
        } catch {
            show(Toast(message: L.templateDuplicateFailed(error: error.localizedDescription)))
            return nil
        }
    }

    /// saveAsTemplate keeps a copy of a page as a template, with Edit in
    /// the toast to open it.
    func saveAsTemplate(_ page: PageRow, open: @escaping @MainActor (String) -> Void) async {
        do {
            guard let t = try await client?.saveAsTemplate(page.id) else { return }
            await reload()
            show(
                Toast(
                    message: L.templateSaved(title: t.displayTitle), actionLabel: L.commonEdit,
                    action: { open(t.id) }))
        } catch {
            show(Toast(message: L.templateSaveFailed(error: error.localizedDescription)))
        }
    }

    /// move moves a page under parentID, into folderID, or to the top
    /// level with neither, after the pages already there.
    func move(_ page: PageRow, parentID: String, folderID: String) async {
        let siblings = library.pages.filter {
            $0.id != page.id && !$0.template && ($0.parentID ?? "") == parentID
                && (parentID.isEmpty ? ($0.folderID ?? "") == folderID : true)
        }
        let last = siblings.max { ($0.sortKey, $0.id) < ($1.sortKey, $1.id) }
        do {
            try await client?.movePage(
                page.id, parentID: parentID, folderID: folderID, afterID: last?.id ?? "")
            await reload()
        } catch {
            show(Toast(message: L.sidebarCouldNotMove(error: error.localizedDescription)))
        }
    }

    /// syncs reports whether pages go anywhere but this device, through
    /// an account or iCloud: only then is keeping one here a choice.
    var syncs: Bool { status?.signedIn ?? false || status?.iCloudOn ?? false }

    func setKeepOffline(_ page: PageRow, _ keep: Bool) async {
        try? await client?.setKeepOffline(page.id, keep: keep)
        await reload()
    }

    func show(_ toast: Toast) {
        self.toast = toast
        let id = toast.id
        Task {
            try? await Task.sleep(for: .seconds(toast.action == nil ? 3 : 6))
            if self.toast?.id == id { self.toast = nil }
        }
    }

    // MARK: Settings

    func loadSettings() async {
        guard let raw = try? await client?.settings(),
            let s = try? JSONDecoder().decode(LookSettings.self, from: Data(raw.utf8))
        else { return }
        theme = ThemeMode(rawValue: s.theme ?? "") ?? .system
        accent = s.accent ?? ""
        if let size = s.textSize, size > 0 { textSize = size }
        allPagesOffline = s.allPagesOffline ?? true
        workOffline = s.workOffline ?? false
        agentsReadOtherPages = s.agentsReadOtherPages ?? true
        if (s.language ?? "") != language || L.language != shown {
            language = s.language ?? ""
            await showLanguage()
        }
    }

    /// saveLook keeps the theme, the accent and the text size.
    func saveLook() {
        save(["theme": theme.rawValue, "accent": accent, "text_size": textSize])
    }

    /// setLanguage shows the app in language, a tag of the catalogs or ""
    /// for the system's, and keeps it, as the desktop's settings do. The
    /// system's own words (its menus and pickers) follow from the next
    /// launch.
    func setLanguage(_ tag: String) {
        guard tag != language else { return }
        language = tag
        save(["language": tag])
        if tag.isEmpty {
            UserDefaults.standard.removeObject(forKey: "AppleLanguages")
        } else {
            UserDefaults.standard.set([tag], forKey: "AppleLanguages")
        }
        Task { await showLanguage() }
    }

    /// showLanguage has the app and the core say their words in the
    /// language chosen, and the windows draw again in it.
    private func showLanguage() async {
        L.use(language)
        await client?.setLocale(L.language)
        shown = L.language
        await reload()
    }

    /// save keeps the settings given by their JSON names; the others stay
    /// as they are.
    func save(_ settings: [String: any Sendable]) {
        guard let client,
            let data = try? JSONSerialization.data(withJSONObject: settings)
        else { return }
        Task { try? await client.saveSettings(String(decoding: data, as: UTF8.self)) }
    }

    // MARK: Lifecycle

    /// foreground starts sync as a window comes to the front.
    func foreground(_ scene: SceneModel) {
        scenes.add(scene)
        front.add(scene)
        guard let client else { return }
        Task {
            await client.start()
            await reload()
        }
    }

    /// background saves what is being typed and sends what waits to go up,
    /// as the app leaves the screen, in the time the system gives it: as
    /// its last window goes to the background. Sync stops then, unless a
    /// window came back to the front meanwhile.
    func background(_ scene: SceneModel) {
        front.remove(scene)
        guard front.allObjects.isEmpty, let client else { return }
        let task = BackgroundTask()
        // Out of time, the pass is cut short, and what it didn't send goes
        // on the next; the system ends an app whose task outlives it.
        task.begin("Save and sync") {
            Task.detached { await client.stop() }
        }
        client.stopWatchingICloud()
        BackgroundSync.schedule()
        Task {
            _ = await client.saveOpenPages()
            try? await client.syncNow()
            if front.allObjects.isEmpty { await client.stop() }
            task.end()
        }
    }
}

/// BackgroundTask is time the system gives the app to finish its work in
/// the background, ended once: when the work is done, or as the time runs
/// out.
@MainActor
private final class BackgroundTask {
    private var id = UIBackgroundTaskIdentifier.invalid

    /// begin asks for the time; expired runs as it runs out, before the
    /// task ends.
    func begin(_ name: String, expired: @escaping @MainActor () -> Void) {
        id = UIApplication.shared.beginBackgroundTask(withName: name) { [self] in
            MainActor.assumeIsolated {
                expired()
                end()
            }
        }
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}

/// LookSettings are the settings the app reads at launch: those its look
/// comes from, and those of sync and agents it shows.
private struct LookSettings: Decodable {
    var theme: String?
    var accent: String?
    var textSize: Double?
    var language: String?
    var allPagesOffline: Bool?
    var workOffline: Bool?
    var agentsReadOtherPages: Bool?

    enum CodingKeys: String, CodingKey {
        case theme, accent, language
        case textSize = "text_size"
        case allPagesOffline = "all_pages_offline"
        case workOffline = "work_offline"
        case agentsReadOtherPages = "agents_read_other_pages"
    }
}
