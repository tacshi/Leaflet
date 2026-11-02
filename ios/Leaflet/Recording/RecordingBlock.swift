import AVFoundation
import SwiftUI
import UIKit

/// RecordingBlock is a recording in the page, as the desktop draws it: its
/// title and state, Play and its menu, and its transcript, a time on each
/// line that plays from there. While it records, it shows what the
/// microphone hears, and the words as they come. It is as tall as it
/// needs, which it tells onHeight.
struct RecordingBlock: View {
    let node: DocNode
    /// canEdit says the page can be changed: the transcript only copies
    /// otherwise.
    let canEdit: Bool
    var onHeight: (@MainActor (CGFloat) -> Void)?

    private var id: String { node.data?["recording_id"]?.string ?? "" }

    var body: some View {
        let palette = BlockLook.palette
        let recorder = Recorder.shared
        let state = recorder.state
        let live = state.recording && state.id == id
        let row = recorder.rows[id]
        VStack(alignment: .leading, spacing: 12) {
            header(recorder, row, live: live)
            if live {
                LevelMeter(levels: state.levels, color: palette.danger, idle: palette.divider)
                    .frame(height: 36)
                if !state.liveText.isEmpty {
                    Text(state.liveText).font(.subheadline).foregroundStyle(palette.textSecondary)
                }
            } else if let row {
                if recorder.playing == id, let player = recorder.player {
                    Scrubber(player: player, id: id)
                }
                if row.status == "failed", !row.error.isEmpty {
                    Label(row.error, systemImage: "exclamationmark.circle")
                        .font(.footnote)
                        .foregroundStyle(palette.dangerText)
                        .padding(10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(palette.dangerBg, in: .rect(cornerRadius: 8))
                }
                if !row.transcript.isEmpty {
                    transcript(row)
                }
            }
        }
        .padding(.horizontal, 16).padding(.vertical, 14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.surface, in: .rect(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.divider))
        .fixedSize(horizontal: false, vertical: true)
        .onGeometryChange(for: CGFloat.self) {
            $0.size.height
        } action: {
            onHeight?($0 + 2)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .task(id: id) { _ = recorder.row(id) }
    }

    private func header(_ recorder: Recorder, _ row: RecordingRow?, live: Bool) -> some View {
        let palette = BlockLook.palette
        return HStack(spacing: 10) {
            Image(systemName: "waveform")
                .font(.footnote)
                .foregroundStyle(live ? palette.danger : palette.accentText)
                .frame(width: 30, height: 30)
                .background(live ? palette.dangerBg : palette.accentSoft, in: .circle)
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(palette.text)
                    .lineLimit(1)
                Text(status(recorder, row, live: live)).font(.caption)
                    .foregroundStyle(palette.textMuted)
                    .lineLimit(2)
            }
            Spacer(minLength: 0)
            if let row, !live, row.status != "recording" {
                play(recorder)
                menu(recorder, row)
            }
        }
    }

    private var title: String {
        let t = node.data?["title"]?.string ?? ""
        return t.isEmpty ? L.commonRecording : t
    }

    private func status(_ recorder: Recorder, _ row: RecordingRow?, live: Bool) -> String {
        let state = recorder.state
        guard let row else { return live ? state.engine : "" }
        let duration = RecordControl.time(TimeInterval(row.durationMs) / 1000)
        if live { return state.engine }
        if state.finishing.contains(id) { return L.recAddingTimestamps }
        if row.status == "transcribing" || state.transcribing.contains(id) {
            return L.recTranscribing
        }
        if row.status == "failed" { return L.recTranscriptionFailed }
        let engine =
            switch row.sttProvider {
            case "": L.recAudioOnly
            case "soniox": "Soniox"
            case "on-device": L.recEngineOnDevice
            default: L.commonEndpoint
            }
        return L.recStatus(duration: duration, engine: engine)
    }

    private func play(_ recorder: Recorder) -> some View {
        let palette = BlockLook.palette
        _ = recorder.changes
        let playing = recorder.playing == id && recorder.player?.isPlaying == true
        return Button {
            recorder.play(id)
        } label: {
            Group {
                if recorder.loading == id {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: playing ? "pause.fill" : "play.fill")
                }
            }
            .frame(width: 32, height: 32)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(palette.icon)
        .accessibilityLabel(playing ? L.commonPause : L.recPlay)
        .disabled(recorder.loading != nil || recorder.state.recording)
    }

    private func menu(_ recorder: Recorder, _ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        let empty = row.transcript.isEmpty
        return Menu {
            Button(L.recCopyTranscript, systemImage: "doc.on.doc") {
                UIPasteboard.general.string = row.plain
            }
            .disabled(empty)
            Button(L.recInsertTranscript, systemImage: "text.insert") {
                recorder.insertTranscript(id)
            }
            .disabled(empty || !canEdit)
            // Not while a transcription runs, which the new one would race.
            Button(
                empty ? L.recTranscribe : L.recTranscribeAgain, systemImage: "captions.bubble"
            ) {
                recorder.retranscribe(id)
            }
            .disabled(!canEdit || recorder.state.transcribing.contains(id))
        } label: {
            Image(systemName: "ellipsis")
                .frame(width: 32, height: 32)
                .contentShape(.rect)
        }
        .foregroundStyle(palette.icon)
        .accessibilityLabel(L.recActions)
    }

    /// transcript is the recording's lines, each after its time: a tap on
    /// either plays from it.
    private func transcript(_ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        return VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(row.transcript.enumerated()), id: \.offset) { _, s in
                Button {
                    Recorder.shared.play(id, from: TimeInterval(s.startMs) / 1000)
                } label: {
                    HStack(alignment: .firstTextBaseline, spacing: 12) {
                        Text(RecordControl.time(TimeInterval(s.startMs) / 1000))
                            .font(.caption.monospacedDigit())
                            .foregroundStyle(palette.accent)
                            .frame(width: 46, alignment: .leading)
                        VStack(alignment: .leading, spacing: 2) {
                            if let speaker = s.speaker, !speaker.isEmpty {
                                Text(L.recSpeaker(name: speaker))
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(palette.textMuted)
                            }
                            Text(s.text)
                                .font(.system(size: BlockLook.textSize - 2))
                                .foregroundStyle(palette.text)
                                .multilineTextAlignment(.leading)
                        }
                        Spacer(minLength: 0)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(Recorder.shared.state.recording)
            }
        }
    }
}

/// Scrubber is where a recording playing is, which a drag moves.
private struct Scrubber: View {
    let player: AVAudioPlayer
    let id: String

