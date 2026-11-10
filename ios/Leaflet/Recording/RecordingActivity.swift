import ActivityKit
import Foundation
import os

@MainActor
final class RecordingActivity {
    static let shared = RecordingActivity()

    private typealias State = RecordingAttributes.ContentState

    private var id: String?
    private var shown: State?
    private var last: (recording: RecordingState, at: Date)?
    private var declined = false
    private var swept = false
    private var refreshing: Task<Void, Never>?

    private nonisolated static let fresh: TimeInterval = 120

    private nonisolated static func staleAfter(_ state: State) -> Date? {
        state.paused ? nil : Date().addingTimeInterval(fresh)
    }

    private init() {}

    @discardableResult
    func start() -> Bool {
        if id != nil { return true }
        defer { sweep() }
        guard ActivityAuthorizationInfo().areActivitiesEnabled else { return false }
        let state = State(started: Date(), paused: false, elapsed: 0, starting: true)
        do {
            let activity = try Activity.request(
                attributes: RecordingAttributes(),
                content: .init(state: state, staleDate: Self.staleAfter(state)))
            id = activity.id
            shown = state
            watch(activity)
            keepFresh()
            return true
        } catch {
            Logger.app.error("recording activity: \(error.localizedDescription, privacy: .public)")
            return false
        }
    }

    func sync(_ recording: RecordingState, at: Date) {
        guard recording.recording, !recording.testing else {
            end()
            return
        }
        last = (recording, at)
        guard !declined, start(), let id else { return }
        let elapsed = TimeInterval(recording.elapsedMs) / 1000
        let state = State(
            started: at.addingTimeInterval(-elapsed), paused: recording.paused, elapsed: elapsed,
            starting: !recording.active)
        if let shown, shown.paused == state.paused, shown.starting == state.starting,
            abs(shown.started.timeIntervalSince(state.started)) < 1
        {
            return
        }
        shown = state
        Task { await Self.update(id, to: state) }
    }

    private func keepFresh() {
        guard refreshing == nil else { return }
        refreshing = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(Self.fresh / 2))
                guard !Task.isCancelled, let self else { return }
                if let id = self.id, let shown = self.shown, !shown.paused {
                    await Self.update(id, to: shown)
                }
            }
        }
    }

    private func letGoStale() {
        refreshing?.cancel()
        refreshing = nil
    }

    func end() {
        last = nil
        declined = false
        letGoStale()
        guard id != nil || !swept else { return }
        id = nil
        shown = nil
        swept = true
        endAll(but: nil)
    }

    private func watch(_ activity: Activity<RecordingAttributes>) {
        let watched = activity.id
        Task { [weak self] in
            for await state in activity.activityStateUpdates
            where state == .ended || state == .dismissed {
                self?.gone(watched, byUser: state == .dismissed)
                return
            }
        }
    }

    private func gone(_ watched: String, byUser: Bool) {
        guard watched == id else { return }
        id = nil
        shown = nil
        letGoStale()
        if byUser {
            declined = true
            return
        }
        Task { await Self.end([watched]) }
        if let last { sync(last.recording, at: last.at) }
    }

    private func sweep() {
        guard !swept else { return }
        swept = true
        endAll(but: id)
    }

    private func endAll(but keep: String?) {
        let ids = Activity<RecordingAttributes>.activities.map(\.id).filter { $0 != keep }
        guard !ids.isEmpty else { return }
        Task { await Self.end(ids) }
    }

    private nonisolated static func update(_ id: String, to state: State) async {
        guard let activity = Activity<RecordingAttributes>.activities.first(where: { $0.id == id })
        else { return }
        await activity.update(.init(state: state, staleDate: staleAfter(state)))
    }

    private nonisolated static func end(_ ids: [String]) async {
        for activity in Activity<RecordingAttributes>.activities where ids.contains(activity.id) {
            await activity.end(nil, dismissalPolicy: .immediate)
        }
    }
}
