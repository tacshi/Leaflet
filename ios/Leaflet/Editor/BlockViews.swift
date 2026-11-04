import AVKit
import Charts
import LeafletCore
import SwiftDraw
import SwiftUI
import Synchronization
import UIKit
import WebKit

// The views of blocks without text, inside the page's text: each kind
// drawn as the desktop draws it, math and diagrams by the same pages.

/// BlockLook is the page's palette, for blocks' views, which TextKit makes
/// apart from the SwiftUI views that know it.
@MainActor
enum BlockLook {
    static var palette = Palette.palettes["Blue"]!["light"]!
    /// textSize is the page's text size, which tables' cells follow.
    static var textSize: CGFloat = 17
}

/// PageLinks are what a subpage's card needs of the app: its page's
/// title, read where the app's pages are observed, and opening it; openAt
/// opens a page at one of its blocks, for a link to it, and plays a video
/// or a recording from a time, in seconds, for a link to a moment of one,
/// -1 for none.
struct PageLinks {
    let title: @MainActor (String) -> String?
    let open: @MainActor (String) -> Void
    var openAt: (@MainActor (String, String, Int) -> Void)?
}

/// BlockAction is something a block offers: Play or Pause, or an item of
/// its menu. A block's menu and VoiceOver's actions at the caret, which
/// can't reach the block in the text, offer the same.
struct BlockAction {
    let title: String
    var symbol = ""
    var enabled = true
    var destructive = false
    let run: @MainActor () -> Void
}

/// BlockCard shows a block without text.
struct BlockCard: View {
    let node: DocNode
    var links: PageLinks?
    /// edit opens the block's editor, or is nil on a page that can't be
    /// changed.
    var edit: (@MainActor (DocNode) -> Void)?
    /// editor is the page's, which a recording's transcript goes in by.
    weak var editor: EditorController?
    /// onHeight takes the size a block measured itself at, as a
    /// recording does, which grows with its transcript.
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// page returns the web page that draws a block, or nil for one SwiftUI
    /// draws. BlockViews shows a web block in a web view of its own.
    @MainActor static func page(for node: DocNode) -> String? {
        let d = node.data ?? [:]
        let palette = BlockLook.palette
        switch node.type {
        case "math_equation":
            return MobileMathPage(d[MobileSourceKey(node.type)]?.string ?? "")
        case "diagram":
            return MobileDiagramPage(
                d[MobileSourceKey(node.type)]?.string ?? "", palette.surface.hex)
        case "embed":
            return MobileEmbedPage(d["html"]?.string ?? "", palette.surface.hex, palette.isDark)
        default:
            return nil
        }
    }

    /// scaled is a height that holds text, as the text size the person
    /// chose scales the page's text: a table's rows, a card, a chart's
    /// labels. The size is the traits' the page lays out in, which its
    /// fonts follow, not the app's.
    static func scaled(_ height: CGFloat) -> CGFloat {
        UIFontMetrics(forTextStyle: .body).scaledValue(
            for: height, compatibleWith: UITraitCollection.current)
    }

    /// height is the room a block takes at width.
    static func height(_ node: DocNode, width: CGFloat) -> CGFloat {
        switch node.type {
        case "divider": 24
        case "image": min(width * 0.6, 420)
        // As tall as it measured itself, its rows as tall as their text,
        // or a line a row until then.
        case "table":
            BlockHeights.of(node.id, width: width)
                ?? CGFloat(max(TableGrid.rows(node).count, 1)) * scaled(40) + 2
        // Math and diagrams take the height their page measures, once it
        // has; until then, a guess.
        // At least the room of their Edit, an empty formula too.
        case "math_equation": max(BlockHeights.of(node.id, width: width) ?? 96, 44)
        case "diagram":
            max(BlockHeights.of(node.id, width: width) ?? min(max(width * 0.3, 160), 320), 44)
        // As tall as it measured itself, with its notes, or its player
        // and title until then.
        case "video": BlockHeights.of(node.id, width: width) ?? min(width * 9 / 16, 420) + 44
        case "chart": scaled(300)
        // Short without a description or picture to make room for, as on
        // the desktop.
        case "link_preview": scaled(LinkCardBlock.tall(node) ? 96 : 64)
        // As tall as it measured itself, or its header alone until then.
        case "recording": BlockHeights.of(node.id, width: width) ?? scaled(64)
        // As tall as it was made, or as its content, when that is taller,
        // up to 1200: an embed never scrolls within itself, as on the
        // desktop.
        case "embed":
            max(
                CGFloat(min(max(node.data?["height"]?.int ?? 320, 120), 1200)),
                min(BlockHeights.of(node.id, width: width) ?? 0, 1200))
        default: scaled(64)
        }
    }

    var body: some View {
        switch node.type {
        case "divider":
            Divider().frame(maxHeight: .infinity)
        case "image":
            ImageBlock(node: node)
        case "video":
            VideoBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        case "chart":
            ChartBlock(node: node)
        case "table":
            TableGrid(node: node, edit: edit, onHeight: onHeight)
        case "subpage":
            SubpageBlock(node: node, links: links)
        case "recording":
            RecordingBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        case "link_preview":
            LinkCardBlock(node: node)
        default:
            CardBlock(node: node)
        }
    }
}

/// LinkCardBlock is a link card as the desktop draws it: the page's title,
/// its description, its site, and its picture beside them.
struct LinkCardBlock: View {
    let node: DocNode
    @State private var picture: UIImage?

    /// pictures are the cards' pictures as they came, by link, for a card
    /// made again not to ask for its picture again.
    @MainActor private static let pictures = NSCache<NSString, UIImage>()

    /// tall says the card has a description or a picture to make room for.
    static func tall(_ node: DocNode) -> Bool {
        !(node.data?["description"]?.string ?? "").isEmpty
            || !(node.data?["image"]?.string ?? "").isEmpty
    }

