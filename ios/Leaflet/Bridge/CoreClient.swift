import Foundation
import LeafletCore
import Synchronization
import os

/// CoreEvent is what changes in the core, as the app hears it.
enum CoreEvent: Sendable {
    /// changed says a table changed, and the page concerned, or "".
    case changed(table: String, pageID: String)
    /// syncState says a pass started or ended, with why it failed.
    case syncState(running: Bool, failure: String?)
    /// pageUpdated gives an open page's content after a change from
    /// outside the editor: "external", "agent" or "merge". Its JSON is
    /// read as it comes, on the core's thread, not the main one.
    case pageUpdated(
        pageID: String, content: DocContent, rev: Int64, reason: String, newStep: Bool)
    case pageReadOnly(pageID: String, readOnly: Bool)
    /// pageSaving says whether an open page's edits wait to be saved.
    case pageSaving(pageID: String, saving: Bool)
    /// tint gives the blocks an agent run last changed.
    case tint(pageID: String, ids: [String])
    /// notice tells the user something.
    case notice(String)
    /// agentsChanged says the agent state of a page changed, or of every
    /// page for "".
    case agentsChanged(pageID: String)
    /// recording gives the recording in progress, and the transcriptions
    /// running, as they change; recordingLevels its latest levels.
    case recording(RecordingState)
    case recordingLevels([Double])
}

/// CoreClient is the Go core, as the app calls it. Every call blocks
/// until the core answers, so each runs off the main thread, and off
/// Swift's own threads, which must not wait. Calls reach the core one at
/// a time, in the order they are made, as the core runs them on its own
/// queue: a later read can't come back with what was there before an
/// earlier write, nor an earlier read land after a later one. The calls
/// that can take long step out of line (waiting), for the others not to
/// wait behind them: those that wait on the network or the user, and
/// those that copy a file or read many versions; their order among the
/// others is then not kept.
final class CoreClient: Sendable {
    let events: AsyncStream<CoreEvent>
    private let core: CoreRef
    let platform: PlatformBridge
    private let listener: EventBridge

    /// open opens the core over the database in dataDir. It waits while
    /// the database opens, and after an update migrates, which can take a
    /// while: the app calls it off the main thread.
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

    /// call runs body on a thread that may wait for the core, after the
    /// calls made before it. It takes its turn as it is called, on the
    /// caller's actor, before it first suspends: calls made one after
    /// another from the main actor reach the core in that order, even
    /// from tasks of their own, as the methods here call it on their
    /// caller's actor too (nonsending).
    ///
    /// waiting runs it beside the others instead, for a call that waits
    /// on the network or the user, such as a sync pass or a sign-in: the
    /// calls after it don't wait for it, and nothing orders it among them.
    nonisolated(nonsending) func call<T: Sendable>(
        waiting: Bool = false, _ body: @escaping @Sendable (MobileCore) throws -> T
    ) async throws -> T {
        let ref = core
        let queue = waiting ? Self.beside : Self.inOrder
        return try await withCheckedThrowingContinuation { done in
            queue.async { done.resume(with: Result { try body(ref.core) }) }
        }
    }

    /// call runs body as the call above does and decodes the JSON it
    /// returns as type, there too: off the caller's thread, which may be
    /// the main one.
    nonisolated(nonsending) func call<T: Decodable & Sendable>(
        _ type: T.Type, waiting: Bool = false,
        _ body: @escaping @Sendable (MobileCore) throws -> String
    ) async throws -> T {
        try await call(waiting: waiting) { core in
            try JSONDecoder().decode(type, from: Data(try body(core).utf8))
        }
    }