    var body: some View {
        let palette = BlockLook.palette
        TimelineView(.periodic(from: .now, by: 0.25)) { _ in
            HStack(spacing: 10) {
                Text(RecordControl.time(player.currentTime))
                Slider(
                    value: Binding(
                        get: { player.currentTime },
                        set: { Recorder.shared.seek(id, to: $0) }),
                    in: 0...max(player.duration, 0.1)
                )
                .tint(palette.accent)
                Text(RecordControl.time(player.duration))
            }
            .font(.caption.monospacedDigit())
            .foregroundStyle(palette.textMuted)
        }
    }
}

/// LevelMeter is what the microphone hears: a thin bar each 50 ms, the
/// newest at the right, as on the desktop.
private struct LevelMeter: View {
    let levels: [Double]
    let color: Color
    let idle: Color

    var body: some View {
        Canvas { context, size in
            let bar: CGFloat = 2.5
            let step: CGFloat = 4.5
            let n = Int(size.width / step)
            for i in 0..<n {
                let j = levels.count - n + i
                let v = j >= 0 ? levels[j] : 0
                let h = max(CGFloat(v) * size.height, bar)
                let rect = CGRect(
                    x: CGFloat(i) * step, y: (size.height - h) / 2, width: bar, height: h)
                context.fill(
                    Path(roundedRect: rect, cornerRadius: bar / 2),
                    with: .color(j >= 0 ? color.opacity(0.8) : idle))
            }
        }
    }
}