    private var link: String { node.data?["url"]?.string ?? "" }
    private var image: String { node.data?["image"]?.string ?? "" }

    private var title: String {
        let title = node.data?["title"]?.string ?? ""
        return title.isEmpty ? link : title
    }

    private var site: String {
        if let site = node.data?["site"]?.string, !site.isEmpty { return site }
        let host = URL(string: link)?.host() ?? ""
        return host.hasPrefix("www.") ? String(host.dropFirst(4)) : host
    }

    var body: some View {
        HStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                if let detail = node.data?["description"]?.string, !detail.isEmpty {
                    Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                }
                Spacer(minLength: 0)
                HStack(spacing: 6) {
                    Image(systemName: "globe").font(.caption2)
                    Text(site).font(.caption).lineLimit(1)
                }
                .foregroundStyle(.secondary)
            }
            .padding(.vertical, 12)
            .padding(.horizontal, 14)
            .frame(maxWidth: .infinity, alignment: .leading)
            if let picture {
                Image(uiImage: picture).resizable().scaledToFill()
                    .frame(width: 150)
                    .frame(maxHeight: .infinity)
                    .clipped()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.quaternary.opacity(0.5))
        .clipShape(.rect(cornerRadius: 10))
        .task(id: image) {
            guard !image.isEmpty else {
                picture = nil
                return
            }
            if let kept = Self.pictures.object(forKey: image as NSString) {
                picture = kept
                return
            }
            // From the web through the core, as the desktop reads one: http
            // too, and never this device's network.
            guard let data = try? await ImageBlock.client?.picture(image), !data.isEmpty,
                let made = await Task.detached(operation: { UIImage(data: data) }).value
            else { return }
            Self.pictures.setObject(made, forKey: image as NSString)
            picture = made
        }
    }
}

/// SubpageBlock is a card for a page inside this one, with its title, which
/// opens it.
struct SubpageBlock: View {
    let node: DocNode
    let links: PageLinks?

    private var pageID: String { node.data?["page_id"]?.string ?? "" }

    var body: some View {
        let palette = BlockLook.palette
        // A page deleted, or not on this device, keeps its card, as on the
        // desktop, for Undo to bring it back to: it says so, and opens
        // nothing.
        let title = links?.title(pageID)
        Button {
            links?.open(pageID)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "doc.text").font(.body).foregroundStyle(palette.icon)
                Group {
                    if let title {
                        Text(title.isEmpty ? L.commonUntitled : title)
                            .fontWeight(.semibold).foregroundStyle(palette.text)
                    } else {
                        Text(L.editorPageNotFound).foregroundStyle(palette.textMuted)
                    }
                }
                .lineLimit(1)
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold))
                    .foregroundStyle(palette.textMuted)
            }
            .padding(.horizontal, 12)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(palette.surface, in: .rect(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.divider))
            .contentShape(.rect(cornerRadius: 8))
        }
        .buttonStyle(.plain)
        .hoverEffect(.highlight)
        .disabled(pageID.isEmpty || links == nil || title == nil)
    }
}

/// ImageBlock shows an image, from the device's files or the web.
struct ImageBlock: View {
    let node: DocNode
    @State private var picture: Picture?

    /// client is the core, which has the pictures kept with pages, set as
    /// the app opens it.
    static var client: CoreClient?

    /// Picture is an image as it shows: a bitmap, or an SVG, which UIImage
    /// can't read and SwiftDraw draws.
    private enum Picture: Sendable {
        case bitmap(UIImage)
        case svg(SVG)
    }

    private var link: String { node.data?["url"]?.string ?? "" }
    private var fileID: String { node.data?["file_id"]?.string ?? "" }

