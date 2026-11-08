import ActivityKit
import AppIntents
import SwiftUI
import WidgetKit

/// RecordingLiveActivity draws the recording in progress on the Lock
/// Screen and in the Dynamic Island: whether it records or is paused, its
/// time, and Pause and Stop, which the app performs.
struct RecordingLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RecordingAttributes.self) { context in
            HStack(spacing: 12) {
                Dot(paused: context.state.paused)
                VStack(alignment: .leading, spacing: 2) {
                    Status(paused: context.state.paused).font(.headline)
                    Time(state: context.state).font(.title2.monospacedDigit())
                }
                Spacer()
                Controls(paused: context.state.paused)
            }
            .padding()
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    HStack(spacing: 6) {
                        Dot(paused: context.state.paused)
                        Status(paused: context.state.paused).font(.headline)
                    }
                    .padding(.leading, 4)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Time(state: context.state)
                        .font(.headline.monospacedDigit())
                        .padding(.trailing, 4)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    Controls(paused: context.state.paused)
                }
            } compactLeading: {
                Image(systemName: "mic.fill").foregroundStyle(.red)
            } compactTrailing: {
                Time(state: context.state)
                    .monospacedDigit()
                    .frame(maxWidth: 52)
            } minimal: {
                Image(systemName: "mic.fill").foregroundStyle(.red)
            }
        }
    }
}

/// Dot is the red dot of a recording, grey while it is paused.
private struct Dot: View {
    let paused: Bool

    var body: some View {
        Circle()
            .fill(paused ? Color.secondary : Color.red)
            .frame(width: 10, height: 10)
    }
}

private struct Status: View {
    let paused: Bool

    var body: some View {
        if paused {
            Text(
                "Paused", tableName: "Recording",
                comment: "A recording's Live Activity: the recording is paused.")
        } else {
            Text(
                "Recording", tableName: "Recording",
                comment: "A recording's Live Activity: a recording is under way.")
        }
    }
}

/// Time is how long the recording has run: counting on by itself while it
/// records, and still while it is paused.
private struct Time: View {
    let state: RecordingAttributes.ContentState

    var body: some View {
        if state.paused {
            Text(
                Duration.seconds(state.elapsed),
                format: .time(pattern: state.elapsed >= 3600 ? .hourMinuteSecond : .minuteSecond))
        } else {
            Text(timerInterval: state.started...Date.distantFuture, countsDown: false)
                .multilineTextAlignment(.trailing)
        }
    }
}

/// Controls are Pause, or Resume while it is paused, and Stop.
private struct Controls: View {
    let paused: Bool

    var body: some View {
        HStack(spacing: 10) {
            Button(intent: PauseRecordingIntent()) {
                Image(systemName: paused ? "play.fill" : "pause.fill")
                    .frame(width: 22, height: 22)
            }
            .accessibilityLabel(
                paused
                    ? Text(
                        "Resume", tableName: "Recording",
                        comment:
                            "A recording's Live Activity: VoiceOver's name of the button that goes on with the paused recording."
                    )
                    : Text(
                        "Pause", tableName: "Recording",
                        comment:
                            "A recording's Live Activity: VoiceOver's name of the button that pauses the recording."
                    ))
            Button(intent: StopRecordingIntent()) {
                Image(systemName: "stop.fill")
                    .frame(width: 22, height: 22)
            }
            .tint(.red)
            .accessibilityLabel(
                Text(
                    "Stop", tableName: "Recording",
                    comment:
                        "A recording's Live Activity: VoiceOver's name of the button that stops the recording."
                ))
        }
        .buttonStyle(.bordered)
        .buttonBorderShape(.circle)
    }
}
