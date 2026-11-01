import Foundation
import LeafletCore

/// CoreEvent is what changes in the core, as the app hears it.
enum CoreEvent: Sendable {
    /// changed says a table changed, and the page concerned, or "".
    case changed(table: String, pageID: String)
    /// syncState says a pass started or ended, with why it failed.
    case syncState(running: Bool, failure: String?)
    /// pageUpdated gives an open page's content after a change from
    /// outside the editor: "external", "agent" or "merge".
    case pageUpdated(pageID: String, content: String, rev: Int64, reason: String, newStep: Bool)
    case pageReadOnly(pageID: String, readOnly: Bool)
    /// pageSaving says whether an open page's edits wait to be saved.
    case pageSaving(pageID: String, saving: Bool)
    /// tint gives the blocks an agent run last changed.
    case tint(pageID: String, ids: [String])
    /// notice tells the user something.
    case notice(String)
    /// agentsChanged says an agent's state changed, on some page.
    case agentsChanged
    /// recording gives the recording in progress, and the transcriptions
    /// running, as they change.
    case recording(RecordingState)
}

/// CoreClient is the Go core, as the app calls it. Every call blocks
/// until the core answers, so each runs off the main thread, and off
/// Swift's own threads, which must not wait.
final class CoreClient: Sendable {
    let events: AsyncStream<CoreEvent>
    private let core: CoreRef
    let platform: PlatformBridge
    private let listener: EventBridge

    /// open opens the core over the database in dataDir.
    @MainActor
    static func open(dataDir: URL, iCloudRoot: String?, locale: String) throws -> CoreClient {
        try FileManager.default.createDirectory(at: dataDir, withIntermediateDirectories: true)
        // The database stays readable from the first unlock after a
        // restart, for sync in the background with the screen locked.
        try FileManager.default.setAttributes(
            [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
            ofItemAtPath: dataDir.path)
        let platform = PlatformBridge(service: Bundle.main.bundleIdentifier ?? "Leaflet")
        let (events, continuation) = AsyncStream.makeStream(of: CoreEvent.self)
        let listener = EventBridge(continuation)
        #if DEBUG
            // Leaflet Debug syncs through the project the desktop's
            // development build sets up.
            let dev = true
        #else
            let dev = false
        #endif
        let config = try JSONSerialization.data(withJSONObject: [
            "data_dir": dataDir.path, "icloud_root": iCloudRoot ?? "", "locale": locale,
            "dev": dev,
        ])
        var error: NSError?
        guard
            let core = MobileOpen(
                String(decoding: config, as: UTF8.self), platform, listener, &error)
        else {
            throw error ?? CocoaError(.fileReadUnknown)
        }
        return CoreClient(core: core, platform: platform, listener: listener, events: events)
    }

    private init(
        core: MobileCore, platform: PlatformBridge, listener: EventBridge,
        events: AsyncStream<CoreEvent>
    ) {
        self.core = CoreRef(core: core)
        self.platform = platform
        self.listener = listener
        self.events = events
    }

    /// call runs body on a thread that may wait for the core.
    func call<T: Sendable>(_ body: @escaping @Sendable (MobileCore) throws -> T)
        async throws -> T
    {
        let ref = core
        return try await withCheckedThrowingContinuation { done in
            DispatchQueue.global(qos: .userInitiated).async {
                done.resume(with: Result { try body(ref.core) })
            }
        }
    }

    /// answer returns what a call of the core that fails through an error
    /// pointer returns, or throws its error: gomobile's methods that
    /// return a string do so.
    static func answer<T>(_ body: (NSErrorPointer) -> T) throws -> T {
        var error: NSError?
        let value = body(&error)
        if let error { throw error }
        return value
    }

    func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSONDecoder().decode(type, from: Data(json.utf8))
    }

    private func encode<T: Encodable>(_ value: T) throws -> String {
        String(decoding: try JSONEncoder().encode(value), as: UTF8.self)
    }

    // MARK: Lifecycle and sync

    func start() async { try? await call { $0.start() } }
    func stop() async { try? await call { $0.stop() } }
    func kick() async { try? await call { $0.kick() } }
    func syncNow() async throws { try await call { try $0.syncNow() } }
    func saveOpenPages() async -> Bool { (try? await call { $0.saveOpenPages() }) ?? false }

    /// conflicts are the blocks two devices changed at once, as sync kept
    /// them, newest first.
    func conflicts() async throws -> [Conflict] {
        try decode(
            [Conflict].self, try await call { core in try Self.answer { e in core.conflicts(e) } })
    }

    func resolveConflict(_ id: String, how: Conflict.Resolution) async throws {
        try await call { try $0.resolveConflict(id, how: how.rawValue) }
    }

    func status() async throws -> CoreStatus {
        try decode(
            CoreStatus.self, try await call { core in try Self.answer { e in core.status(e) } })
    }

    // MARK: Library

    func library() async throws -> LibrarySnapshot {
        try decode(
            LibrarySnapshot.self,
            try await call { core in try Self.answer { e in core.library(e) } })
    }

    func createPage(_ page: NewPage) async throws -> PageRow {
        let json = try encode(page)
        return try decode(
            PageRow.self,
            try await call { core in try Self.answer { e in core.createPage(json, error: e) } })
    }

    func renamePage(_ id: String, title: String) async throws {
        try await call { try $0.renamePage(id, title: title) }
    }

    func movePage(_ id: String, parentID: String = "", folderID: String = "", afterID: String = "")
        async throws
    {
        try await call {
            try $0.movePage(id, parentID: parentID, folderID: folderID, afterID: afterID)
        }
    }