    var body: some View {
        Group {
            switch picture {
            case .bitmap(let image):
                Image(uiImage: image).resizable().scaledToFit()
            case .svg(let svg):
                SVGView(svg: svg).resizable().aspectRatio(svg.size, contentMode: .fit)
            case nil:
                placeholder
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .task(id: [fileID, link]) {
            picture = nil
            var data: Data?
            // A picture kept with the page, by its file: its url is a path
            // on the device that added it, and another device downloads it.
            if !fileID.isEmpty, let client = Self.client,
                let path = try? await client.localFile(fileID)
            {
                data = await Task.detached { FileManager.default.contents(atPath: path) }.value
            } else if link.hasPrefix("file://") {
                // A file the page keeps on this device: its path may have
                // spaces, which a URL string can't.
                let path = LocalFiles.resolve(String(link.dropFirst("file://".count)))
                data = await Task.detached { FileManager.default.contents(atPath: path) }.value
            } else {
                // From the web through the core, as the desktop reads one:
                // http too, and never this device's network.
                data = try? await Self.client?.picture(link)
            }
            guard let data, !data.isEmpty else { return }
            picture = await Task.detached { Self.picture(data) }.value
        }
    }

    private var placeholder: some View {
        RoundedRectangle(cornerRadius: 8).fill(.quaternary)
    }

    /// picture is data as it shows: a bitmap, an SVG, or nil when it is
    /// neither.
    private nonisolated static func picture(_ data: Data) -> Picture? {
        if let image = UIImage(data: data) {
            return .bitmap(image)
        }
        return SVG(data: SVGPercents.resolved(data)).map { .svg($0) }
    }
}

/// VideoPlayback is which videos play in place of their pictures, by
/// block: play is pressed on the block, or chosen from VoiceOver's actions
/// at the caret, which can't reach the block's button; and where each
/// plays from, as a time in its notes or a link to a moment of it asks.
/// One plays at a time, as one person listens to one: the one that starts
/// pauses the others, and a recording playing.
@MainActor
@Observable
final class VideoPlayback {
    static let shared = VideoPlayback()
    /// playing are the blocks whose players show, playing or paused.
    private(set) var playing: Set<String> = []
    /// views are the players' web views, by block, which another starting
    /// pauses, and current the block of the one that started last.
    @ObservationIgnored private var views: [String: Weak] = [:]
    @ObservationIgnored private var current: String?
    @ObservationIgnored private var watching: Task<Void, Never>?

    private final class Weak {
        weak var view: WKWebView?
        init(_ view: WKWebView) { self.view = view }
    }
    /// start is the time a block's player loads at, in seconds, and loads
    /// how many times it loaded there, for the same time asked again to
    /// load it again.
    var start: [String: Int] = [:]
    var loads: [String: Int] = [:]
    /// seeks are the scripts a block's player runs to go to a time without
    /// loading again, where its site's player takes them.
    var seeks: [String: Seek] = [:]

    /// Seek is a script for a player to run once.
    struct Seek: Equatable {
        let id = UUID()
        let script: String
    }

    /// play shows the player of video block id, which plays alone.
    func play(_ id: String) {
        playing.insert(id)
        playAlone(id)
    }

    /// seek plays video block id, of link, from at seconds in: its player
    /// goes there, or loads again there where its site's can't be told
    /// to, or starts there if it has not.
    func seek(_ id: String, link: String, at: Int) {
        guard playing.contains(id) else {
            start[id] = at
            play(id)
            return
        }
        playAlone(id)
        let script = MobileVideoSeek(link, at)
        // A player whose web view left the page, as many blocks showed
        // since, loads there as it shows again: a script has no page to run
        // in, and the view made next runs none it was made with.
        if script.isEmpty || views[id]?.view?.window == nil {
            start[id] = at
            loads[id, default: 0] += 1
        } else {
            seeks[id] = Seek(script: script)
        }
    }

    /// stop takes the blocks of ids out of play, as their page closes.
    func stop(_ ids: Set<String>) {
        playing.subtract(ids)
        for id in ids {
            start[id] = nil
            loads[id] = nil
            seeks[id] = nil
            views[id] = nil
        }
    }

    /// playAlone pauses every player but that of block id, which starts
    /// now, and a recording playing.
    func playAlone(_ id: String) {
        current = id
        for (other, weak) in views where other != id {
            weak.view?.pauseAllMediaPlayback(completionHandler: nil)
        }
        Recorder.shared.pausePlaying()
    }

    /// pauseAll pauses every player, as a recording starts.
    func pauseAll() {
        current = nil
        for weak in views.values {
            weak.view?.pauseAllMediaPlayback(completionHandler: nil)
        }
    }

    /// register keeps the web view of block id's player, for another
    /// starting to pause it. While two show, or one as a recording plays,
    /// one started from its own controls, which the app isn't told of, is
    /// found within a second.
    func register(_ view: WKWebView, for id: String) {
        views[id] = Weak(view)
        views = views.filter { $0.value.view != nil }
        guard watching == nil else { return }
        watching = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                guard let self else { return }
                self.views = self.views.filter { $0.value.view != nil }
                if self.views.isEmpty {
                    self.watching = nil
                    return
                }
                // One player alone, with no recording playing, has none to
                // pause. A recording that starts pauses the players and
                // leaves none current: each is looked at then.
                guard self.views.count > 1 || Recorder.shared.player?.isPlaying == true
                else { continue }
                for (id, weak) in self.views where id != self.current {
                    if await weak.view?.requestMediaPlaybackState() == .playing {
                        self.playAlone(id)
                        break
                    }
                }
            }
        }
    }
}

/// VideoBlock shows a video's picture and title, and its player in place
/// once play is pressed; under them its notes, a line's time beside it, as
/// a recording's transcript shows them, which plays the video from there,
/// a long one folded to its first lines. It is as tall as it needs, which
/// it tells onHeight.
struct VideoBlock: View {
    let node: DocNode
    /// canEdit says the page can be changed: the notes only show otherwise.
    var canEdit = false
    /// editor is the page's, which writes the notes and asks an agent.
    weak var editor: EditorController?
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// width is the block's, which the player's height follows.
    @State private var width: CGFloat = 0
    /// open shows long notes whole.
    @State private var open = false
    /// made is the block's player, as the core makes it off the main
    /// thread, a Bilibili video's asking its site for its part the first
    /// time, with the time it was loaded again for.
    @State private var made: Made?
    @ScaledMetric(relativeTo: .subheadline) private var control: CGFloat = 28

    private var playback: VideoPlayback { VideoPlayback.shared }
    private var playing: Bool { playback.playing.contains(node.id) }
    private var link: String { node.data?["url"]?.string ?? "" }
    private var title: String {
        let title = node.data?["title"]?.string ?? ""
        return title.isEmpty ? link : title
    }

    private struct Player: Decodable, Sendable {
        let page: String
        let origin: String
        let thumbnail: String
    }

    private struct Made {
        let player: Player
        let loads: Int
    }

    /// Line is a line of the notes, as the core reads them.
    struct Line: Decodable {
        let at: Int
        var time: String?
        let parts: [Part]

        struct Part: Decodable {
            let text: String
            let at: Int
        }
    }

