import LeafletCore
import SwiftUI

/// SpeakerEdit is a speaker of a file block's notes being named: from, on
/// all their lines, or for nil a new one for the line at place.
struct SpeakerEdit {
    let node: DocNode
    let from: String?
    let place: Int
}

/// FileBlock is a file in the page, as the desktop draws it: its card, its
/// player once it plays, as audio and video play in the page, and its notes,
/// a time in them playing it from there. Audio is one card with all of these,
/// as a recording is. A tap shows any other file with Quick Look. It is as
/// tall as it needs, which it tells onHeight.
struct FileBlock: View {
    let node: DocNode
    /// canEdit says the page can be changed: the notes only show otherwise.
    var canEdit = false
    /// editor is the page's, which writes the notes and shows the file.
    weak var editor: EditorController?
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// lines are the notes as the core reads them, read as they change: a
    /// transcript has thousands, and a block draws often.
    @State private var lines: [VideoBlock.Line] = []
    /// speakers are those the notes name, read with them.
    @State private var speakers: [String] = []
    /// clock says what is being said while the block plays, and starts are
    /// when the lines it goes through start.
    @State private var clock = LineClock()
    @State private var starts: [Double] = []
    @State private var width: CGFloat = 0
    @ScaledMetric(relativeTo: .subheadline) private var control: CGFloat = 32

    private var fileID: String { node.data?["file_id"]?.string ?? "" }
    private var name: String { node.data?["name"]?.string ?? L.cardFile }
    private var notes: String { node.data?["notes"]?.string ?? "" }
    private var speakersHidden: Bool { Self.speakersHidden(node) }
    private var kind: String { Self.kind(node) }
    private var audio: Bool { kind == "audio" }

    /// speakersHidden says block's notes show without the speakers' names.
    private static func speakersHidden(_ block: DocNode) -> Bool {
        block.data?["speakers_hidden"] == .bool(true)
    }

    /// names are the names the user gave speakers in block's notes, for
    /// the core, as JSON.
    private static func names(_ block: DocNode) -> String {
        let list = block.data?["speaker_names"]?.array?.compactMap(\.string) ?? []
        return (try? JSONEncoder().encode(list)).map { String(decoding: $0, as: UTF8.self) } ?? "[]"
    }

    /// Reading is what the lines shown are read from.
    private struct Reading: Equatable {
        let notes: String
        let names: String
        let hidden: Bool
    }

    /// SpeakersEdit is notes after a change of speakers, as the core says.
    private struct SpeakersEdit: Decodable {
        let notes: String
        let names: [String]
    }

    /// editSpeakers changes the notes and speakers' names of block id, as
    /// it is now, with what synced in since, with edit, which goes through
    /// the core, as one step to undo.
    static func editSpeakers(
        _ id: String, in editor: EditorController?, _ edit: (String, String) -> String
    ) {
        guard let editor, let now = editor.node(ofBlock: id) else { return }
        let json = edit(now.data?["notes"]?.string ?? "", names(now))
        guard let out = try? JSONDecoder().decode(SpeakersEdit.self, from: Data(json.utf8))
        else { return }
        var data = now.data ?? [:]
        data["notes"] = .string(out.notes)
        data["speaker_names"] = out.names.isEmpty ? nil : .array(out.names.map { .string($0) })
        editor.setData(data, ofBlock: id)
    }

    /// kind is how the app shows block's file (doc.FilePreviewKind).
    static func kind(_ block: DocNode) -> String {
        MobileFileKind(block.data?["name"]?.string ?? "", block.data?["mime"]?.string ?? "")
    }

    private static func media(_ kind: String) -> Bool { kind == "audio" || kind == "video" }

