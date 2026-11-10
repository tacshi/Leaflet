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
    /// last is the recording as it was last told of, and when, to show it
    /// again in an activity of its own should iOS end the one showing.
    private var last: (recording: RecordingState, at: Date)?
    /// declined says the user took the activity away while it recorded:
    /// none shows again until the next recording.
    private var declined = false
    /// swept says the activities an earlier launch left have gone.
    private var swept = false
    /// refreshing tells the activity, while it shows, that the app still
    /// records.
    private var refreshing: Task<Void, Never>?

    /// fresh is how long what a recording's activity says holds without
    /// word from the app: an app that quit while it recorded, as one iOS
    /// ended, can't take its activity away, which then says the recording
    /// stopped rather than count on over nothing.
    private nonisolated static let fresh: TimeInterval = 120

    /// staleAfter is when state stops holding. A paused recording says
    /// nothing more, and the app, with no microphone on, may not run.
    private nonisolated static func staleAfter(_ state: State) -> Date? {
        state.paused ? nil : Date().addingTimeInterval(fresh)
    }

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

    /// sync follows the recording as the core says it changed, at at: it
    /// shows while one records, and goes once none does. The microphone's
    /// test doesn't show it.
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
        // Its time counts on by itself: told again only as it pauses or
        // goes on, as its microphone comes on, or as it is found a second
        // out.
        if let shown, shown.paused == state.paused, shown.starting == state.starting,
            abs(shown.started.timeIntervalSince(state.started)) < 1
        {
            return
        }
        shown = state
        Task { await Self.update(id, to: state) }
    }

    /// keepFresh says the recording still runs, twice as often as the
    /// activity needs to hear it, for as long as one shows.
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

    /// end takes the activity away, and those an earlier launch left.
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

    /// watch hears activity end without the app ending it, as end does:
    /// one iOS ended, after the hours it may show, gives way to a new one,
    /// should iOS allow it now, and one the user took away to none.
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
        // One the app ended, or that another replaced, is gone already.
        guard watched == id else { return }
        id = nil
        shown = nil
        letGoStale()
        if byUser {
            declined = true
            return
        }
        // Ended, it would stay on the Lock Screen a while beside the new.
        Task { await Self.end([watched]) }
        if let last { sync(last.recording, at: last.at) }
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
        await activity.update(.init(state: state, staleDate: staleAfter(state)))
    }

    private nonisolated static func end(_ ids: [String]) async {
        for activity in Activity<RecordingAttributes>.activities where ids.contains(activity.id) {
            await activity.end(nil, dismissalPolicy: .immediate)
        }
    }
}