    var body: some View {
        let start = playback.start[node.id] ?? -1
        let loads = playback.loads[node.id] ?? 0
        let player = made?.player
        let lines = Self.lines(node)
        VStack(alignment: .leading, spacing: 0) {
            // The black area takes the player's room; the picture fills it
            // without setting its size, so it never spills past the block.
            Color.black
                .frame(maxWidth: .infinity)
                .frame(height: min(max(width, 1) * 9 / 16, 420))
                .overlay {
                    if playing, let made {
                        // Loaded again for the same time asked again.
                        WebPage(
                            html: made.player.page + "<!-- \(made.loads) -->",
                            base: URL(string: made.player.origin), interactive: true,
                            script: playback.seeks[node.id], player: node.id)
                    } else if let thumb = player?.thumbnail, let url = URL(string: thumb) {
                        AsyncImage(url: url) {
                            $0.resizable().scaledToFill()
                        } placeholder: {
                            Color.black
                        }
                    }
                }
                .overlay {
                    if !playing {
                        Button {
                            playback.play(node.id)
                        } label: {
                            Image(systemName: "play.fill")
                                .font(.title)
                                .foregroundStyle(.white)
                                .frame(width: 64, height: 64)
                                .glassEffect(.regular.interactive(), in: .circle)
                        }
                        .buttonStyle(.plain)
                        .disabled(player == nil)
                    }
                }
                .clipped()
                .clipShape(.rect(cornerRadius: 10))
            // Its title, as its site's page has it under the player, or its
            // link for a block that has none.
            HStack(spacing: 8) {
                Text(title)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(BlockLook.palette.text)
                    .lineLimit(1)
                Spacer(minLength: 0)
                menu(lines)
            }
            .frame(minHeight: control)
            .padding(.top, 4)
            if !lines.isEmpty {
                notes(lines).padding(.top, 4)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .onGeometryChange(for: CGSize.self) {
            $0.size
        } action: {
            width = $0.width
            onHeight?(CGSize(width: $0.width, height: $0.height + 2))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .task(id: "\(start) \(loads) \(link)") {
            let link = link
            let player = await Task.detached {
                try? JSONDecoder().decode(
                    Player.self, from: Data(MobileVideoPlayerAt(link, start).utf8))
            }.value
            made = player.map { Made(player: $0, loads: loads) }
        }
    }

    /// play plays the video from at seconds in.
    private func play(_ at: Int) {
        playback.seek(node.id, link: link, at: at)
    }

    /// notes are the video's notes, each line after its time, if it has
    /// one, which a tap on the line plays from; a time in a line without
    /// one plays from there. They are in the page's font and size, in a
    /// panel of their own, as a video's description is on its site, tinted
    /// as a code block is.
    private func notes(_ lines: [Line]) -> some View {
        let palette = BlockLook.palette
        // The times' column is as wide as the longest of them, its digits
        // all as wide.
        let widest =
            lines.compactMap(\.time).max { $0.count < $1.count }
            .map { String($0.map { $0.isNumber ? "0" : $0 }) } ?? "0:00"
        let folds = MobileFoldsTranscript(lines.count)
        let shown = folds && !open ? Array(lines.prefix(Int(MobileFoldedLines))) : lines
        return VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(shown.enumerated()), id: \.offset) { _, line in
                Group {
                    if let time = line.time, line.at >= 0 {
                        TimedRow(time: time, widest: widest, style: .notes) {
                            play(line.at)
                        } content: {
                            Text(text(line.parts, links: false))
                                .multilineTextAlignment(.leading)
                        }
                    } else {
                        // Its times are links, which play from there.
                        Text(text(line.parts, links: true))
                            .multilineTextAlignment(.leading)
                            .environment(
                                \.openURL,
                                OpenURLAction { url in
                                    guard url.scheme == Self.seekScheme,
                                        let at = Int(url.host() ?? "")
                                    else { return .systemAction }
                                    play(at)
                                    return .handled
                                })
                    }
                }
                .font(Fonts.pageText(size: BlockLook.textSize))
                .foregroundStyle(palette.text)
                .contextMenu { lineMenu(line) }
            }
            if folds {
                FoldButton(open: $open, count: lines.count, indent: TimeStyle.notes.indent(widest))
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.code, in: .rect(cornerRadius: 10))
    }

    /// seekScheme is the scheme of the links a time in a line is, to play
    /// from: leaflet-seek://83.
    private static let seekScheme = "leaflet-seek"

    /// text is a line's text, its times in the accent, and, with links,
    /// links that play from each.
    private func text(_ parts: [Line.Part], links: Bool) -> AttributedString {
        var out = AttributedString()
        for p in parts {
            var run = AttributedString(p.text)
            if p.at >= 0 {
                run.foregroundColor = BlockLook.palette.accent
                if links { run.link = URL(string: "\(Self.seekScheme)://\(p.at)") }
            }
            out += run
        }
        return out
    }

    /// lineMenu is a line's menu, from a long press: a link to the moment
    /// it is about, to put in another page or a chat.
    @ViewBuilder private func lineMenu(_ line: Line) -> some View {
        let at = line.at >= 0 ? line.at : line.parts.first { $0.at >= 0 }?.at
        if let at, let page = editor?.pageID, !page.isEmpty {
            Button(L.videoCopyMoment(time: MobileClock(at)), systemImage: "link") {
                UIPasteboard.general.string = MobileMomentLink(page, node.id, at)
            }
        }
    }

    /// lines are the notes of block, a line each, as the core reads them.
    static func lines(_ block: DocNode) -> [Line] {
        let notes = MobileVideoNotes(block.data?["notes"]?.string ?? "")
        return (try? JSONDecoder().decode([Line].self, from: Data(notes.utf8))) ?? []
    }

    /// actions are what block offers now, for VoiceOver at the caret,
    /// which can't reach the block in the text: Play, each time in its
    /// notes, as a tap on its line plays from it, and its menu's items.
    static func actions(_ block: DocNode, canEdit: Bool, editor: EditorController?)
        -> [BlockAction]
    {
        let playback = VideoPlayback.shared
        let link = block.data?["url"]?.string ?? ""
        let lines = Self.lines(block)
        var out: [BlockAction] = []
        if !playback.playing.contains(block.id) {
            out.append(BlockAction(title: L.recPlay) { playback.play(block.id) })
        }
        func offer(_ title: String, at: Int) {
            out.append(
                BlockAction(title: L.videoPlay(title: title)) {
                    playback.seek(block.id, link: link, at: at)
                })
        }
        for line in lines {
            let text = line.parts.map(\.text).joined()
            if let time = line.time, line.at >= 0 {
                offer(text.isEmpty ? time : time + " " + text, at: line.at)
            } else {
                // A line with one time is named by the whole of it, one
                // with more by each time.
                let times = line.parts.filter { $0.at >= 0 }
                for p in times { offer(times.count == 1 ? text : p.text, at: p.at) }
            }
        }
        return out + menuItems(block, lines, canEdit: canEdit, editor: editor).joined()
    }

    /// menuItems are the sections of the menu of block, in editor's page,
    /// as the desktop's context menu of it has them: Cut and Copy; a link
    /// to the block, its link, its notes and an agent's summary of them
    /// from what is said in the video, where its site gives its captions;
    /// and Delete, as a recording's menu has. A page that can't be changed
    /// offers only what copies.
    private static func menuItems(
        _ block: DocNode, _ lines: [Line], canEdit: Bool, editor: EditorController?
    ) -> [[BlockAction]] {
        let link = block.data?["url"]?.string ?? ""
        let title = block.data?["title"]?.string ?? ""
        var copies: [BlockAction] = []
        if canEdit {
            copies.append(
                BlockAction(title: L.editorCut, symbol: "scissors") { editor?.cutBlock(block.id) })
        }
        copies.append(
            BlockAction(title: L.commonCopy, symbol: "doc.on.doc") { editor?.copyBlock(block.id) })
        var links: [BlockAction] = []
        if let page = editor?.pageID, !page.isEmpty {
            links.append(
                BlockAction(title: L.editorCopyBlockLink, symbol: "link") {
                    UIPasteboard.general.string = MobileBlockLink(page, block.id)
                })
        }
        guard canEdit else { return [copies, links] }
        links.append(
            BlockAction(title: L.editorEditMediaLink, symbol: "pencil") {
                editor?.onEditLink?(block)
            })
        links.append(
            BlockAction(
                title: lines.isEmpty ? L.videoAddNotes : L.videoEditNotes,
                symbol: "text.alignleft"
            ) {
                editor?.onEditNotes?(block)
            })
        if MobileVideoHasCaptions(link) {
            links.append(
                BlockAction(title: L.videoSummarize, symbol: "sparkles") {
                    editor?.onSummarize?(
                        block, L.videoSummarizePrompt(title: title.isEmpty ? link : title))
                })
        }
        // The block goes as one step to undo.
        let delete = BlockAction(title: L.commonDelete, symbol: "trash", destructive: true) {
            editor?.removeBlock(block.id)
        }
        return [copies, links, [delete]]
    }

    /// menu is the block's, of its menuItems.
    private func menu(_ lines: [Line]) -> some View {
        let palette = BlockLook.palette
        let sections = Self.menuItems(node, lines, canEdit: canEdit, editor: editor)
        return Menu {
            ForEach(sections.indices, id: \.self) { i in
                Section {
                    ForEach(sections[i], id: \.title) { item in
                        Button(
                            item.title, systemImage: item.symbol,
                            role: item.destructive ? .destructive : nil, action: item.run)
                    }
                }
            }
        } label: {
            Image(systemName: "ellipsis")
                .frame(width: control, height: control)
                .contentShape(.rect)
        }
        .foregroundStyle(palette.icon)
    }
}

/// ChartBlock draws a chart: bars, a line or a pie, of its labels and
/// series, with its title and caption.
struct ChartBlock: View {
    let node: DocNode

    private struct Point: Identifiable {
        let id: Int
        let label: String
        let series: String
        let value: Double
    }

    var body: some View {
        let d = node.data ?? [:]
        let kind = d["chart_type"]?.string ?? "bar"
        // A year or a count is a whole number, shown without a fraction.
        let labels = (d["labels"]?.array ?? []).map {
            $0.string ?? ($0.int.map { "\($0)" } ?? $0.double.map { "\($0)" } ?? "")
        }
        var points: [Point] = []
        for (s, series) in (d["series"]?.array ?? []).enumerated() {
            let name = series["name"]?.string ?? ""
            for (i, v) in (series["values"]?.array ?? []).enumerated() where i < labels.count {
                points.append(
                    Point(id: s * 10_000 + i, label: labels[i], series: name, value: v.double ?? 0))
            }
        }
        let many = Set(points.map(\.series)).count > 1
        return VStack(alignment: .leading, spacing: 8) {
            if let title = d["title"]?.string, !title.isEmpty {
                Text(title).font(.subheadline.weight(.semibold))
            }
            Chart(points) { p in
                switch kind {
                case "line":
                    LineMark(x: .value("", p.label), y: .value("", p.value))
                        .foregroundStyle(by: .value("", p.series))
                        .symbol(by: .value("", p.series))
                case "pie":
                    SectorMark(
                        angle: .value("", p.value), innerRadius: .ratio(0.5), angularInset: 1
                    )
                    .foregroundStyle(by: .value("", p.label))
                default:
                    BarMark(x: .value("", p.label), y: .value("", p.value))
                        .foregroundStyle(by: .value("", many ? p.series : ""))
                        .position(by: .value("", p.series))
                }
            }
            .chartLegend(kind == "pie" || many ? .visible : .hidden)
            if let caption = d["caption"]?.string, !caption.isEmpty {
                Text(caption).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .background(.quaternary.opacity(0.35), in: .rect(cornerRadius: 10))
    }
}

/// CardBlock is a block the page doesn't draw yet, as a card of what it is.
struct CardBlock: View {
    let node: DocNode

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: symbol).font(.title3).foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.subheadline.weight(.semibold)).lineLimit(1)
                if let detail = node.data?["description"]?.string {
                    Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(.quaternary.opacity(0.5), in: .rect(cornerRadius: 10))
    }

    private var symbol: String {
        switch node.type {
        case "embed": "curlybraces.square"
        case "file": "doc"
        case "recording": "waveform"
        case "subpage": "doc.text"
        default: "square.dashed"
        }
    }

    private var title: String {
        let d = node.data ?? [:]
        return d["title"]?.string ?? d["name"]?.string ?? d["url"]?.string ?? Self.name(node.type)
    }

    /// name is what a block of type is called, as the "/" menu names it.
    nonisolated static func name(_ type: String) -> String {
        switch type {
        case "embed": L.hostSlashEmbed
        case "link_preview": L.hostSlashLinkCard
        case "file": L.hostSlashFile
        case "subpage": L.hostSlashSubpage
        case "image": L.hostSlashImage
        case "video": L.hostSlashVideo
        case "table": L.hostSlashTable
        case "chart": L.hostSlashChart
        case "math_equation": L.hostSlashMath
        case "diagram": L.hostSlashDiagram
        case "divider": L.hostSlashDivider
        default: L.editorUnsupported(type: type)
        }
    }
}

/// WebPage shows HTML in a web view that keeps no data, follows no links,
/// and, unless interactive, neither scrolls nor takes touches but a pinch.
struct WebPage: UIViewRepresentable {
    let html: String
    let base: URL?
    let interactive: Bool
    /// script runs in the page once, as a video's player is told to go to
    /// a time.
    var script: VideoPlayback.Seek?
    /// player is the video block the page plays, which VideoPlayback keeps
    /// to one playing at a time.
    var player: String?

    /// store keeps nothing past the app's run, shared by the blocks' pages.
    @MainActor static let store = WKWebsiteDataStore.nonPersistent()

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        // One store for all of them: each store of its own starts a web
        // process of its own, which is what made blocks slow to show.
        config.websiteDataStore = Self.store
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []
        // Safari's own end of the user agent, which WebKit's lacks: the
        // sites' players, as Vimeo's, play only in a browser they know.
        config.applicationNameForUserAgent = "Version/26.0 Mobile/15E148 Safari/604.1"
        let view = WKWebView(frame: .zero, configuration: config)
        view.isOpaque = false
        view.backgroundColor = .clear
        view.scrollView.backgroundColor = .clear
        view.scrollView.isScrollEnabled = interactive
        view.navigationDelegate = context.coordinator
        context.coordinator.loaded = html
        context.coordinator.ran = script?.id
        if let player { VideoPlayback.shared.register(view, for: player) }
        view.loadHTMLString(
            themed(html, dark: context.environment.colorScheme == .dark), baseURL: base)
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {
        let dark = context.environment.colorScheme == .dark
        if context.coordinator.loaded != html || context.coordinator.dark != dark {
            context.coordinator.loaded = html
            context.coordinator.dark = dark
            view.loadHTMLString(themed(html, dark: dark), baseURL: base)
        }
        if let script, context.coordinator.ran != script.id {
            context.coordinator.ran = script.id
            view.evaluateJavaScript(script.script)
        }
    }

    /// themed marks the page dark, as the desktop marks its frames, for its
    /// dark colors.
    private func themed(_ html: String, dark: Bool) -> String {
        guard dark else { return html }
        return html.replacingOccurrences(of: "<html>", with: "<html data-theme=\"dark\">")
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loaded = ""
        var dark = false
        var ran: UUID?

        func webView(
            _ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
            decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void
        ) {
            // The page itself, and the frames it holds (a video's player),
            // load; a link followed from it does not.
            let ok = action.navigationType == .other || action.targetFrame?.isMainFrame == false
            decisionHandler(ok ? .allow : .cancel)
        }
    }
}

/// TableGrid shows a table's cells, its first row as the header, their
/// text whole: its columns as TableLayout has them, scrolling sideways
/// when they are wider than the page. It tells onHeight how tall it is,
/// its rows as tall as their text.
struct TableGrid: View {
    let node: DocNode
    /// edit opens the table's sheet from a tap, as a click edits a cell on
    /// the desktop; nil on a page that can't be changed.
    var edit: (@MainActor (DocNode) -> Void)?
    var onHeight: (@MainActor (CGSize) -> Void)?
    /// height is the table's, as it last measured.
    @State private var height: CGFloat = 0

    /// rows are the table's cells' text, by row, as the core reads its
    /// cells (mobile.TableText): the rows and columns it says it has, as
    /// far as it has cells for them. The table's last are kept, as it is
    /// laid out and drawn again and again.
    static func rows(_ node: DocNode) -> [[String]] {
        if let (table, rows) = read[node.id], table == node { return rows }
        let raw = (try? JSONEncoder().encode(node)).map { String(decoding: $0, as: UTF8.self) }
        var error: NSError?
        let json = MobileTableText(raw ?? "{}", &error)
        let rows = (try? JSONDecoder().decode([[String]].self, from: Data(json.utf8))) ?? []
        read[node.id] = (node, rows)
        return rows
    }
    @MainActor private static var read: [String: (DocNode, [[String]])] = [:]

    var body: some View {
        GeometryReader { page in
            ScrollView(.horizontal) {
                Group {
                    if let edit {
                        Button {
                            edit(node)
                        } label: {
                            grid(room: page.size.width - 2)
                        }
                        .buttonStyle(.plain)
                    } else {
                        grid(room: page.size.width - 2)
                    }
                }
                // Room for the border's outer half, at either end too.
                .padding(1)
                .onGeometryChange(for: CGFloat.self) {
                    $0.size.height
                } action: {
                    height = $0
                }
            }
            .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
            .defaultScrollAnchor(.topLeading, for: .alignment)
            // The table's room in the text is kept by the page's width,
            // which the table can be wider than.
            .onChange(of: CGSize(width: page.size.width, height: height)) { _, size in
                if size.height > 0 { onHeight?(size) }
            }
        }
    }

    private func grid(room: CGFloat) -> some View {
        let rows = Self.rows(node)
        let columns = rows.map(\.count).max() ?? 0
        let palette = BlockLook.palette
        let rowHeight = BlockCard.scaled(40)
        return TableLayout(
            columns: columns, room: room, rowHeight: rowHeight, wrapTo: BlockCard.scaled(160)
        ) {
            // Each row as many cells as the widest, as on the desktop.
            ForEach(0..<rows.count * columns, id: \.self) { i in
                let (r, c) = (i / columns, i % columns)
                // In the page's font, a little smaller, as the desktop's
                // cells; one line stands in the middle of a row's height,
                // more from its top.
                Text(c < rows[r].count ? rows[r][c] : "")
                    .font(Fonts.pageText(size: BlockLook.textSize * 0.88))
                    .fontWeight(r == 0 ? .semibold : .regular)
                    .foregroundStyle(palette.text)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 8)
                    .frame(minHeight: rowHeight, alignment: .leading)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .background(r == 0 ? palette.panel : .clear)
                    .border(palette.divider, width: 0.5)
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border, lineWidth: 1))
    }
}

/// TableLayout lays a table's cells out row by row, in columns as wide as
/// their widest cell's text on one line, and as the desktop's narrowest
/// at least, which share room as they fill it. Too wide for room, the
/// widest wrap their text down to one width, as long as that leaves them
/// wrapTo wide: past that, as with many columns, they keep their widths
/// and the table is wider than room. No column is wider than room. Each
/// row is as tall as its tallest cell, rowHeight at least.
struct TableLayout: Layout {
    let columns: Int
    let room: CGFloat
    let rowHeight: CGFloat
    let wrapTo: CGFloat