    /// cancellable runs body as a waiting call, or in order with inOrder,
    /// with a token by which the core gives up on it as the task that
    /// called it is cancelled: a download for a page that closed holds a
    /// thread no longer, and a search typed over stops. One given up on
    /// before its turn comes never reaches the core, and throws
    /// CancellationError: a search typed over would otherwise wait its
    /// turn for an answer no one reads, and hold up the saves after it.
    nonisolated(nonsending) func cancellable<T: Sendable>(
        inOrder: Bool = false, _ body: @escaping @Sendable (MobileCore, Int64) throws -> T
    ) async throws -> T {
        try Task.checkCancellation()
        let token = Self.tokens.add(1, ordering: .relaxed).newValue
        let ref = core
        let turn = Turn()
        return try await withTaskCancellationHandler {
            try await call(waiting: !inOrder) { core in
                guard turn.begin() else { throw CancellationError() }
                defer { turn.end() }
                return try body(core, token)
            }
        } onCancel: {
            if turn.giveUp() { ref.core.cancel(token) }
        }
    }

    /// tokens numbers the calls that may be given up on.
    private static let tokens = Atomic<Int64>(0)

    /// inOrder runs the calls one at a time, as they are made; beside, the
    /// calls that wait, at once.
    private static let inOrder = DispatchQueue(
        label: "app.capyvibe.leaflet.core", qos: .userInitiated)
    private static let beside = DispatchQueue.global(qos: .userInitiated)

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

    /// start starts sync and watching iCloud Drive's folder, as the app
    /// comes to the front: the folder may be watched from now on.
    nonisolated(nonsending) func start() async {
        platform.setInFront(true)
        try? await call { $0.start() }
    }
    /// stop cuts short a pass running, SyncNow's too, so it doesn't wait
    /// behind one.
    nonisolated(nonsending) func stop() async { try? await call(waiting: true) { $0.stop() } }
    nonisolated(nonsending) func kick() async { try? await call { $0.kick() } }
    nonisolated(nonsending) func syncNow() async throws {
        try await call(waiting: true) { try $0.syncNow() }
    }
    nonisolated(nonsending) func saveOpenPages() async -> Bool {
        (try? await call { $0.saveOpenPages() }) ?? false
    }

    /// conflicts are the blocks two devices changed at once, as sync kept
    /// them, newest first.
    nonisolated(nonsending) func conflicts() async throws -> [Conflict] {
        try await call([Conflict].self) { core in try Self.answer { e in core.conflicts(e) } }
    }

    nonisolated(nonsending) func resolveConflict(_ id: String, how: Conflict.Resolution)
        async throws
    {
        try await call { try $0.resolveConflict(id, how: how.rawValue) }
    }

    nonisolated(nonsending) func status() async throws -> CoreStatus {
        try await call(CoreStatus.self) { core in try Self.answer { e in core.status(e) } }
    }

    // MARK: Library

    nonisolated(nonsending) func library() async throws -> LibrarySnapshot {
        try await call(LibrarySnapshot.self) { core in try Self.answer { e in core.library(e) } }
    }

    /// A page that starts from a template waits beside the other calls,
    /// as applyTemplate does: what its files need may download first.
    nonisolated(nonsending) func createPage(_ page: NewPage) async throws -> PageRow {
        let json = try encode(page)
        return try await call(PageRow.self, waiting: page.start) { core in
            try Self.answer { e in core.createPage(json, error: e) }
        }
    }

    nonisolated(nonsending) func renamePage(_ id: String, title: String) async throws {
        try await call { try $0.renamePage(id, title: title) }
    }

    nonisolated(nonsending) func movePageBefore(
        _ id: String, parentID: String = "", folderID: String = "", beforeID: String = ""
    ) async throws {
        try await call {
            try $0.movePage(before: id, parentID: parentID, folderID: folderID, beforeID: beforeID)
        }
    }

    nonisolated(nonsending) func movePage(
        _ id: String, parentID: String = "", folderID: String = "", afterID: String = ""
    )
        async throws
    {
        try await call {
            try $0.movePage(id, parentID: parentID, folderID: folderID, afterID: afterID)
        }
    }

    /// deletePage deletes a page and its subpages, returning what
    /// restorePages takes to bring them back.
    nonisolated(nonsending) func deletePage(_ id: String) async throws -> String {
        try await call { core in try Self.answer { e in core.deletePage(id, error: e) } }
    }

    nonisolated(nonsending) func restorePages(_ ids: String) async throws {
        try await call { try $0.restorePages(ids) }
    }

