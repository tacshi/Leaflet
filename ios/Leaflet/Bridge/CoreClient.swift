import Foundation
import LeafletCore
import Synchronization
import os

enum CoreEvent: Sendable {
    case changed(table: String, pageID: String)
    case syncState(running: Bool, failure: String?)
    case pageUpdated(
        pageID: String, content: DocContent, rev: Int64, reason: String, newStep: Bool)
    case pageReadOnly(pageID: String, readOnly: Bool)
    case pageSaving(pageID: String, saving: Bool)
    case tint(pageID: String, ids: [String])
    case notice(String)
    case agentsChanged(pageID: String)
    case recording(RecordingState)
    case recordingLevels([Double])
}

final class CoreClient: Sendable {
    let events: AsyncStream<CoreEvent>
    private let core: CoreRef
    let platform: PlatformBridge
    private let listener: EventBridge

    static func open(dataDir: URL, iCloudRoot: String?, locale: String) throws -> CoreClient {
        try FileManager.default.createDirectory(at: dataDir, withIntermediateDirectories: true)
        try FileManager.default.setAttributes(
            [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
            ofItemAtPath: dataDir.path)
        let platform = PlatformBridge(service: Bundle.main.bundleIdentifier ?? "Leaflet")
        let (events, continuation) = AsyncStream.makeStream(of: CoreEvent.self)
        let listener = EventBridge(continuation)
        #if DEBUG
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

    nonisolated(nonsending) func call<T: Sendable>(
        waiting: Bool = false, _ body: @escaping @Sendable (MobileCore) throws -> T
    ) async throws -> T {
        let ref = core
        let queue = waiting ? Self.beside : Self.inOrder
        return try await withCheckedThrowingContinuation { done in
            queue.async { done.resume(with: Result { try body(ref.core) }) }
        }
    }

    nonisolated(nonsending) func call<T: Decodable & Sendable>(
        _ type: T.Type, waiting: Bool = false,
        _ body: @escaping @Sendable (MobileCore) throws -> String
    ) async throws -> T {
        try await call(waiting: waiting) { core in
            try JSONDecoder().decode(type, from: Data(try body(core).utf8))
        }
    }

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

    private static let tokens = Atomic<Int64>(0)

    private static let inOrder = DispatchQueue(
        label: "app.capyvibe.leaflet.core", qos: .userInitiated)
    private static let beside = DispatchQueue.global(qos: .userInitiated)

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

    nonisolated(nonsending) func start() async {
        platform.setInFront(true)
        try? await call { $0.start() }
    }
    nonisolated(nonsending) func stop() async { try? await call(waiting: true) { $0.stop() } }
    nonisolated(nonsending) func kick() async { try? await call { $0.kick() } }
    nonisolated(nonsending) func syncNow() async throws {
        try await call(waiting: true) { try $0.syncNow() }
    }
    nonisolated(nonsending) func saveOpenPages() async -> Bool {
        (try? await call { $0.saveOpenPages() }) ?? false
    }

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

    nonisolated(nonsending) func library() async throws -> LibrarySnapshot {
        try await call(LibrarySnapshot.self) { core in try Self.answer { e in core.library(e) } }
    }

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

    nonisolated(nonsending) func renameTag(_ old: String, to name: String) async throws -> String {
        try await call { core in try Self.answer { e in core.renameTag(old, name: name, error: e) }
        }
    }

    nonisolated(nonsending) func deleteTag(_ tag: String) async throws -> String {
        try await call { core in try Self.answer { e in core.deleteTag(tag, error: e) } }
    }

    nonisolated(nonsending) func restoreTags(_ before: String) async throws {
        try await call { try $0.restoreTags(before) }
    }

    nonisolated(nonsending) func setKeepOffline(_ id: String, keep: Bool) async throws {
        try await call { try $0.setKeepOffline(id, keep: keep) }
    }

    nonisolated(nonsending) func createFolder(_ name: String, in parentID: String = "")
        async throws -> FolderRow
    {
        try await call(FolderRow.self) { core in
            try Self.answer { e in core.createFolder(in: name, parentID: parentID, error: e) }
        }
    }

    nonisolated(nonsending) func moveFolder(
        _ id: String, parentID: String = "", afterID: String = ""
    ) async throws {
        try await call { try $0.moveFolder(id, parentID: parentID, afterID: afterID) }
    }

    nonisolated(nonsending) func moveFolderBefore(
        _ id: String, parentID: String = "", beforeID: String = ""
    ) async throws {
        try await call { try $0.moveFolder(before: id, parentID: parentID, beforeID: beforeID) }
    }

    nonisolated(nonsending) func renameFolder(_ id: String, name: String) async throws {
        try await call { try $0.renameFolder(id, name: name) }
    }

    nonisolated(nonsending) func deleteFolder(_ id: String) async throws {
        try await call { try $0.deleteFolder(id) }
    }

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

    nonisolated(nonsending) func todos() async throws -> [TodoGroup] {
        try await call([TodoGroup].self) { core in try Self.answer { e in core.todos(e) } }
    }

    nonisolated(nonsending) func reminders(now: Date, limit: Int) async throws -> [Reminder] {
        let ms = Int64(now.timeIntervalSince1970 * 1000)
        return try await call([Reminder].self) { core in
            try Self.answer { e in core.reminders(ms, limit: limit, error: e) }
        }
    }

    nonisolated(nonsending) func remindedOf(_ reminders: [Reminder]) async throws {
        let json = try encode(reminders)
        try await call { try $0.reminded(of: json) }
    }

    nonisolated(nonsending) func moveTargets(_ id: String) async throws -> [String] {
        try await call([String].self) { core in
            try Self.answer { e in core.moveTargets(id, error: e) }
        }
    }

    nonisolated(nonsending) func settings() async throws -> String {
        try await call { core in try Self.answer { e in core.settings(e) } }
    }

    nonisolated(nonsending) func setLocale(_ language: String) async {
        try? await call { $0.setLocale(language) }
    }

    nonisolated(nonsending) func saveSettings(_ json: String) async throws {
        try await call { try $0.saveSettings(json) }
    }

    nonisolated(nonsending) func openPage(_ id: String) async throws -> OpenedPage {
        try await call(OpenedPage.self) { core in
            try Self.answer { e in core.openPage(id, error: e) }
        }
    }

    @discardableResult
    nonisolated(nonsending) func editPage(
        _ id: String, editor: Int64, content: DocContent, rev: Int64
    ) async throws -> Int64 {
        try await call { core in
            let json = try self.encode(content)
            var left: Int64 = 0
            try core.editPage(id, editor: editor, content: json, rev: rev, left: &left)
            return left
        }
    }

    nonisolated(nonsending) func members(_ pageID: String) async throws -> [Member] {
        try await call([Member].self) { core in
            try Self.answer { e in core.members(pageID, error: e) }
        }
    }

    nonisolated(nonsending) func closePage(_ id: String, editor: Int64) async {
        try? await call { $0.closePage(id, editor: editor) }
    }
}

private final class Turn: Sendable {
    private let state = Atomic<Int>(0)
    private static let waiting = 0
    private static let running = 1
    private static let givenUp = 2
    private static let done = 3

    func begin() -> Bool {
        state.compareExchange(
            expected: Self.waiting, desired: Self.running, ordering: .acquiringAndReleasing
        ).exchanged
    }

    func end() { state.store(Self.done, ordering: .releasing) }

    func giveUp() -> Bool {
        !state.compareExchange(
            expected: Self.waiting, desired: Self.givenUp, ordering: .acquiringAndReleasing
        ).exchanged && state.load(ordering: .acquiring) == Self.running
    }
}

private struct CoreRef: @unchecked Sendable {
    let core: MobileCore
}

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