    /// least is a column's narrowest width, as on the desktop.
    static let least: CGFloat = 80

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Void)
        -> CGSize
    {
        let (widths, heights) = plan(subviews)
        return CGSize(width: widths.reduce(0, +), height: heights.reduce(0, +))
    }

    func placeSubviews(
        in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Void
    ) {
        let (widths, heights) = plan(subviews)
        var y = bounds.minY
        for (r, height) in heights.enumerated() {
            var x = bounds.minX
            for (c, width) in widths.enumerated() where r * columns + c < subviews.count {
                subviews[r * columns + c].place(
                    at: CGPoint(x: x, y: y), anchor: .topLeading,
                    proposal: ProposedViewSize(width: width, height: height))
                x += width
            }
            y += height
        }
    }

    /// plan is the columns' widths and the rows' heights.
    private func plan(_ subviews: Subviews) -> (widths: [CGFloat], heights: [CGFloat]) {
        guard columns > 0 else { return ([], []) }
        var ideal = Array(repeating: Self.least, count: columns)
        for (i, cell) in subviews.enumerated() {
            ideal[i % columns] = max(ideal[i % columns], cell.sizeThatFits(.unspecified).width)
        }
        let widths = Self.fit(ideal, in: room, wrapTo: wrapTo)
        var heights = Array(repeating: rowHeight, count: (subviews.count + columns - 1) / columns)
        for (i, cell) in subviews.enumerated() {
            let width = widths[i % columns]
            let fitted = cell.sizeThatFits(ProposedViewSize(width: width, height: nil))
            heights[i / columns] = max(heights[i / columns], fitted.height)
        }
        return (widths, heights)
    }