    nonisolated(nonsending) func setTags(_ id: String, tags: [String]) async throws {
        let json = try encode(tags)
        try await call { try $0.setPageTags(id, tags: json) }
    }

    /// renameTag renames a tag on every page the user may change, and
    /// returns the name as kept, or "" for a name of nothing.
    nonisolated(nonsending) func renameTag(_ old: String, to name: String) async throws -> String {
        try await call { core in try Self.answer { e in core.renameTag(old, name: name, error: e) }
        }
    }

    /// deleteTag takes a tag off every page the user may change, returning
    /// what restoreTags takes to put it back.
    nonisolated(nonsending) func deleteTag(_ tag: String) async throws -> String {
        try await call { core in try Self.answer { e in core.deleteTag(tag, error: e) } }
    }

    nonisolated(nonsending) func restoreTags(_ before: String) async throws {
        try await call { try $0.restoreTags(before) }
    }

    nonisolated(nonsending) func setKeepOffline(_ id: String, keep: Bool) async throws {
        try await call { try $0.setKeepOffline(id, keep: keep) }
    }

    nonisolated(nonsending) func createFolder(_ name: String) async throws -> FolderRow {
        try await call(FolderRow.self) { core in
            try Self.answer { e in core.createFolder(name, error: e) }
        }
    }

    nonisolated(nonsending) func renameFolder(_ id: String, name: String) async throws {
        try await call { try $0.renameFolder(id, name: name) }
    }

    nonisolated(nonsending) func deleteFolder(_ id: String) async throws {
        try await call { try $0.deleteFolder(id) }
    }

    /// search finds query's hits. A search whose task is cancelled, as one
    /// typed over is, stops, or never reaches the core if its turn has
    /// not come (cancellable).
    nonisolated(nonsending) func search(_ query: String, limit: Int = 50) async throws
        -> [SearchHit]
    {
        try await cancellable(inOrder: true) { core, token in
            try JSONDecoder().decode(
                [SearchHit].self,
                from: Data(
                    try Self.answer { e in
                        core.searchCancellable(query, limit: limit, token: token, error: e)
                    }.utf8))
        }
    }

    /// todos are the open to-dos with a due date or an owner, in the
    /// To-dos tab's sections, as the desktop sorts them.
    nonisolated(nonsending) func todos() async throws -> [TodoGroup] {
        try await call([TodoGroup].self) { core in try Self.answer { e in core.todos(e) } }
    }

    /// reminders are the to-dos' notifications to schedule, at now.
    nonisolated(nonsending) func reminders(now: Date, limit: Int) async throws -> [Reminder] {
        let ms = Int64(now.timeIntervalSince1970 * 1000)
        return try await call([Reminder].self) { core in
            try Self.answer { e in core.reminders(ms, limit: limit, error: e) }
        }
    }

    /// remindedOf keeps that these were scheduled, for each to be reminded
    /// of once.
    nonisolated(nonsending) func remindedOf(_ reminders: [Reminder]) async throws {
        let json = try encode(reminders)
        try await call { try $0.reminded(of: json) }
    }

    /// moveTargets are the pages a page can move under, the one changed
    /// last first, by ID.
    nonisolated(nonsending) func moveTargets(_ id: String) async throws -> [String] {
        try await call([String].self) { core in
            try Self.answer { e in core.moveTargets(id, error: e) }
        }
    }

    // MARK: Settings

    /// settings returns the settings on this device, as JSON.
    nonisolated(nonsending) func settings() async throws -> String {
        try await call { core in try Self.answer { e in core.settings(e) } }
    }

    /// setLocale says the core's messages in language, a tag of the
    /// catalogs.
    nonisolated(nonsending) func setLocale(_ language: String) async {
        try? await call { $0.setLocale(language) }
    }

    /// saveSettings keeps the settings given, as JSON; those it leaves out
    /// stay as they are.
    nonisolated(nonsending) func saveSettings(_ json: String) async throws {
        try await call { try $0.saveSettings(json) }
    }

    // MARK: Open pages

    nonisolated(nonsending) func openPage(_ id: String) async throws -> OpenedPage {
        try await call(OpenedPage.self) { core in
            try Self.answer { e in core.openPage(id, error: e) }
        }
    }

