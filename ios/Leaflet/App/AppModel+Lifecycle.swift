import UIKit

extension AppModel {
    func foreground(_ scene: SceneModel) {
        scenes.add(scene)
        let away = front.allObjects.isEmpty
        front.add(scene)
        guard away else { return }
        turn { client in await client.start() }
        Task {
            await reload()
            remind()
        }
    }

    func background(_ scene: SceneModel) {
        front.remove(scene)
        guard front.allObjects.isEmpty else { return }
        let task = BackgroundTask()
        let leaving = leavings.begin()
        task.begin("Save and sync") { [weak self] in
            guard let self, stays(away: leaving), let client else { return }
            Task.detached { await client.stop() }
        }
        BackgroundSync.schedule()
        turn { [weak self] client in
            guard self?.front.allObjects.isEmpty ?? true else { return }
            client.stopWatchingICloud()
        }
        Task { [weak self] in
            guard let client = await self?.opened() else {
                task.end()
                return
            }
            for scene in self?.scenes.allObjects ?? [] { await scene.flush() }
            _ = await client.saveOpenPages()
            try? await client.syncNow()
            await Reminders.schedule(client, asking: false)
            self?.turn { [weak self] client in
                if self?.stays(away: leaving) ?? true { await client.stop() }
                task.end()
            }
        }
    }

    private func stays(away leaving: Int) -> Bool {
        front.allObjects.isEmpty && leavings.isLatest(leaving)
    }

    func turn(_ body: @escaping @MainActor (CoreClient) async -> Void) {
        lifecycle = Task { [previous = lifecycle, weak self] in
            await previous?.value
            guard let client = await self?.opened() else { return }
            await body(client)
        }
    }
}

struct Leavings {
    private var count = 0

    mutating func begin() -> Int {
        count += 1
        return count
    }

    func isLatest(_ leaving: Int) -> Bool { leaving == count }
}

@MainActor
final class BackgroundTask {
    private var id = UIBackgroundTaskIdentifier.invalid

    var running: Bool { id != .invalid }

    func begin(_ name: String, expired: @escaping @MainActor () -> Void = {}) {
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
