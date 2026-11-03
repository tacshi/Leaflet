import UIKit

/// Sync's starts and stops as the app's windows come and go: it runs while
/// a window is in front, and as the last goes to the background it sends
/// what waits to go up, in the time the system gives it.
extension AppModel {
    /// foreground starts sync as the app comes to the front: as a window
    /// does with none in front before. One back from a moment inactive, as
    /// under Control Center, or a second window, finds it running.
    func foreground(_ scene: SceneModel) {
        scenes.add(scene)
        let away = front.allObjects.isEmpty
        front.add(scene)
        guard away else { return }
        turn { client in await client.start() }
        Task { await reload() }
    }

    /// background saves what is being typed and sends what waits to go up,
    /// as the app leaves the screen, in the time the system gives it: as
    /// its last window goes to the background. Sync stops then, unless a
    /// window came back to the front meanwhile.
    func background(_ scene: SceneModel) {
        front.remove(scene)
        guard front.allObjects.isEmpty else { return }
        let task = BackgroundTask()
        // Out of time, the pass is cut short, and what it didn't send goes
        // on the next; the system ends an app whose task outlives it.
        task.begin("Save and sync") { [weak self] in
            guard let self, front.allObjects.isEmpty, let client else { return }
            Task.detached { await client.stop() }
        }
        BackgroundSync.schedule()
        // After a start asked for before, which watches iCloud again.
        turn { [weak self] client in
            guard self?.front.allObjects.isEmpty ?? true else { return }
            client.stopWatchingICloud()
        }
        Task { [weak self] in
            guard let client = await self?.opened() else {
                task.end()
                return
            }
            // What each window's editor holds back for a moment, and a
            // title being typed, go first: killed while away, the app
            // would lose them.
            for scene in self?.scenes.allObjects ?? [] { await scene.flush() }
            _ = await client.saveOpenPages()
            try? await client.syncNow()
            self?.turn { [weak self] client in
                if self?.front.allObjects.isEmpty ?? true { await client.stop() }
                task.end()
            }
        }
    }

    /// turn runs body with the core after sync's starts and stops asked for
    /// before it, once the core is open.
    func turn(_ body: @escaping @MainActor (CoreClient) async -> Void) {
        lifecycle = Task { [previous = lifecycle, weak self] in
            await previous?.value
            guard let client = await self?.opened() else { return }
            await body(client)
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
