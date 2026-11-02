import BackgroundTasks
import Foundation
import os

/// BackgroundSync runs a sync pass now and then while the app is in the
/// background, as often as the system lets it, so pages from the other
/// devices are there when the app opens and changes made here went up.
enum BackgroundSync {
    /// identifier names the task, as Info.plist permits it.
    static let identifier = (Bundle.main.bundleIdentifier ?? "app.capyvibe.leaflet") + ".sync"

    @MainActor private static var registered = false

    /// register has the system run sync passes through the core opened
    /// gives, once it is open. It must run before the app finishes
    /// launching, once, which may be before the core is open.
    @MainActor
    static func register(_ opened: @escaping @MainActor () async -> CoreClient?) {
        guard !registered else { return }
        registered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: identifier, using: .main
        ) { task in
            MainActor.assumeIsolated { run(task, opened) }
        }
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
    /// cut short; what it didn't send goes on the next.
    @MainActor
    private static func run(_ task: BGTask, _ opened: @escaping @MainActor () async -> CoreClient?)
    {
        schedule()
        let pass = Task {
            guard let client = await opened(), !Task.isCancelled else {
                task.setTaskCompleted(success: false)
                return
            }
            task.expirationHandler = {
                Task { await client.stop() }
            }
            do {
                try await client.syncNow()
                task.setTaskCompleted(success: true)
            } catch {
                task.setTaskCompleted(success: false)
            }
        }
        // Out of time before the core opened, the pass gives up.
        task.expirationHandler = { pass.cancel() }
    }
}
