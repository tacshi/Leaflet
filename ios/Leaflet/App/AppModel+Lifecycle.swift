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
            let pages = agentPages
            let stopped = DispatchSemaphore(value: 0)
            Task.detached(priority: .userInitiated) {
                await Self.stopAgents(on: pages, client)
                stopped.signal()
                await client.stop()
            }
            // The app is suspended once this returns, so the runs are stopped before it does.
            _ = stopped.wait(timeout: .now() + AgentHold.lastWait)
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
            let agents = Task { [weak self] in
                await self?.letAgentsEnd(client, leaving: leaving, task: task) ?? false
            }
            for scene in self?.scenes.allObjects ?? [] { await scene.flush() }
            _ = await client.saveOpenPages()
            try? await client.syncNow()
            await Reminders.schedule(client, asking: false)
            var worked = await agents.value
            if !worked {
                worked = await self?.letAgentsEnd(client, leaving: leaving, task: task) ?? false
            }
            if worked, self?.stays(away: leaving) ?? false, task.running {
                for scene in self?.scenes.allObjects ?? [] { await scene.flush() }
                _ = await client.saveOpenPages()
                try? await client.syncNow()
            }
            self?.turn { [weak self] client in
                if self?.stays(away: leaving) ?? true { await client.stop() }
                task.end()
            }
        }
    }

    /// Waits, away, for the agents at work to end, and tells whether any was: nothing but this
    /// device ends a run, and one cut off as the app is suspended fails with part of its edits
    /// made. As the time the system gives runs out they are stopped, so they end as stopped.
    /// It runs beside the save and sync, which may take all of that time.
    private func letAgentsEnd(_ client: CoreClient, leaving: Int, task: BackgroundTask) async
        -> Bool
    {
        var pages = agentPages
        var worked = false
        var stopping = false
        while stays(away: leaving), task.running {
            let work = await Self.agentWork(on: pages, client)
            let left = UIApplication.shared.backgroundTimeRemaining
            let next = AgentHold.next(atWork: !work.isEmpty, remaining: left, stopping: stopping)
            if next == .end { break }
            if next == .stop {
                stopping = true
                for (page, runs) in work {
                    for run in runs { await client.stopAgent(page, runID: run) }
                }
            }
            (pages, worked) = (Set(work.keys), true)
            try? await Task.sleep(for: .milliseconds(500))
        }
        return worked
    }

    /// The pages, of these, an agent is at work on, each with its runs that can be stopped.
    nonisolated private static func agentWork(on pages: Set<String>, _ client: CoreClient) async
        -> [String: [String]]
    {
        var work: [String: [String]] = [:]
        for page in pages {
            guard let state = try? await client.agentState(page), state.atWork else { continue }
            work[page] = state.runs.filter { $0.running && $0.stoppable }.map(\.id)
        }
        return work
    }

    /// Stops the agents at work on these pages, and gives their runs a moment to end.
    nonisolated private static func stopAgents(on pages: Set<String>, _ client: CoreClient) async {
        var work = await agentWork(on: pages, client)
        for (page, runs) in work {
            for run in runs { await client.stopAgent(page, runID: run) }
        }
        for _ in 0..<10 where !work.isEmpty {
            try? await Task.sleep(for: .milliseconds(100))
            work = await agentWork(on: Set(work.keys), client)
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

/// What the app does, away, about the agents at work.
enum AgentHold: Equatable {
    case wait, stop, end

    /// How long before the time away runs out the agents are stopped, for their runs to end
    /// and be saved within it.
    static let margin: TimeInterval = 8
    /// How long the app waits, as that time is up, for agents still at work to be stopped.
    static let lastWait: TimeInterval = 3

    static func next(atWork: Bool, remaining: TimeInterval, stopping: Bool) -> AgentHold {
        guard atWork else { return .end }
        return !stopping && remaining < margin ? .stop : .wait
    }
}

extension AgentState {
    var atWork: Bool { running || runs.contains(where: \.running) }
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
