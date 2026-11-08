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
    /// pause is the size of Pause, as the text size scales it.
    @ScaledMetric(relativeTo: .footnote) private var pause: CGFloat = 28

    var body: some View {
        let recorder = Recorder.shared
        let state = recorder.state
        if state.recording, !state.testing {
            pill(recorder, state)
        } else {
            // With more than one speech provider set up, recording asks
            // which transcribes it: there is no default.
            if let picked = recorder.choices(for: pageID), picked.choices.count > 1 {
                Menu(L.pageRecord, systemImage: "record.circle") {
                    ForEach(picked.choices) { p in
                        Button {
                            recorder.start(pageID: pageID, editor: editor, provider: p.id)
                        } label: {
                            if p.id == picked.current {
                                Label(p.name, systemImage: "checkmark")
                            } else {
                                Text(p.name)
                            }
                        }
                    }
                }
                .tint(Color.primary)
                .disabled(!canRecord || state.recording)
            } else {
                Button(L.pageRecord, systemImage: "record.circle") {
                    recorder.start(pageID: pageID, editor: editor)
                }
                .tint(Color.primary)
                .disabled(!canRecord || state.recording)
            }
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
                        // On one line and whole: squeezed by a long title in
                        // the bar, the time would wrap to "0:" over "…".
                        .lineLimit(1)
                        .fixedSize()
                }
            }
            Button {
                recorder.togglePause()
            } label: {
                Image(systemName: state.paused ? "play.fill" : "pause.fill")
                    .font(.footnote)
                    .frame(width: pause, height: pause)
                    .contentShape(.rect)
            }
            .accessibilityLabel(state.paused ? LI.resumeRecording : L.commonPause)
            .disabled(state.starting)
            // A square in a circle, which needs no room for a word.
            Button {
                recorder.stop()
            } label: {
                RoundedRectangle(cornerRadius: 2).fill(palette.onAccent)
                    .frame(width: pause * 0.32, height: pause * 0.32)
                    .frame(width: pause, height: pause)
                    .background(palette.danger, in: .circle)
                    .contentShape(.circle)
            }
            .accessibilityLabel(L.recStop)
        }
        .buttonStyle(.plain)
        .foregroundStyle(palette.danger)
        .padding(.leading, 10).padding(.trailing, 4)
        // The bar shortens the page's title before the pill.
        .fixedSize()
    }

    /// time is a duration as the desktop shows it: 1:05, or 1:02:05.
    static func time(_ seconds: TimeInterval) -> String {
        let s = max(Int(seconds), 0)
        return s >= 3600
            ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60)
            : String(format: "%d:%02d", s / 60, s % 60)
    }
}