    /// editPage gives the core the page's content after an edit in
    /// editor, made over rev, and returns the rev it left the page at,
    /// which has the edit: a change at an older one is from before it.
    @discardableResult
    nonisolated(nonsending) func editPage(
        _ id: String, editor: Int64, content: DocContent, rev: Int64
    ) async throws -> Int64 {
        // Encoded off the caller's thread, the main one, with the call.
        try await call { core in
            let json = try self.encode(content)
            var left: Int64 = 0
            try core.editPage(id, editor: editor, content: json, rev: rev, left: &left)
            return left
        }
    }

    /// members are who a page is shared with, as this device knows.
    nonisolated(nonsending) func members(_ pageID: String) async throws -> [Member] {
        try await call([Member].self) { core in
            try Self.answer { e in core.members(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func closePage(_ id: String, editor: Int64) async {
        try? await call { $0.closePage(id, editor: editor) }
    }
}

/// Turn is where a call that may be given up on is, from any thread:
/// waiting for its turn, under way in the core, given up on first, or
/// done.
private final class Turn: Sendable {
    private let state = Atomic<Int>(0)
    private static let waiting = 0
    private static let running = 1
    private static let givenUp = 2
    private static let done = 3

    /// begin starts the call as its turn comes, and reports whether it
    /// may, not having been given up on.
    func begin() -> Bool {
        state.compareExchange(
            expected: Self.waiting, desired: Self.running, ordering: .acquiringAndReleasing
        ).exchanged
    }

    /// end says the call is done: given up on after, there is nothing to
    /// tell the core.
    func end() { state.store(Self.done, ordering: .releasing) }

    /// giveUp gives the call up, and reports whether it is under way in
    /// the core, which is then to be told: one still waiting never goes.
    func giveUp() -> Bool {
        !state.compareExchange(
            expected: Self.waiting, desired: Self.givenUp, ordering: .acquiringAndReleasing
        ).exchanged && state.load(ordering: .acquiring) == Self.running
    }
}

/// CoreRef holds the core for calls from any thread, which it takes:
/// gomobile's methods may be called from any thread, and the core runs
/// what they do on a queue of its own.
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
        guard let c = Self.decode(DocContent.self, content, "page") else { return }
        to.yield(
            .pageUpdated(
                pageID: pageID ?? "", content: c, rev: rev, reason: reason ?? "",
                newStep: newStep))
    }

    func pageReadOnly(_ pageID: String?, readOnly: Bool) {
        to.yield(.pageReadOnly(pageID: pageID ?? "", readOnly: readOnly))
    }

    func pageSaving(_ pageID: String?, saving: Bool) {
        to.yield(.pageSaving(pageID: pageID ?? "", saving: saving))
    }

    func tint(_ pageID: String?, ids: String?) {
        let list = Self.decode([String].self, ids ?? "[]", "tint") ?? []
        to.yield(.tint(pageID: pageID ?? "", ids: list))
    }

    func notice(_ message: String?) {
        to.yield(.notice(message ?? ""))
    }

    func agentsChanged(_ pageID: String?) {
        to.yield(.agentsChanged(pageID: pageID ?? ""))
    }

    func recordingChanged(_ state: String?) {
        guard let s = Self.decode(RecordingState.self, state, "recording") else { return }
        to.yield(.recording(s))
    }

    func recordingLevels(_ levels: String?) {
        guard let l = Self.decode([Double].self, levels, "recording levels") else { return }
        to.yield(.recordingLevels(l))
    }

    /// decode reads an event's JSON as type, or logs why it can't, for the
    /// event that is dropped then: one the app can't read means the core
    /// and the app disagree on its form.
    private static func decode<T: Decodable>(_ type: T.Type, _ json: String?, _ what: String)
        -> T?
    {
        do {
            return try JSONDecoder().decode(type, from: Data((json ?? "").utf8))
        } catch {
            Logger.app.error("The core's \(what, privacy: .public) event can't be read: \(error)")
            return nil
        }
    }
}
