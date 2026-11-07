import AVFoundation
import LeafletCore
import SwiftUI
import UIKit

/// RecordingBlock is a recording in the page, as the desktop draws it: its
/// title and state, Play and its menu, and its transcript, a time on each
/// line that plays from there, a long one folded to its first lines. While
/// it records, it shows what the microphone hears, and the last two lines
/// of the words as they come. It is as tall as it needs, which it tells
/// onHeight.
struct RecordingBlock: View {
    let node: DocNode
    /// canEdit says the page can be changed: the transcript only copies
    /// otherwise.
    let canEdit: Bool
    /// editor is the page's, which puts the transcript in.
    weak var editor: EditorController?
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// control is the size of Play and the menu's button, as the text
    /// size scales them.
    @ScaledMetric(relativeTo: .subheadline) private var control: CGFloat = 32
    /// open shows a long transcript whole.
    @State private var open = false
    /// clock says what is being said while the recording plays.
    @State private var clock = LineClock()

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
                LevelMeter(levels: recorder.levels, color: palette.danger, idle: palette.divider)
                    .frame(height: 36)
                if !recorder.liveText.isEmpty {
                    LiveTail(text: recorder.liveText, color: palette.textSecondary)
                }
            } else if let row {
                if recorder.playing == id, let player = recorder.player {
                    Scrubber(player: player, id: id)
                }
                // What went wrong, until it is read and closed: a failure,
                // or a warning over a transcript that may be short.
                if !row.error.isEmpty {
                    notice(row)
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
        .onGeometryChange(for: CGSize.self) {
            $0.size
        } action: {
            onHeight?(CGSize(width: $0.width, height: $0.height + 2))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .coordinateSpace(.named(LineClock.space))
        .task(id: id) { _ = recorder.row(id) }
        .task(id: recorder.playing == id) {
            guard recorder.playing == id else {
                clock.clear()
                return
            }
            let starts = (row?.transcript ?? []).map { Double($0.startMs) / 1000 }
            while !Task.isCancelled, let player = recorder.player {
                clock.tick(player.currentTime, starts: starts)
                try? await Task.sleep(for: .milliseconds(250))
            }
        }
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
                Text(title(row)).font(.subheadline.weight(.semibold)).foregroundStyle(palette.text)
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

    /// title is the block's title as the core names it: an older one's in
    /// the app's language, or Recording on a page of the same title.
    private func title(_ row: RecordingRow?) -> String {
        Self.title(node, row)
    }

    private static func title(_ node: DocNode, _ row: RecordingRow?) -> String {
        MobileRecordingTitle(
            node.data?["title"]?.string ?? "", row?.createdMs ?? 0, row?.pageTitle ?? "")
    }

    /// status is the line under the title: what runs for the recording
    /// here, else what the core says of it.
    private func status(_ recorder: Recorder, _ row: RecordingRow?, live: Bool) -> String {
        let state = recorder.state
        if live { return state.engine }
        guard let row else { return recorder.missing.contains(id) ? L.recNotFound : "" }
        if state.finishing.contains(id) { return L.recAddingTimestamps }
        if state.transcribing.contains(id) { return L.recTranscribing }
        return row.label
    }

    /// actions are what block, of recording id, offers now, for VoiceOver
    /// at the caret, which can't reach the block in the text: Play or
    /// Pause, and its menu's items.
    static func actions(_ id: String, block: DocNode, canEdit: Bool, editor: EditorController?)
        -> [BlockAction]
    {
        let recorder = Recorder.shared
        let state = recorder.state
        guard let row = recorder.rows[id], !(state.recording && state.id == id),
            row.status != "recording"
        else { return [] }
        var out: [BlockAction] = []
        if recorder.loading == nil, !state.recording {
            let playing = recorder.playing == id && recorder.player?.isPlaying == true
            out.append(
                BlockAction(title: playing ? L.commonPause : L.recPlay) { recorder.play(id) })
        }
        return out
            + menuItems(id, block: block, row, canEdit: canEdit, editor: editor).filter(\.enabled)
    }

    /// menuItems are the items of the menu of block, of recording id, in
    /// editor's page.
    private static func menuItems(
        _ id: String, block: DocNode, _ row: RecordingRow, canEdit: Bool, editor: EditorController?
    ) -> [BlockAction] {
        let recorder = Recorder.shared
        let empty = row.transcript.isEmpty
        return [
            BlockAction(title: L.recRename, symbol: "pencil", enabled: canEdit) {
                editor?.onRenameBlock?(block, title(block, row))
            },
            BlockAction(title: L.recCopyTranscript, symbol: "doc.on.doc", enabled: !empty) {
                UIPasteboard.general.string = row.plain
            },
            BlockAction(
                title: L.recInsertTranscript, symbol: "text.insert", enabled: !empty && canEdit
            ) {
                recorder.insertTranscript(id, after: block.id, into: editor)
            },
            BlockAction(title: L.recExportAudio, symbol: "square.and.arrow.up") {
                recorder.shareAudio(id, title: title(block, row), in: editor)
            },
        ]
            + [("txt", L.recExportText), ("srt", L.recExportSrt), ("vtt", L.recExportVtt)].map {
                format, kind in
                BlockAction(
                    title: L.recExportTranscriptAs(kind: kind), symbol: "square.and.arrow.up",
                    enabled: !empty
                ) {
                    recorder.shareTranscript(
                        id, title: title(block, row), format: format, in: editor)
                }
            } + [
                // The agent reads the transcript with the block.
                BlockAction(title: L.recSummarize, symbol: "sparkles", enabled: !empty && canEdit) {
                    editor?.onSummarize?(block, L.recSummarizePrompt(title: title(block, row)))
                },
                // Not while a transcription runs, which the new one would race.
                BlockAction(
                    title: empty ? L.recTranscribe : L.recTranscribeAgain,
                    symbol: "captions.bubble",
                    enabled: canEdit && !recorder.state.transcribing.contains(id)
                ) {
                    recorder.retranscribe(id)
                },
                // The block goes as one step to undo; its recording stays, for
                // the undo to bring back.
                BlockAction(
                    title: L.commonDelete, symbol: "trash", enabled: canEdit, destructive: true
                ) {
                    editor?.removeBlock(block.id)
                },
            ]
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
            .frame(width: control, height: control)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(palette.icon)
        .disabled(recorder.loading != nil || recorder.state.recording)
    }

    private func menu(_ recorder: Recorder, _ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        return Menu {
            let items = Self.menuItems(id, block: node, row, canEdit: canEdit, editor: editor)
            ForEach(items.filter { !$0.destructive }, id: \.title) { item in
                Button(item.title, systemImage: item.symbol, action: item.run)
                    .disabled(!item.enabled)
            }
            Divider()
            ForEach(items.filter(\.destructive), id: \.title) { item in
                Button(item.title, systemImage: item.symbol, role: .destructive, action: item.run)
                    .disabled(!item.enabled)
            }
        } label: {
            Image(systemName: "ellipsis")
                .frame(width: control, height: control)
                .contentShape(.rect)
        }
        .foregroundStyle(palette.icon)
    }

    /// transcript is the recording's lines, each after its time: a tap on
    /// either plays from it.
    private func transcript(_ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        let widest = row.durationMs >= 3_600_000 ? "0:00:00" : "00:00"
        let folds = row.folded > 0 && row.folded < row.transcript.count
        let lines = row.transcript
        let follow: (CGFloat) -> Void = { [editor, block = node.id] in
            editor?.follow(block: block, y: $0)
        }
        return VStack(alignment: .leading, spacing: 8) {
            VStack(alignment: .leading, spacing: 8) {
                if folds, !open {
                    RollingLines(count: lines.count, clock: clock, spacing: 8) { index in
                        transcriptLine(lines[index], index, widest: widest, follow: { _ in })
                    }
                } else {
                    ForEach(Array(lines.enumerated()), id: \.offset) { index, line in
                        transcriptLine(line, index, widest: widest, follow: follow)
                    }
                }
            }
            .padding(.horizontal, -TimeStyle.lineInset)
            if folds {
                FoldButton(
                    open: $open, count: row.transcript.count,
                    indent: TimeStyle.caption.indent(widest))
            }
        }
    }

    private func transcriptLine(
        _ line: Segment, _ index: Int, widest: String, follow: @escaping (CGFloat) -> Void
    ) -> some View {
        RecordingLine(
            line: line, index: index, widest: widest, clock: clock,
            disabled: Recorder.shared.state.recording, follow: follow,
            play: { Recorder.shared.play(id, from: TimeInterval(line.startMs) / 1000) })
    }

    /// notice is what went wrong with the recording, in red when it failed:
    /// its × lets it go, on a page that can be changed.
    private func notice(_ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        let failed = row.status == "failed"
        let fg = failed ? palette.dangerText : palette.warnText
        return HStack(alignment: .firstTextBaseline, spacing: 8) {
            Label(row.error, systemImage: "exclamationmark.circle")
                .frame(maxWidth: .infinity, alignment: .leading)
            if canEdit {
                Button {
                    Recorder.shared.dismissError(id)
                } label: {
                    Image(systemName: "xmark").imageScale(.small)
                        .frame(width: 24, height: 24)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(L.pageDismiss)
            }
        }
        .font(.footnote)
        .foregroundStyle(fg)
        .padding(10)
        .background(failed ? palette.dangerBg : palette.warnBg, in: .rect(cornerRadius: 8))
    }
}

/// RecordingLine is a line of a recording's transcript.
private struct RecordingLine: View {
    let line: Segment
    let index: Int
    let widest: String
    let clock: LineClock
    let disabled: Bool
    let follow: (CGFloat) -> Void
    let play: () -> Void

    var body: some View {
        let palette = BlockLook.palette
        let current = clock.line == index
        TimedRow(time: RecordControl.time(TimeInterval(line.startMs) / 1000), widest: widest) {
            play()
        } content: {
            VStack(alignment: .leading, spacing: 2) {
                if let speaker = line.speaker, !speaker.isEmpty {
                    Text(L.recSpeaker(name: speaker))
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(palette.textMuted)
                }
                Text(line.text)
                    .font(.system(size: BlockLook.textSize - 2))
                    .foregroundStyle(palette.text)
                    .multilineTextAlignment(.leading)
            }
        }
        .disabled(disabled)
        .padding(.horizontal, TimeStyle.lineInset)
        .padding(.vertical, TimeStyle.lineLift)
        .background(current ? palette.accentSoft : .clear, in: .rect(cornerRadius: 6))
        .background {
            if current {
                Color.clear.onGeometryChange(for: CGFloat.self) {
                    $0.frame(in: .named(LineClock.space)).minY
                } action: {
                    follow($0)
                }
            }
        }
        .padding(.vertical, -TimeStyle.lineLift)
    }
}

/// LiveTail is the last two lines of the words heard while recording, so
/// the block keeps its height as they come, as on the desktop: as tall as
/// the words up to two lines, which show from the bottom up.
private struct LiveTail: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text).lineLimit(2).hidden()
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay(alignment: .bottomLeading) {
                Text(text).foregroundStyle(color)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .clipped()
            .font(.subheadline)
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
                SkipButton(seconds: -PlaySpeed.skip) { Recorder.shared.skip(id, by: -PlaySpeed.skip) }
                SkipButton(seconds: PlaySpeed.skip) { Recorder.shared.skip(id, by: PlaySpeed.skip) }
                Text(RecordControl.time(player.currentTime))
                Slider(
                    value: Binding(
                        get: { player.currentTime },
                        set: { Recorder.shared.seek(id, to: $0) }),
                    in: 0...max(player.duration, 0.1)
                )
                .tint(palette.accent)
                Text(RecordControl.time(player.duration))
                SpeedMenu()
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
