import ActivityKit
import Foundation
import os

/// RecordingActivity is the recording in progress as a Live Activity, on
/// the Lock Screen and in the Dynamic Island, with its time, Pause and
/// Stop: shown for every recording, as iOS stops one started from the
/// background without it. The widgets draw it (Widgets/).
@MainActor
final class RecordingActivity {
    static let shared = RecordingActivity()

    private typealias State = RecordingAttributes.ContentState

    /// id is the activity showing, by its ID: Activity isn't Sendable, so
    /// the calls that change it look it up again off the main thread.
    private var id: String?
    /// shown is what it was last told.
    private var shown: State?
    /// swept says the activities an earlier launch left have gone.
    private var swept = false

    private init() {}

    /// start shows the activity, as a recording starts, unless it shows
    /// already. It reports whether it shows: iOS may refuse it, from the
    /// background, or with Live Activities turned off for the app.
    @discardableResult
    func start() -> Bool {
        if id != nil { return true }
        // Once it shows, or iOS refused it: the one just shown stays.
        defer { sweep() }
        guard ActivityAuthorizationInfo().areActivitiesEnabled else { return false }
        let state = State(started: Date(), paused: false, elapsed: 0)
        do {
            let activity = try Activity.request(
                attributes: RecordingAttributes(), content: .init(state: state, staleDate: nil))
            id = activity.id
            shown = state
            return true
        } catch {
            Logger.app.error("recording activity: \(error.localizedDescription, privacy: .public)")
            return false
        }
    }

    /// sync follows the recording as the core says it changed, at at: it
    /// shows while one records, and goes once none does. The microphone's
    /// test doesn't show it.
    func sync(_ recording: RecordingState, at: Date) {
        guard recording.recording, !recording.testing else {
            end()
            return
        }
        guard start(), let id else { return }
        let elapsed = TimeInterval(recording.elapsedMs) / 1000
        let state = State(
            started: at.addingTimeInterval(-elapsed), paused: recording.paused, elapsed: elapsed)
        // Its time counts on by itself: told again only as it pauses or
        // goes on, or as it is found a second out.
        if let shown, shown.paused == state.paused,
            abs(shown.started.timeIntervalSince(state.started)) < 1
        {
            return
        }
        shown = state
        Task { await Self.update(id, to: state) }
    }

    /// end takes the activity away, and those an earlier launch left.
    func end() {
        guard id != nil || !swept else { return }
        id = nil
        shown = nil
        swept = true
        endAll(but: nil)
    }

    /// sweep takes away the activities an earlier launch left, as one that
    /// ended while it recorded, once.
    private func sweep() {
        guard !swept else { return }
        swept = true
        endAll(but: id)
    }

    /// endAll ends the activities showing now, but keep: named here, for
    /// one started while they end to stay.
    private func endAll(but keep: String?) {
        let ids = Activity<RecordingAttributes>.activities.map(\.id).filter { $0 != keep }
        guard !ids.isEmpty else { return }
        Task { await Self.end(ids) }
    }

    private nonisolated static func update(_ id: String, to state: State) async {
        guard let activity = Activity<RecordingAttributes>.activities.first(where: { $0.id == id })
        else { return }
        await activity.update(.init(state: state, staleDate: nil))
    }

    private nonisolated static func end(_ ids: [String]) async {
        for activity in Activity<RecordingAttributes>.activities where ids.contains(activity.id) {
            await activity.end(nil, dismissalPolicy: .immediate)
        }
    }
}
