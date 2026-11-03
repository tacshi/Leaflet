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

    /// Action is something the block offers: Play or Pause, or an item of
    /// its menu.
    struct Action {
        let title: String
        var symbol = ""
        var enabled = true
        var destructive = false
        let run: @MainActor () -> Void
    }

    /// actions are what block, of recording id, offers now, for VoiceOver
    /// at the caret, which can't reach the block in the text: Play or
    /// Pause, and its menu's items.
    static func actions(_ id: String, block: DocNode, canEdit: Bool, editor: EditorController?)
        -> [Action]
    {
        let recorder = Recorder.shared
        let state = recorder.state
        guard let row = recorder.rows[id], !(state.recording && state.id == id),
            row.status != "recording"
        else { return [] }
        var out: [Action] = []
        if recorder.loading == nil, !state.recording {
            let playing = recorder.playing == id && recorder.player?.isPlaying == true
            out.append(Action(title: playing ? L.commonPause : L.recPlay) { recorder.play(id) })
        }
        return out
            + menuItems(id, block: block, row, canEdit: canEdit, editor: editor).filter(\.enabled)
    }

    /// menuItems are the items of the menu of block, of recording id, in
    /// editor's page.
    private static func menuItems(
        _ id: String, block: DocNode, _ row: RecordingRow, canEdit: Bool, editor: EditorController?
    ) -> [Action] {
        let recorder = Recorder.shared
        let empty = row.transcript.isEmpty
        return [
            Action(title: L.recRename, symbol: "pencil", enabled: canEdit) {
                editor?.onRenameBlock?(block, title(block, row))
            },
            Action(title: L.recCopyTranscript, symbol: "doc.on.doc", enabled: !empty) {
                UIPasteboard.general.string = row.plain
            },
            Action(title: L.recInsertTranscript, symbol: "text.insert", enabled: !empty && canEdit)
            {
                recorder.insertTranscript(id, after: block.id, into: editor)
            },
            // Not while a transcription runs, which the new one would race.
            Action(
                title: empty ? L.recTranscribe : L.recTranscribeAgain, symbol: "captions.bubble",
                enabled: canEdit && !recorder.state.transcribing.contains(id)
            ) {
                recorder.retranscribe(id)
            },
            // The block goes as one step to undo; its recording stays, for
            // the undo to bring back.
            Action(title: L.commonDelete, symbol: "trash", enabled: canEdit, destructive: true) {
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
    /// either plays from it. The times are as wide as the longest they
    /// can be, as the text size has them, for the lines to start together.
    private func transcript(_ row: RecordingRow) -> some View {
        let palette = BlockLook.palette
        let widest = row.durationMs >= 3_600_000 ? "0:00:00" : "00:00"
        let folds = row.folded > 0 && row.folded < row.transcript.count
        let lines = folds && !open ? Array(row.transcript.prefix(row.folded)) : row.transcript
        return VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, s in
                Button {
                    Recorder.shared.play(id, from: TimeInterval(s.startMs) / 1000)
                } label: {
                    HStack(alignment: .firstTextBaseline, spacing: 12) {
                        ZStack(alignment: .leading) {
                            Text(verbatim: widest).hidden()
                            Text(RecordControl.time(TimeInterval(s.startMs) / 1000))
                                .foregroundStyle(palette.accent)
                        }
                        .font(.caption.monospacedDigit())
                        .fixedSize()
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
            if folds {
                Button {
                    open.toggle()
                } label: {
                    HStack(spacing: 4) {
                        Text(open ? L.recShowLess : L.recShowAll(count: row.transcript.count))
                        Image(systemName: "chevron.down").imageScale(.small)
                            .rotationEffect(.degrees(open ? 180 : 0))
                    }
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(palette.accent)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                // Under the lines' text, past their times.
                .padding(.leading, timesWidth(widest))
            }
        }
    }

    /// timesWidth is how far the lines' text starts: past the widest time
    /// and the space after it.
    private func timesWidth(_ widest: String) -> CGFloat {
        let font = UIFont.monospacedDigitSystemFont(
            ofSize: UIFont.preferredFont(forTextStyle: .caption1).pointSize, weight: .regular)
        return (widest as NSString).size(withAttributes: [.font: font]).width.rounded(.up) + 12
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
