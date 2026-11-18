import BackgroundTasks
import Foundation
import Synchronization
import os

enum BackgroundSync {
    static let identifier = (Bundle.main.bundleIdentifier ?? "app.capyvibe.leaflet") + ".sync"

    @MainActor private static var registered = false

    @MainActor
    static func register(
        inFront: @escaping @MainActor () -> Bool,
        opened: @escaping @MainActor () async -> CoreClient?
    ) {
        guard !registered else { return }
        registered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: identifier, using: .main
        ) { task in
            MainActor.assumeIsolated { run(task, inFront, opened) }
        }
    }

    @MainActor
    static func outOfTime(
        inFront: @MainActor () -> Bool, stop: @MainActor () async -> Void
    ) async {
        if !inFront() { await stop() }
    }

    static func schedule() {
        let request = BGAppRefreshTaskRequest(identifier: identifier)
        request.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60)
        do {
            try BGTaskScheduler.shared.submit(request)
        } catch {
            Logger.app.info("Background sync not scheduled: \(error.localizedDescription)")
        }
    }

    @MainActor
    private static func run(
        _ task: BGTask, _ inFront: @escaping @MainActor () -> Bool,
        _ opened: @escaping @MainActor () async -> CoreClient?
    ) {
        schedule()
        let done = Completion(task)
        let pass = Task {
            guard let client = await opened() else {
                done.complete(success: false)
                return
            }
            done.syncing(client)
            // Given up on, the task is completed as the sync is stopped.
            guard !Task.isCancelled else { return }
            do {
                try await client.syncNow()
                await Reminders.schedule(client, asking: false)
                done.complete(success: true)
            } catch {
                done.complete(success: false)
            }
        }
        // The task is completed only once the sync is stopped: the app may be suspended as soon
        // as it is.
        task.expirationHandler = { @Sendable in
            pass.cancel()
            guard let client = done.client else {
                done.complete(success: false)
                return
            }
            Task { @MainActor in
                await outOfTime(inFront: inFront) { await client.stop() }
                done.complete(success: false)
            }
        }
    }
}

private final class Completion: @unchecked Sendable {
    private let task: BGTask
    private let completed = Mutex(false)
    private let syncer = Mutex<CoreClient?>(nil)

    init(_ task: BGTask) { self.task = task }

    var client: CoreClient? { syncer.withLock { $0 } }

    func syncing(_ client: CoreClient) { syncer.withLock { $0 = client } }

    func complete(success: Bool) {
        let first = completed.withLock { done in
            defer { done = true }
            return !done
        }
        if first { task.setTaskCompleted(success: success) }
    }
}