    var body: some View {
        let playback = FilePlayback.shared
        let palette = BlockLook.palette
        VStack(alignment: .leading, spacing: audio ? 12 : 10) {
            if audio {
                header(playback)
            } else {
                card(playback)
            }
            if playback.block == node.id, let player = playback.player {
                if kind == "video" {
                    FileVideo(player: player)
                        .frame(height: min(max(width, 1) * 9 / 16, 420))
                        .clipShape(.rect(cornerRadius: 10))
                } else {
                    FileBar(player: player)
                }
            }
            if playback.failed.contains(node.id) {
                Text(L.cardCannotPlay).font(.caption).foregroundStyle(palette.textMuted)
            }
            if !audio, Recorder.shared.state.transcribingFiles.contains(node.id) {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    Text(L.recTranscribing).font(.caption).foregroundStyle(palette.textMuted)
                }
            }
            if !lines.isEmpty {
                NotesPanel(
                    lines: lines, blockID: node.id, pageID: editor?.pageID,
                    speakers: canEdit ? speakerActions : nil,
                    play: { play($0) }, clock: clock,
                    follow: { [editor, id = node.id] in editor?.follow(block: id, y: $0) },
                    look: audio ? .card(speakers: !speakersHidden) : .panel)
            }
        }
        .padding(.horizontal, audio ? 16 : 0).padding(.vertical, audio ? 14 : 0)
        .background(audio ? palette.surface : .clear, in: .rect(cornerRadius: 10))
        .overlay {
            if audio { RoundedRectangle(cornerRadius: 10).stroke(palette.divider) }
        }
        .coordinateSpace(.named(LineClock.space))
        .fixedSize(horizontal: false, vertical: true)
        .onGeometryChange(for: CGSize.self) {
            $0.size
        } action: {
            width = $0.width
            onHeight?(CGSize(width: $0.width, height: $0.height + 2))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .task(id: Reading(notes: notes, names: Self.names(node), hidden: speakersHidden)) {
            // Audio names its speakers over their lines, as a recording does.
            let (notes, names, hidden) = (notes, Self.names(node), speakersHidden || audio)
            let read = await Task.detached {
                (
                    VideoBlock.lines(json: MobileFileNotes(notes, names, hidden)),
                    (try? JSONDecoder().decode(
                        [String].self, from: Data(MobileFileSpeakers(notes, names).utf8))) ?? []
                )
            }.value
            (lines, speakers) = read
            starts = lines.map { Double($0.at) }
        }
        .task(id: playback.block == node.id) {
            guard playback.block == node.id else {
                clock.clear()
                return
            }
            while !Task.isCancelled, let player = playback.player {
                let sec = player.currentTime().seconds
                if sec.isFinite { clock.tick(sec, starts: starts) }
                try? await Task.sleep(for: .milliseconds(250))
            }
        }
    }

    /// speakerActions are what a line's menu does to its speaker.
    private var speakerActions: NotesPanel.Speakers {
        let (id, node, editor) = (node.id, node, editor)
        return NotesPanel.Speakers(
            all: speakers,
            change: { place, name in
                Self.editSpeakers(id, in: editor) { notes, names in
                    MobileSetLineSpeaker(notes, names, place, name)
                }
            },
            add: { editor?.onEditSpeaker?(node, nil, $0) },
            rename: { editor?.onEditSpeaker?(node, $0, $1) })
    }