    /// fit is the columns' widths, from their text's on one line, as
    /// TableLayout has them.
    static func fit(_ ideal: [CGFloat], in room: CGFloat, wrapTo: CGFloat) -> [CGFloat] {
        let widths = ideal.map { min($0, max(room, least)) }
        let total = widths.reduce(0, +)
        guard room > 0, total > 0 else { return widths }
        if total <= room {
            return widths.map { $0 + (room - total) * $0 / total }
        }
        // The level the widest come down to for the columns to fill room:
        // the narrower, under it, keep their widths.
        let sorted = widths.sorted()
        var narrower: CGFloat = 0
        for (k, width) in sorted.enumerated() {
            let level = (room - narrower) / CGFloat(sorted.count - k)
            if level <= width {
                return level < wrapTo ? widths : widths.map { min($0, level) }
            }
            narrower += width
        }
        return widths
    }
}

extension Color {
    /// hex is the color as "#rrggbb", for the core's pages.
    var hex: String {
        var r: CGFloat = 0
        var g: CGFloat = 0
        var b: CGFloat = 0
        var a: CGFloat = 0
        UIColor(self).getRed(&r, green: &g, blue: &b, alpha: &a)
        return String(format: "#%02x%02x%02x", Int(r * 255), Int(g * 255), Int(b * 255))
    }
}