    /// deletePage deletes a page and its subpages, returning what
    /// restorePages takes to bring them back.
    func deletePage(_ id: String) async throws -> String {
        try await call { core in try Self.answer { e in core.deletePage(id, error: e) } }
    }

    func restorePages(_ ids: String) async throws {
        try await call { try $0.restorePages(ids) }
    }

    func setTags(_ id: String, tags: [String]) async throws {
        let json = try encode(tags)
        try await call { try $0.setPageTags(id, tags: json) }
    }

    /// renameTag renames a tag on every page the user may change, and
    /// returns the name as kept, or "" for a name of nothing.
    func renameTag(_ old: String, to name: String) async throws -> String {
        try await call { core in try Self.answer { e in core.renameTag(old, name: name, error: e) }
        }
    }

    /// deleteTag takes a tag off every page the user may change, returning
    /// what restoreTags takes to put it back.
    func deleteTag(_ tag: String) async throws -> String {
        try await call { core in try Self.answer { e in core.deleteTag(tag, error: e) } }
    }

    func restoreTags(_ before: String) async throws {
        try await call { try $0.restoreTags(before) }
    }

    func setKeepOffline(_ id: String, keep: Bool) async throws {
        try await call { try $0.setKeepOffline(id, keep: keep) }
    }

    func createFolder(_ name: String) async throws -> FolderRow {
        try decode(
            FolderRow.self,
            try await call { core in try Self.answer { e in core.createFolder(name, error: e) } })
    }

    func renameFolder(_ id: String, name: String) async throws {
        try await call { try $0.renameFolder(id, name: name) }
    }

    func deleteFolder(_ id: String) async throws {
        try await call { try $0.deleteFolder(id) }
    }

    func search(_ query: String, limit: Int = 50) async throws -> [SearchHit] {
        try decode(
            [SearchHit].self,
            try await call { core in
                try Self.answer { e in core.search(query, limit: limit, error: e) }
            })
    }

    func todos() async throws -> [Todo] {
        try decode([Todo].self, try await call { core in try Self.answer { e in core.todos(e) } })
    }

    // MARK: Settings

    /// settings returns the settings on this device, as JSON.
    func settings() async throws -> String {
        try await call { core in try Self.answer { e in core.settings(e) } }
    }

    /// setLocale says the core's messages in language, a tag of the
    /// catalogs.
    func setLocale(_ language: String) async {
        try? await call { $0.setLocale(language) }
    }

    /// saveSettings keeps the settings given, as JSON; those it leaves out
    /// stay as they are.
    func saveSettings(_ json: String) async throws {
        try await call { try $0.saveSettings(json) }
    }

    // MARK: Open pages

    func openPage(_ id: String) async throws -> OpenedPage {
        try decode(
            OpenedPage.self,
            try await call { core in try Self.answer { e in core.openPage(id, error: e) } })
    }

    /// editPage gives the core the page's content after an edit in
    /// editor, made over rev.
    func editPage(_ id: String, editor: Int64, content: DocContent, rev: Int64) async throws {
        let json = try encode(content)
        try await call { try $0.editPage(id, editor: editor, content: json, rev: rev) }
    }

    /// members are who a page is shared with, as this device knows.
    func members(_ pageID: String) async throws -> [Member] {
        try decode(
            [Member].self,
            try await call { core in try Self.answer { e in core.members(pageID, error: e) } })
    }

    func closePage(_ id: String, editor: Int64) async {
        try? await call { $0.closePage(id, editor: editor) }
    }
}

/// CoreRef holds the core for calls from any thread, which it takes:
/// it runs every call on a queue of its own.
private struct CoreRef: @unchecked Sendable {
    let core: MobileCore
}

/// EventBridge hears the core's events, on a thread of the core's, and
/// passes them on in order.
private final class EventBridge: NSObject, MobileEventsProtocol, Sendable {
    private let to: AsyncStream<CoreEvent>.Continuation

    init(_ to: AsyncStream<CoreEvent>.Continuation) { self.to = to }

    func changed(_ table: String?, pageID: String?) {
        to.yield(.changed(table: table ?? "", pageID: pageID ?? ""))
    }

    func syncState(_ running: Bool, failure: String?) {
        to.yield(.syncState(running: running, failure: failure.flatMap { $0.isEmpty ? nil : $0 }))
    }

    func pageUpdated(
        _ pageID: String?, content: String?, rev: Int64, reason: String?, newStep: Bool
    ) {
        to.yield(
            .pageUpdated(
                pageID: pageID ?? "", content: content ?? "", rev: rev, reason: reason ?? "",
                newStep: newStep))
    }

    func pageReadOnly(_ pageID: String?, readOnly: Bool) {
        to.yield(.pageReadOnly(pageID: pageID ?? "", readOnly: readOnly))
    }

    func pageSaving(_ pageID: String?, saving: Bool) {
        to.yield(.pageSaving(pageID: pageID ?? "", saving: saving))
    }

    func tint(_ pageID: String?, ids: String?) {
        let list = (try? JSONDecoder().decode([String].self, from: Data((ids ?? "[]").utf8))) ?? []
        to.yield(.tint(pageID: pageID ?? "", ids: list))
    }

    func notice(_ message: String?) {
        to.yield(.notice(message ?? ""))
    }

    func agentsChanged() {
        to.yield(.agentsChanged)
    }

    func recordingChanged(_ state: String?) {
        guard
            let s = try? JSONDecoder().decode(
                RecordingState.self, from: Data((state ?? "").utf8))
        else { return }
        to.yield(.recording(s))
    }
}
