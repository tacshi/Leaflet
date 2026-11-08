import ActivityKit
import Foundation

/// RecordingAttributes are a recording's Live Activity, on the Lock Screen
/// and in the Dynamic Island, which the app and its widgets share: the
/// app starts it and keeps it up to date, and the widgets draw it.
struct RecordingAttributes: ActivityAttributes {
    /// ContentState is how the recording stands.
    struct ContentState: Codable, Hashable, Sendable {
        /// started is when the recording would have begun had it never
        /// paused: its time counts on from it while it runs.
        var started: Date
        var paused: Bool
        /// elapsed is how long it had recorded, as it paused.
        var elapsed: TimeInterval
    }
}
