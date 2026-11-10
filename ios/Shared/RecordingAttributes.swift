import ActivityKit
import Foundation

struct RecordingAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable, Sendable {
        var started: Date
        var paused: Bool
        var elapsed: TimeInterval
        var starting: Bool
    }
}