/// LocalFiles finds the files a page keeps on this device. The core keeps
/// their paths whole, and the app's data folder can move when the app is
/// installed again: a path that is gone is looked for in the folder as it
/// is now.
enum LocalFiles {
    static func resolve(_ path: String) -> String {
        if FileManager.default.fileExists(atPath: path) { return path }
        guard let r = path.range(of: "/Leaflet/", options: .backwards),
            let support = try? FileManager.default.url(
                for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil,
                create: false)
        else { return path }
        return support.appending(path: "Leaflet").appending(path: String(path[r.upperBound...]))
            .path
    }
}

/// BlockHeights are the heights blocks measured themselves at, as a web
/// block its page, by block and the width they had, which their room in
/// the text takes: the same page in two windows of different widths, or
/// a window resized, has a height for each width.
enum BlockHeights {
    private static let heights = Mutex<[String: [(width: CGFloat, height: CGFloat)]]>([:])
    /// widths is how many widths a block keeps a height for, the latest:
    /// a window resized goes through many on its way.
    private static let widths = 4

    /// of is the height block id measured itself at, at width.
    static func of(_ id: String, width: CGFloat) -> CGFloat? {
        heights.withLock { $0[id]?.last { abs($0.width - width) < 2 }?.height }
    }

    /// set keeps the height a block measured itself at, at its width, and
    /// reports whether it changed.
    static func set(_ id: String, _ size: CGSize) -> Bool {
        heights.withLock {
            var kept = $0[id] ?? []
            if let i = kept.lastIndex(where: { abs($0.width - size.width) < 2 }) {
                if abs(kept[i].height - size.height) < 1 { return false }
                kept.remove(at: i)
            }
            kept.append((size.width, size.height))
            $0[id] = Array(kept.suffix(widths))
            return true
        }
    }
}