    /// header is an audio file's name and controls, as a recording's.
    private func header(_ playback: FilePlayback) -> some View {
        let palette = BlockLook.palette
        let failed = playback.failed.contains(node.id)
        let on = playback.block == node.id && playback.playing
        return HStack(spacing: 10) {
            Image(systemName: "waveform")
                .font(.footnote)
                .foregroundStyle(palette.accentText)
                .frame(width: 30, height: 30)
                .background(palette.accentSoft, in: .circle)
            VStack(alignment: .leading, spacing: 1) {
                Text(name).font(.subheadline.weight(.semibold)).foregroundStyle(palette.text)
                    .lineLimit(1)
                Text(status).font(.caption).foregroundStyle(palette.textMuted).lineLimit(2)
            }
            Spacer(minLength: 0)
            Button {
                tap(!failed)
            } label: {
                Group {
                    if playback.loading == node.id {
                        ProgressView().controlSize(.small)
                    } else {
                        Image(systemName: failed ? "doc" : on ? "pause.fill" : "play.fill")
                    }
                }
                .frame(width: control, height: control)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .foregroundStyle(palette.icon)
            .disabled(fileID.isEmpty || playback.loading != nil)
            .accessibilityLabel(
                failed
                    ? L.cardOpenFile(name: name)
                    : on ? L.commonPause : L.cardPlayFile(name: name))
            menu
        }
    }

    /// status is the line under an audio file's name: what runs for it, else
    /// its kind and size, and the engine that made the transcript its notes
    /// have.
    private var status: String {
        if Recorder.shared.state.transcribingFiles.contains(node.id) { return L.recTranscribing }
        guard let size = node.data?["size"]?.int, size > 0 else { return "" }
        let bytes = ByteCountFormatter.string(fromByteCount: Int64(size), countStyle: .file)
        let type = (name as NSString).pathExtension.uppercased()
        let detail = type.isEmpty ? bytes : L.cardFileDetail(kind: type, size: bytes)
        guard let engine = Self.engine(node.data?["transcribed_by"]?.string),
            !notes.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        else { return detail }
        return L.cardTranscribedBy(detail: detail, engine: engine)
    }

    /// engine names the speech provider that made a transcript, as
    /// recording.EngineName does, or nil for none.
    static func engine(_ provider: String?) -> String? {
        switch provider {
        case nil, "": nil
        case "soniox": "Soniox"
        case "moss": "MOSS"
        case "on-device": L.recEngineOnDevice
        default: L.commonEndpoint
        }
    }

    private func card(_ playback: FilePlayback) -> some View {
        let palette = BlockLook.palette
        let playable = Self.media(kind) && !playback.failed.contains(node.id)
        let on = playback.block == node.id && playback.playing
        return HStack(spacing: 12) {
            Button {
                tap(playable)
            } label: {
                HStack(spacing: 12) {
                    Group {
                        if playback.loading == node.id {
                            ProgressView().controlSize(.small)
                        } else {
                            Image(systemName: playable ? (on ? "pause.fill" : "play.fill") : "doc")
                        }
                    }
                    .font(.footnote)
                    .foregroundStyle(palette.accentText)
                    .frame(width: 30, height: 30)
                    .background(palette.accentSoft, in: .circle)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(name).font(.subheadline.weight(.semibold)).foregroundStyle(
                            palette.text
                        )
                        .lineLimit(1)
                        if let size = node.data?["size"]?.int, size > 0 {
                            Text(
                                ByteCountFormatter.string(
                                    fromByteCount: Int64(size), countStyle: .file)
                            )
                            .font(.caption).foregroundStyle(palette.textMuted)
                        }
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .disabled(fileID.isEmpty)
            .accessibilityLabel(playable ? L.cardPlayFile(name: name) : L.cardOpenFile(name: name))
            menu
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.surface, in: .rect(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.divider))
    }

    private func tap(_ playable: Bool) {
        if playable {
            FilePlayback.shared.play(block: node.id, file: fileID, client: editor?.client)
        } else {
            editor?.onOpenFile?(fileID)
        }
    }

    /// play plays the file from at seconds in.
    private func play(_ at: Int) {
        guard Self.media(kind) else { return }
        FilePlayback.shared.play(block: node.id, file: fileID, at: at, client: editor?.client)
    }

    private var menu: some View {
        let items = Self.menuItems(node, canEdit: canEdit, editor: editor)
        return Menu {
            ForEach(items, id: \.title) { BlockMenuItem(item: $0) }
        } label: {
            Image(systemName: "ellipsis")
                .frame(width: control, height: control)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(BlockLook.palette.icon)
        .accessibilityLabel(L.cardActions)
    }

    /// actions are what block offers now, for VoiceOver at the caret, which
    /// can't reach the block in the text.
    static func actions(_ block: DocNode, canEdit: Bool, editor: EditorController?) -> [BlockAction]
    {
        menuItems(block, canEdit: canEdit, editor: editor).filter(\.enabled)
    }

    /// menuItems are the items of block's menu, in editor's page: to show
    /// it, its notes, and a transcript of them for audio.
    private static func menuItems(_ block: DocNode, canEdit: Bool, editor: EditorController?)
        -> [BlockAction]
    {
        let file = block.data?["file_id"]?.string ?? ""
        let notes = !(block.data?["notes"]?.string ?? "").isEmpty
        var out = [
            BlockAction(title: L.cardOpenExternally, symbol: "eye", enabled: !file.isEmpty) {
                editor?.onOpenFile?(file)
            },
            BlockAction(
                title: notes ? L.videoEditNotes : L.videoAddNotes, symbol: "text.alignleft",
                enabled: canEdit
            ) {
                editor?.onEditNotes?(block)
            },
        ]
        let spoken = block.data?["notes"]?.string ?? ""
        let hidden = speakersHidden(block)
        if MobileFileSpeakers(spoken, names(block)) != "[]" {
            out.append(
                BlockAction(
                    title: hidden ? L.cardShowSpeakers : L.cardHideSpeakers, symbol: "person.2",
                    enabled: canEdit
                ) {
                    // Into the block as it is now, with what synced in since
                    // the menu was made.
                    guard let editor, let now = editor.node(ofBlock: block.id) else { return }
                    var data = now.data ?? [:]
                    data["speakers_hidden"] = hidden ? nil : .bool(true)
                    editor.setData(data, ofBlock: block.id)
                })
        }
        if notes, media(kind(block)) {
            let (list, title) = (names(block), block.data?["name"]?.string ?? L.cardFile)
            out.append(
                BlockAction(title: L.recCopyTranscript, symbol: "doc.on.doc") {
                    UIPasteboard.general.string = MobileFileTranscriptText(spoken, list, hidden)
                })
            out.append(
                BlockAction(title: L.recInsertTranscript, symbol: "text.insert", enabled: canEdit) {
                    let json = MobileFileTranscriptBlocks(spoken, list, hidden)
                    guard
                        let made = try? JSONDecoder().decode(
                            Transcript.self, from: Data(json.utf8))
                    else { return }
                    Recorder.put(made, after: block.id, in: editor)
                })
            // The agent reads the notes with the block.
            out.append(
                BlockAction(title: L.recSummarize, symbol: "sparkles", enabled: canEdit) {
                    editor?.onSummarize?(block, L.recSummarizePrompt(title: title))
                })
            out.append(LineClock.followAction)
        }
        if kind(block) == "audio" {
            let running = Recorder.shared.state.transcribingFiles.contains(block.id)
            out.append(
                .transcribe(
                    notes ? L.recTranscribeAgain : L.recTranscribe, page: editor?.pageID,
                    enabled: canEdit && !running
                ) { provider in
                    if notes {
                        editor?.onReplaceNotes?(block, provider)
                    } else if let page = editor?.pageID {
                        Recorder.shared.transcribeFile(
                            page: page, block: block.id, provider: provider)
                    }
                })
        }
        return out
    }
}
