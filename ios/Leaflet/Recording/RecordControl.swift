import SwiftUI

/// RecordControl is the page's Record button, as the desktop's header has
/// it, and, while a recording runs, its pill: how long it has run, Pause
/// and Stop.
struct RecordControl: View {
    let pageID: String
    /// canRecord says the page can be changed, which a recording does.
    let canRecord: Bool
    let editor: EditorController?
    @Environment(\.palette) private var palette

    var body: some View {
        let recorder = Recorder.shared
        let state = recorder.state
        if state.recording, !state.testing {
            pill(recorder, state)
        } else {
            Button(L.pageRecord, systemImage: "record.circle") {
                recorder.start(pageID: pageID, editor: editor)
            }
            .tint(Color.primary)
            .disabled(!canRecord || state.recording)
        }
    }

    private func pill(_ recorder: Recorder, _ state: RecordingState) -> some View {
        HStack(spacing: 8) {
            TimelineView(.periodic(from: .now, by: 0.5)) { t in
                HStack(spacing: 6) {
                    Circle().fill(palette.danger).frame(width: 8, height: 8)
                        .opacity(
                            state.paused || Int(t.date.timeIntervalSince1970 * 2) % 2 == 0 ? 1 : 0.4
                        )
                    Text(state.starting ? L.recStarting : Self.time(recorder.elapsed(at: t.date)))
                        .font(.footnote.weight(.semibold).monospacedDigit())
                        .foregroundStyle(palette.dangerText)
                }
            }
            Button {
                recorder.togglePause()
            } label: {
                Image(systemName: state.paused ? "play.fill" : "pause.fill")
                    .font(.footnote)
                    .frame(width: 28, height: 28)
            }
            .accessibilityLabel(state.paused ? LI.resumeRecording : L.commonPause)
            .disabled(state.starting)
            Button {
                recorder.stop()
            } label: {
                Text(L.recStop).font(.footnote.weight(.semibold))
                    .padding(.horizontal, 10).padding(.vertical, 5)
                    .background(palette.danger, in: .capsule)
                    .foregroundStyle(palette.onAccent)
            }
        }
        .buttonStyle(.plain)
        .foregroundStyle(palette.danger)
        .padding(.leading, 10).padding(.trailing, 4)
    }

    /// time is a duration as the desktop shows it: 1:05, or 1:02:05.
    static func time(_ seconds: TimeInterval) -> String {
        let s = max(Int(seconds), 0)
        return s >= 3600
            ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60)
            : String(format: "%d:%02d", s / 60, s % 60)
    }
}
