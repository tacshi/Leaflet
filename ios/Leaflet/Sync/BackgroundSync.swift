import BackgroundTasks
import Foundation
import Synchronization
import os

/// BackgroundSync runs a sync pass now and then while the app is in the
/// background, as often as the system lets it, so pages from the other
/// devices are there when the app opens and changes made here went up.
enum BackgroundSync {
    /// identifier names the task, as Info.plist permits it.
    static let identifier = (Bundle.main.bundleIdentifier ?? "app.capyvibe.leaflet") + ".sync"

    @MainActor private static var registered = false

    /// register has the system run sync passes through the core opened
    /// gives, once it is open; inFront says a window is in front. It must
    /// run before the app finishes launching, once, which may be before
    /// the core is open.
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

    /// outOfTime stops sync as a pass's time runs out, unless a window came
    /// to the front meanwhile, which sync goes on for: the app's start
    /// found it running then, and nothing would start it again.
    @MainActor
    static func outOfTime(
        inFront: @MainActor () -> Bool, stop: @MainActor () async -> Void
    ) async {
        if !inFront() { await stop() }
    }

    /// schedule asks for a pass in a quarter of an hour at the earliest,
    /// as the app goes to the background.
    static func schedule() {
        let request = BGAppRefreshTaskRequest(identifier: identifier)
        request.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60)
        do {
            try BGTaskScheduler.shared.submit(request)
        } catch {
            // The simulator runs no background tasks.
            Logger.app.info("Background sync not scheduled: \(error.localizedDescription)")
        }
    }

    /// run runs a pass and asks for the next. Out of time, the pass is
    /// cut short; what it didn't send goes on the next. The system calls
    /// the task's expiration handler on a queue of its own, so its
    /// closures belong to no actor.
    @MainActor
    private static func run(
        _ task: BGTask, _ inFront: @escaping @MainActor () -> Bool,
        _ opened: @escaping @MainActor () async -> CoreClient?
    ) {
        schedule()
        let done = Completion(task)
        let pass = Task {
            guard let client = await opened(), !Task.isCancelled else {
                done.complete(success: false)
                return
            }
            // Out of time now, sync stops (outOfTime), and the pass ends as
            // it does; the task is complete at once all the same, as a pass
            // may be waiting on what no stop cuts short, as a file iCloud
            // Drive has yet to download: left running, iOS refreshes less.
            task.expirationHandler = { @Sendable in
                Task { @MainActor in
                    await outOfTime(inFront: inFront) { await client.stop() }
                }
                done.complete(success: false)
            }
            do {
                try await client.syncNow()
                done.complete(success: true)
            } catch {
                done.complete(success: false)
            }
        }
        // Out of time before the core opened, the pass gives up at once:
        // opening the core doesn't stop for it.
        task.expirationHandler = { @Sendable in
            pass.cancel()
            done.complete(success: false)
        }
    }
}

/// Completion completes a background task once, from any thread: as its
/// pass ends, or as its time runs out first.
private final class Completion: @unchecked Sendable {
    // The task is only told it is complete, once, under the lock.
    private let task: BGTask
    private let completed = Mutex(false)

    init(_ task: BGTask) { self.task = task }

    func complete(success: Bool) {
        let first = completed.withLock { done in
            defer { done = true }
            return !done
        }
        if first { task.setTaskCompleted(success: success) }
    }
}
