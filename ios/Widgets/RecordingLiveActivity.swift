import ActivityKit
import AppIntents
import SwiftUI
import WidgetKit

struct RecordingLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RecordingAttributes.self) { context in
            HStack(spacing: 12) {
                Dot(paused: context.state.paused || context.isStale)
                VStack(alignment: .leading, spacing: 2) {
                    Status(paused: context.state.paused, stopped: context.isStale).font(.headline)
                    Time(state: context.state, stopped: context.isStale).font(
                        .title2.monospacedDigit())
                }
                Spacer()
                Controls(paused: context.state.paused)
            }
            .padding()
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    HStack(spacing: 6) {
                        Dot(paused: context.state.paused || context.isStale)
                        Status(paused: context.state.paused, stopped: context.isStale).font(
                            .headline)
                    }
                    .padding(.leading, 4)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Time(state: context.state, stopped: context.isStale)
                        .font(.headline.monospacedDigit())
                        .padding(.trailing, 4)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    Controls(paused: context.state.paused)
                }
            } compactLeading: {
                Image(systemName: "mic.fill").foregroundStyle(
                    context.isStale ? Color.secondary : Color.red)
            } compactTrailing: {
                Time(state: context.state, stopped: context.isStale)
                    .monospacedDigit()
                    .frame(maxWidth: 52)
            } minimal: {
                Image(systemName: "mic.fill").foregroundStyle(
                    context.isStale ? Color.secondary : Color.red)
            }
        }
    }
}

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
    let stopped: Bool

    var body: some View {
        if stopped {
            Text(
                "Stopped", tableName: "Recording",
                comment:
                    "A recording's Live Activity: the recording stopped, as the app quit while it recorded."
            )
        } else if paused {
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

private struct Time: View {
    let state: RecordingAttributes.ContentState
    let stopped: Bool

    var body: some View {
        if stopped {
            EmptyView()
        } else if state.paused || state.starting {
            Text(
                Duration.seconds(state.elapsed),
                format: .time(pattern: state.elapsed >= 3600 ? .hourMinuteSecond : .minuteSecond))
        } else {
            Text(timerInterval: state.started...Date.distantFuture, countsDown: false)
                .multilineTextAlignment(.trailing)
        }
    }
}

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