/// WebBlockView is a web block's view: a web view of the core's page, with
/// no data kept and no links followed. It tells onHeight how tall its page
/// is, or an embed's content, to grow to it.
final class WebBlockView: WKWebView, WKNavigationDelegate {
    var onHeight: ((CGSize) -> Void)?
    /// pinned keeps the page at its top.
    private var pinned: NSKeyValueObservation?

    /// measure posts the page's height as it loads and as it changes.
    private static let measure = """
        (function () {
          function send() {
            webkit.messageHandlers.size.postMessage(Math.ceil(document.documentElement.getBoundingClientRect().height));
          }
          addEventListener("load", function () { send(); new ResizeObserver(send).observe(document.body); });
        })();
        """

    /// embedSize posts an embed's content height, as its frame says it
    /// to the page, when it is taller than the frame, or shorter: the
    /// desktop grows the block to it, up to 1200, and back. Content that
    /// fills the frame, as a page as tall as its window, is as tall,
    /// margins aside, and would otherwise grow the block without end.
    private static let embedSize = """
        addEventListener("message", function (e) {
          var z = e.data && e.data.leafletSize;
          if (z && typeof z[0] === "number" && Math.abs(z[0] - innerHeight) > 24)
            webkit.messageHandlers.size.postMessage(Math.min(z[0], 1200));
        });
        """

    /// noRTC takes WebRTC away from each frame of the page as it starts,
    /// before any script of its own runs (mobile.NoRTCScript): those an
    /// embed makes itself too, whose WebRTC would reach the network around
    /// the embed's policy, which WebKit's webrtc directive does not block.
    static let noRTC = MobileNoRTCScript()

    init(html: String, interactive: Bool) {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = WebPage.store
        config.allowsInlineMediaPlayback = true
        config.userContentController.addUserScript(
            WKUserScript(
                source: Self.noRTC, injectionTime: .atDocumentStart, forMainFrameOnly: false))
        let relay = SizeRelay()
        config.userContentController.add(relay, name: "size")
        config.userContentController.addUserScript(
            WKUserScript(
                source: interactive ? Self.embedSize : Self.measure,
                injectionTime: .atDocumentEnd, forMainFrameOnly: true))
        if interactive {
            // An embed's sound plays from the first tap, as on the desktop:
            // iOS otherwise starts Web Audio only once a tap has ended, and
            // the tap that starts it is silent.
            config.mediaTypesRequiringUserActionForPlayback = []
        }
        super.init(frame: .zero, configuration: config)
        relay.view = self
        isOpaque = false
        backgroundColor = .clear
        scrollView.backgroundColor = .clear
        // An embed is as tall as the page gives it, as on the desktop: it
        // never scrolls within itself, nor when what it focuses would
        // move it; the page scrolls past it.
        scrollView.isScrollEnabled = false
        scrollView.contentInsetAdjustmentBehavior = .never
        pinned = scrollView.observe(\.contentOffset, options: [.new]) { view, _ in
            MainActor.assumeIsolated {
                if view.contentOffset != .zero { view.contentOffset = .zero }
            }
        }
        navigationDelegate = self
        // A formula or a diagram is drawn, not used: touches on it are the
        // page's, as its menu, a selection or the caret.
        isUserInteractionEnabled = interactive
        let dark = BlockLook.palette.isDark
        loadHTMLString(
            dark
                ? html.replacingOccurrences(of: "<html>", with: "<html data-theme=\"dark\">")
                : html,
            baseURL: nil)
    }

    required init?(coder: NSCoder) { fatalError("not coded") }

    func webView(
        _ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
        decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void
    ) {
        let ok = action.navigationType == .other || action.targetFrame?.isMainFrame == false
        decisionHandler(ok ? .allow : .cancel)
    }

    /// SizeRelay passes the page's height on, holding its view weakly: the
    /// web view's content controller keeps it. Only the page's own frame
    /// says it, which the app made: an embed's frame, or one it makes, can
    /// post to the app's handlers too, and each height lays the page out
    /// again. The heights that come in one turn of the main run loop go on
    /// as one, the last.
    @MainActor
    private final class SizeRelay: NSObject, WKScriptMessageHandler {
        weak var view: WebBlockView?
        /// height is the last height posted, waiting for the turn to end.
        private var height: CGFloat?

        func userContentController(
            _ c: WKUserContentController, didReceive message: WKScriptMessage
        ) {
            guard message.frameInfo.isMainFrame, let h = message.body as? Double, h > 0,
                h.isFinite
            else { return }
            let waiting = height != nil
            height = CGFloat(h)
            guard !waiting else { return }
            DispatchQueue.main.async { [weak self] in self?.relay() }
        }

        private func relay() {
            guard let h = height else { return }
            height = nil
            guard let view else { return }
            view.onHeight?(CGSize(width: view.bounds.width, height: h))
        }
    }
}
