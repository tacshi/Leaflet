import AVKit
import Charts
import ImageIO
import LeafletCore
import SwiftDraw
import SwiftUI
import Synchronization
import UIKit
import WebKit

@MainActor
enum BlockLook {
    static var palette = Palette.palettes["Blue"]!["light"]!
    static var textSize: CGFloat = 17
}

struct PageLinks {
    let title: @MainActor (String) -> String?
    let open: @MainActor (String) -> Void
    var openAt: (@MainActor (String, String, Int) -> Void)?
}

struct BlockAction {
    let title: String
    var symbol = ""
    var enabled = true
    var destructive = false
    var choices: [BlockAction] = []
    let run: @MainActor () -> Void

    var flat: [BlockAction] {
        guard !choices.isEmpty else { return [self] }
        return choices.map { c in
            BlockAction(title: "\(title): \(c.title)", enabled: c.enabled, run: c.run)
        }
    }

    @MainActor static func transcribe(
        _ title: String, page: String?, enabled: Bool,
        run: @escaping @MainActor (String) -> Void
    ) -> BlockAction {
        let picked = page.flatMap { Recorder.shared.choices(for: $0) }
        var choices: [BlockAction] = []
        if let picked, picked.choices.count > 1 {
            choices = picked.choices.map { p in
                BlockAction(
                    title: p.name, symbol: p.id == picked.current ? "checkmark" : "",
                    enabled: enabled
                ) { run(p.id) }
            }
        }
        return BlockAction(
            title: title, symbol: "captions.bubble", enabled: enabled, choices: choices
        ) { run("") }
    }
}

struct BlockMenuItem: View {
    let item: BlockAction

    var body: some View {
        if item.choices.isEmpty {
            Button(
                item.title, systemImage: item.symbol, role: item.destructive ? .destructive : nil,
                action: item.run
            )
            .disabled(!item.enabled)
        } else {
            Menu(item.title, systemImage: item.symbol) {
                ForEach(item.choices, id: \.title) { c in
                    if c.symbol.isEmpty {
                        Button(c.title, action: c.run)
                    } else {
                        Button(c.title, systemImage: c.symbol, action: c.run)
                    }
                }
            }
            .disabled(!item.enabled)
        }
    }
}

struct BlockCard: View {
    let node: DocNode
    var links: PageLinks?
    var edit: (@MainActor (DocNode) -> Void)?
    weak var editor: EditorController?
    var onHeight: (@MainActor (CGSize) -> Void)?
    @MainActor static func page(for node: DocNode) -> String? {
        let d = node.data ?? [:]
        let palette = BlockLook.palette
        switch node.type {
        case "diagram":
            return MobileDiagramPage(
                d[MobileSourceKey(node.type)]?.string ?? "", palette.surface.hex)
        case "embed":
            return MobileEmbedPage(d["html"]?.string ?? "", palette.surface.hex, palette.isDark)
        default:
            return nil
        }
    }

    static func scaled(_ height: CGFloat) -> CGFloat {
        UIFontMetrics(forTextStyle: .body).scaledValue(
            for: height, compatibleWith: UITraitCollection.current)
    }

    static func height(_ node: DocNode, width: CGFloat) -> CGFloat {
        switch node.type {
        case "divider": 24
        case "image": min(width * 0.6, 420)
        case "table":
            BlockHeights.of(node.id, width: width)
                ?? CGFloat(max(TableGrid.rows(node).count, 1)) * scaled(40) + 2
        case "math_equation": MathBlock.height(node, width: width)
        case "diagram":
            max(BlockHeights.of(node.id, width: width) ?? min(max(width * 0.3, 160), 320), 44)
        case "video": BlockHeights.of(node.id, width: width) ?? min(width * 9 / 16, 420) + 44
        case "chart": scaled(300)
        case "card": BlockHeights.of(node.id, width: width) ?? scaled(160)
        case "map": BlockHeights.of(node.id, width: width) ?? MapBlockView.mapHeight + scaled(80)
        case "link_preview": scaled(LinkCardBlock.tall(node) ? 96 : 64)
        case "recording", "file": BlockHeights.of(node.id, width: width) ?? scaled(64)
        // An embed's frame fits its content, from 120 to 1200 points; its
        // stored height sizes it only until the content says how tall it is.
        case "embed":
            BlockHeights.of(node.id, width: width).map { min(max($0, 120), 1200) }
                ?? CGFloat(min(max(node.data?["height"]?.int ?? 320, 120), 1200))
        default: scaled(64)
        }
    }

    /// The block in the page's colors: a block's view hosted on its own
    /// reads them from the environment, which a hosting view does not
    /// pass on from the page.
    var body: some View {
        block.environment(\.palette, BlockLook.palette)
            // A link in a block, as in a table's cell or a card's sources,
            // opens as the page's own links do: another page's in the app.
            .environment(
                \.openURL,
                OpenURLAction { [weak editor] url in
                    guard let editor else { return .systemAction }
                    editor.open(url)
                    return .handled
                })
    }

    @ViewBuilder private var block: some View {
        switch node.type {
        case "divider":
            Divider().frame(maxHeight: .infinity)
        case "image":
            ImageBlock(node: node)
        case "video":
            VideoBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        case "chart":
            ChartBlock(node: node)
        case "math_equation":
            MathBlock(node: node)
        case "table":
            TableGrid(node: node, edit: edit, onHeight: onHeight)
        case "subpage":
            SubpageBlock(node: node, links: links)
        case "recording":
            RecordingBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        case "link_preview":
            LinkCardBlock(node: node)
        case "card":
            FactCardView(node: node, onHeight: onHeight)
        case "map":
            MapBlockView(node: node, onHeight: onHeight)
        case "file":
            FileBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        default:
            CardBlock(node: node)
        }
    }
}

struct LinkCardBlock: View {
    let node: DocNode
    @Environment(\.displayScale) private var scale
    @State private var picture: UIImage?

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
            await CardPictures.load(image, scale: scale) { picture = $0 }
        }
    }
}

struct SubpageBlock: View {
    let node: DocNode
    let links: PageLinks?

    private var pageID: String { node.data?["page_id"]?.string ?? "" }

    var body: some View {
        let palette = BlockLook.palette
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

struct ImageBlock: View {
    let node: DocNode
    @Environment(\.displayScale) private var scale
    @State private var picture: Picture?
    @State private var width: CGFloat = 0
    /// The picture's file and link that `picture` is of: one made again for
    /// a wider block shows the narrower one until it is ready.
    @State private var shownOf: [String] = []

    static var client: CoreClient?

    private enum Picture: Sendable {
        case bitmap(UIImage)
        case svg(SVG)
    }

    private struct Wanted: Hashable {
        let source: [String]
        let pixels: CGFloat
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
        .onGeometryChange(for: CGFloat.self) {
            $0.size.width
        } action: {
            width = $0
        }
        .task(id: Wanted(source: [fileID, link], pixels: Pictures.pixels(width * scale))) {
            let source = [fileID, link]
            if shownOf != source {
                picture = nil
                shownOf = source
            }
            // As wide as the block is laid out, once it is.
            let pixels = Pictures.pixels(width * scale)
            guard pixels > 0 else { return }
            var data: Data?
            if !fileID.isEmpty, let client = Self.client,
                let path = try? await client.localFile(fileID)
            {
                data = await Task.detached { FileManager.default.contents(atPath: path) }.value
            } else if link.hasPrefix("file://") {
                let path = String(link.dropFirst("file://".count))
                data = await Task.detached {
                    LocalFiles.resolve(path).flatMap { FileManager.default.contents(atPath: $0) }
                }.value
            } else {
                data = try? await Self.client?.picture(link)
            }
            guard let data, !data.isEmpty else { return }
            let made = await Task.detached { Self.picture(data, pixels: pixels) }.value
            guard !Task.isCancelled else { return }
            picture = made
        }
    }

    private var placeholder: some View {
        RoundedRectangle(cornerRadius: 8).fill(.quaternary)
    }

    private nonisolated static func picture(_ data: Data, pixels: CGFloat) -> Picture? {
        guard Pictures.fit(data) else { return nil }
        if let image = Pictures.bitmap(data, pixels: pixels) {
            return .bitmap(image)
        }
        return SVG(data: SVGPercents.resolved(data)).map { .svg($0) }
    }
}

enum Pictures {
    static func fit(_ data: Data) -> Bool {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            let found = CGImageSourceCopyPropertiesAtIndex(
                source, CGImageSourceGetPrimaryImageIndex(source), nil) as? [CFString: Any],
            let width = found[kCGImagePropertyPixelWidth] as? Double,
            let height = found[kCGImagePropertyPixelHeight] as? Double
        else { return true }
        return width * height <= Double(MobileMaxPixels)
    }

    /// The picture in data, decoded no bigger than `pixels` on its longer
    /// side, as it shows: a photo is never held at its full size, which can
    /// be tens of megabytes, to be drawn in a card. Nil for what is no
    /// picture, or one too big to read.
    static func bitmap(_ data: Data, pixels: CGFloat) -> UIImage? {
        guard fit(data),
            let source = CGImageSourceCreateWithData(
                data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary)
        else { return nil }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: max(pixels, 1),
        ]
        guard
            let image = CGImageSourceCreateThumbnailAtIndex(
                source, CGImageSourceGetPrimaryImageIndex(source), options as CFDictionary)
        else { return nil }
        return UIImage(cgImage: image)
    }

    /// The pixels a picture `points` wide on screen is decoded at: rounded
    /// up, so that a block a little wider or narrower does not decode it
    /// again.
    static func pixels(_ points: CGFloat) -> CGFloat {
        guard points.isFinite, points > 0 else { return 0 }
        return (points / 256).rounded(.up) * 256
    }
}

/// The pictures of cards and link cards, by their links: fetched, decoded
/// no bigger than a card shows them, and kept, so that a card made again,
/// as an edit to the page does, shows its picture at once.
@MainActor
enum CardPictures {
    /// The most points a card's picture is wide or tall on screen, with
    /// room to spare.
    static let points: CGFloat = 450

    private static let kept = NSCache<NSString, UIImage>()

    /// Shows the picture at link, or none: never another link's while it
    /// loads or once it fails, nor one asked for before the link changed.
    static func load(_ link: String, scale: CGFloat, show: (UIImage?) -> Void) async {
        guard !link.isEmpty else { return show(nil) }
        let pixels = Pictures.pixels(points * max(scale, 1))
        let key = "\(Int(pixels)) \(link)" as NSString
        if let picture = kept.object(forKey: key) { return show(picture) }
        show(nil)
        guard let data = try? await ImageBlock.client?.picture(link), !data.isEmpty,
            let made = await Task.detached(operation: {
                Pictures.bitmap(data, pixels: pixels)
            }).value,
            !Task.isCancelled
        else { return }
        kept.setObject(made, forKey: key)
        show(made)
    }
}

@MainActor
@Observable
final class VideoPlayback {
    static let shared = VideoPlayback()
    private(set) var playing: Set<String> = []
    @ObservationIgnored private var views: [String: Weak] = [:]
    @ObservationIgnored private var current: String?
    @ObservationIgnored private var watching: Task<Void, Never>?

    private final class Weak {
        weak var view: WKWebView?
        init(_ view: WKWebView) { self.view = view }
    }
    var start: [String: Int] = [:]
    var loads: [String: Int] = [:]
    var seeks: [String: Seek] = [:]

    struct Seek: Equatable {
        let id = UUID()
        let script: String
    }

    /// A player is a block's in one editor: see `EditorController.players`.
    static func key(_ block: String, in editor: EditorController?) -> String {
        editor?.player(block) ?? block
    }

    func play(_ id: String) {
        playing.insert(id)
        playAlone(id)
    }

    func seek(_ id: String, link: String, at: Int) {
        guard playing.contains(id) else {
            start[id] = at
            play(id)
            return
        }
        playAlone(id)
        let script = MobileVideoSeek(link, at)
        if script.isEmpty || views[id]?.view?.window == nil {
            start[id] = at
            loads[id, default: 0] += 1
        } else {
            seeks[id] = Seek(script: script)
        }
    }

    func stop(_ ids: Set<String>) {
        let heard = !playing.isDisjoint(with: ids)
        defer {
            if heard, playing.isEmpty { WebPage.clearPlayers() }
        }
        playing.subtract(ids)
        for id in ids {
            views[id]?.view?.pauseAllMediaPlayback(completionHandler: nil)
            start[id] = nil
            loads[id] = nil
            seeks[id] = nil
            views[id] = nil
        }
    }

    func playAlone(_ id: String) {
        current = id
        for (other, weak) in views where other != id {
            weak.view?.pauseAllMediaPlayback(completionHandler: nil)
        }
        Recorder.shared.pausePlaying()
        FilePlayback.shared.pause()
    }

    func pauseAll() {
        current = nil
        for weak in views.values {
            weak.view?.pauseAllMediaPlayback(completionHandler: nil)
        }
    }

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

struct VideoBlock: View {
    let node: DocNode
    var canEdit = false
    weak var editor: EditorController?
    var onHeight: (@MainActor (CGSize) -> Void)?
    @State private var width: CGFloat = 0
    @State private var made: Made?
    @ScaledMetric(relativeTo: .subheadline) private var control: CGFloat = 28

    private var playback: VideoPlayback { VideoPlayback.shared }
    private var key: String { VideoPlayback.key(node.id, in: editor) }
    private var playing: Bool { playback.playing.contains(key) }
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

    struct Line: Decodable, Sendable {
        let at: Int
        var time: String?
        var line: Int?
        var speaker: String?
        let parts: [Part]

        struct Part: Decodable, Sendable {
            let text: String
            let at: Int
        }
    }

    var body: some View {
        let start = playback.start[key] ?? -1
        let loads = playback.loads[key] ?? 0
        let player = made?.player
        let lines = Self.lines(node)
        VStack(alignment: .leading, spacing: 0) {
            Color.black
                .frame(maxWidth: .infinity)
                .frame(height: min(max(width, 1) * 9 / 16, 420))
                .overlay {
                    if playing, let made {
                        WebPage(
                            html: made.player.page + "<!-- \(made.loads) -->",
                            base: URL(string: made.player.origin), interactive: true,
                            script: playback.seeks[key], player: key)
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
                            playback.play(key)
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
                NotesPanel(lines: lines, blockID: node.id, pageID: editor?.pageID) { play($0) }
                    .padding(.top, 4)
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
            guard !Task.isCancelled else { return }
            made = player.map { Made(player: $0, loads: loads) }
        }
    }

    private func play(_ at: Int) {
        playback.seek(key, link: link, at: at)
    }

    static func lines(_ block: DocNode) -> [Line] {
        lines(notes: block.data?["notes"]?.string ?? "")
    }

    nonisolated static func lines(notes: String) -> [Line] {
        lines(json: MobileVideoNotes(notes))
    }

    nonisolated static func lines(json: String) -> [Line] {
        (try? JSONDecoder().decode([Line].self, from: Data(json.utf8))) ?? []
    }

    static func actions(_ block: DocNode, canEdit: Bool, editor: EditorController?)
        -> [BlockAction]
    {
        let playback = VideoPlayback.shared
        let link = block.data?["url"]?.string ?? ""
        let lines = Self.lines(block)
        var out: [BlockAction] = []
        let key = VideoPlayback.key(block.id, in: editor)
        if !playback.playing.contains(key) {
            out.append(BlockAction(title: L.recPlay) { playback.play(key) })
        }
        func offer(_ title: String, at: Int) {
            out.append(
                BlockAction(title: L.videoPlay(title: title)) {
                    playback.seek(key, link: link, at: at)
                })
        }
        for line in lines {
            let text = line.parts.map(\.text).joined()
            if let time = line.time, line.at >= 0 {
                offer(text.isEmpty ? time : time + " " + text, at: line.at)
            } else {
                let times = line.parts.filter { $0.at >= 0 }
                for p in times { offer(times.count == 1 ? text : p.text, at: p.at) }
            }
        }
        return out + menuItems(block, lines, canEdit: canEdit, editor: editor).joined()
    }

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
        let delete = BlockAction(title: L.commonDelete, symbol: "trash", destructive: true) {
            editor?.removeBlock(block.id)
        }
        return [copies, links, [delete]]
    }

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
        let labels = (d["labels"]?.array ?? []).map {
            $0.string ?? ($0.int.map { "\($0)" } ?? $0.double.map { "\($0)" } ?? "")
        }
        var points: [Point] = []
        for (s, series) in (d["series"]?.array ?? []).enumerated() {
            let name = Self.seriesName(series["name"]?.string ?? "", at: s)
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

    /// The name a chart's series shows by, as the core names it: its own,
    /// or for one with none, its number in the app's language, as Series 2.
    static func seriesName(_ name: String, at index: Int) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            ? L.chartSeriesNumbered(n: index + 1) : name
    }
}

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
        case "card": L.hostSlashCard
        case "map": L.hostSlashMap
        case "math_equation": L.hostSlashMath
        case "diagram": L.hostSlashDiagram
        case "divider": L.hostSlashDivider
        default: L.editorUnsupported(type: type)
        }
    }
}

struct WebPage: UIViewRepresentable {
    let html: String
    let base: URL?
    let interactive: Bool
    var script: VideoPlayback.Seek?
    var player: String?

    @MainActor static let store = WKWebsiteDataStore.nonPersistent()
    @MainActor private static let playerStore: WKWebsiteDataStore = {
        let id = UUID(uuidString: "6F2B1C3A-8D44-4E7B-9A51-2C7E0D3F1B64") ?? UUID()
        let store = WKWebsiteDataStore(forIdentifier: id)
        clear(store)
        return store
    }()

    private static func clear(_ store: WKWebsiteDataStore) {
        store.removeData(
            ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), modifiedSince: .distantPast
        ) {}
    }

    @MainActor static func clearPlayers() { clear(playerStore) }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = player == nil ? Self.store : Self.playerStore
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []
        config.applicationNameForUserAgent = "Version/26.0 Mobile/15E148 Safari/604.1"
        let view = WKWebView(frame: .zero, configuration: config)
        view.isOpaque = false
        view.backgroundColor = .clear
        view.scrollView.backgroundColor = .clear
        view.scrollView.isScrollEnabled = interactive
        view.navigationDelegate = context.coordinator
        context.coordinator.base = base
        context.coordinator.loaded = html
        context.coordinator.dark = context.environment.colorScheme == .dark
        context.coordinator.ran = script?.id
        if let player { VideoPlayback.shared.register(view, for: player) }
        view.loadHTMLString(
            themed(html, dark: context.environment.colorScheme == .dark), baseURL: base)
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {
        let dark = context.environment.colorScheme == .dark
        if context.coordinator.loaded != html || context.coordinator.dark != dark {
            context.coordinator.base = base
            context.coordinator.loaded = html
            context.coordinator.dark = dark
            view.loadHTMLString(themed(html, dark: dark), baseURL: base)
        }
        if let script, context.coordinator.ran != script.id {
            context.coordinator.ran = script.id
            view.evaluateJavaScript(script.script)
        }
    }

    private func themed(_ html: String, dark: Bool) -> String {
        guard dark else { return html }
        return html.replacingOccurrences(of: "<html>", with: "<html data-theme=\"dark\">")
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loaded = ""
        var dark = false
        var ran: UUID?

        var base: URL?

        // The page loads only itself: the player it holds goes where it likes, in its own frame,
        // but never takes the page away nor opens a window.
        func webView(
            _ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
            decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void
        ) {
            let ok =
                switch action.targetFrame?.isMainFrame {
                case false?: true
                case true?: action.request.url == (base ?? URL(string: "about:blank"))
                case nil: false
                }
            decisionHandler(ok ? .allow : .cancel)
        }
    }
}

struct TableGrid: View {
    let node: DocNode
    var edit: (@MainActor (DocNode) -> Void)?
    var onHeight: (@MainActor (CGSize) -> Void)?
    @State private var height: CGFloat = 0

    /// A table's cells, row by row, each its text's delta with its marks.
    typealias Cells = [[[JSONValue]]]

    static func rows(_ node: DocNode) -> Cells {
        read.value(of: node) { cells($0) } ?? []
    }
    @MainActor private static var read = NodeCache<Cells>(most: 256)

    nonisolated static func cells(_ node: DocNode) -> Cells {
        (try? coreValue(node, as: Cells.self, MobileTableCells)) ?? []
    }

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
                .padding(1)
                .onGeometryChange(for: CGFloat.self) {
                    $0.size.height
                } action: {
                    height = $0
                }
            }
            .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
            .defaultScrollAnchor(.topLeading, for: .alignment)
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
        let look = TableLook(node)
        return TableLayout(
            columns: columns, room: room, rowHeight: rowHeight, wrapTo: BlockCard.scaled(160)
        ) {
            ForEach(0..<rows.count * columns, id: \.self) { i in
                let (r, c) = (i / columns, i % columns)
                Text(c < rows[r].count ? MarkdownPreview.text(rows[r][c], palette: palette) : "")
                    .font(Fonts.pageText(size: BlockLook.textSize * 0.88))
                    .foregroundStyle(palette.text)
                    .tableCell(
                        look, row: r, column: c, of: columns, palette: palette,
                        minHeight: rowHeight)
            }
        }
        .tableOutline(look, palette: palette)
    }
}

extension View {
    /// A table cell's look, in a page or an answer alike: its column's
    /// alignment, its padding, none at the table's ends when it is drawn
    /// with lines, a bold header row, and the grid's header background and
    /// borders or, with lines, a hairline over each row after the first.
    func tableCell(
        _ look: TableLook, row: Int, column: Int, of columns: Int, palette: Palette,
        minHeight: CGFloat = 0
    ) -> some View {
        fontWeight(row == 0 ? .semibold : .regular)
            .multilineTextAlignment(look.textAlignment(column))
            .fixedSize(horizontal: false, vertical: true)
            .padding(.leading, look.lines && column == 0 ? 0 : 10)
            .padding(.trailing, look.lines && column == columns - 1 ? 0 : 10)
            .padding(.vertical, 8)
            .frame(minHeight: minHeight, alignment: look.alignment(column, vertical: .center))
            // As tall as its row, so that its lines are the row's.
            .frame(
                maxWidth: .infinity, maxHeight: .infinity,
                alignment: look.alignment(column, vertical: .top)
            )
            .background(row == 0 && !look.lines ? palette.panel : .clear)
            .border(look.lines ? .clear : palette.divider, width: 0.5)
            .overlay(alignment: .top) {
                if look.lines && row > 0 {
                    Rectangle().fill(palette.divider).frame(height: 0.5)
                }
            }
    }

    /// A table's outline: a grid's rounded border, or none with lines.
    func tableOutline(_ look: TableLook, palette: Palette) -> some View {
        clipShape(.rect(cornerRadius: look.lines ? 0 : 8))
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(look.lines ? .clear : palette.border, lineWidth: 1))
    }
}

/// How a table draws: as a grid, or with simple lines (a bold header and a
/// hairline under it and between rows, with no borders), and how each of
/// its columns aligns: "left", "center", "right", or "" as text does.
struct TableLook: Equatable {
    var lines = false
    var aligns: [String] = []

    init(lines: Bool = false, aligns: [String] = []) {
        self.lines = lines
        self.aligns = aligns
    }

    init(_ node: DocNode) {
        lines = node.data?["style"]?.string == "lines"
        aligns = Self.ending(node.data?["aligns"]?.array?.map { $0.string ?? "" } ?? [])
    }

    func align(_ column: Int) -> String {
        column >= 0 && column < aligns.count ? aligns[column] : ""
    }

    func textAlignment(_ column: Int) -> TextAlignment {
        switch align(column) {
        case "center": .center
        case "right": .trailing
        default: .leading
        }
    }

    func alignment(_ column: Int, vertical: VerticalAlignment) -> Alignment {
        switch align(column) {
        case "center": Alignment(horizontal: .center, vertical: vertical)
        case "right": Alignment(horizontal: .trailing, vertical: vertical)
        default: Alignment(horizontal: .leading, vertical: vertical)
        }
    }

    /// The look with column aligned so, its aligns as long as it needs.
    func aligning(_ column: Int, _ align: String) -> TableLook {
        var out = self
        while out.aligns.count <= column { out.aligns.append("") }
        out.aligns[column] = align
        out.aligns = Self.ending(out.aligns)
        return out
    }

    /// The look of the table with columns columns, as it keeps no
    /// alignment for a column taken off it.
    func keeping(columns: Int) -> TableLook {
        var out = self
        out.aligns = Self.ending(Array(aligns.prefix(max(columns, 0))))
        return out
    }

    /// Aligns that end at the last column aligned otherwise than as text
    /// does, as a table keeps them: a look put back as it was is the same.
    private static func ending(_ aligns: [String]) -> [String] {
        var out = aligns
        while out.last == "" { out.removeLast() }
        return out
    }
}

struct TableLayout: Layout {
    let columns: Int
    let room: CGFloat
    let rowHeight: CGFloat
    let wrapTo: CGFloat

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

    static func fit(_ ideal: [CGFloat], in room: CGFloat, wrapTo: CGFloat) -> [CGFloat] {
        let widths = ideal.map { min($0, max(room, least)) }
        let total = widths.reduce(0, +)
        guard room > 0, total > 0 else { return widths }
        if total <= room {
            return widths.map { $0 + (room - total) * $0 / total }
        }
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
    var hex: String {
        var r: CGFloat = 0
        var g: CGFloat = 0
        var b: CGFloat = 0
        var a: CGFloat = 0
        UIColor(self).getRed(&r, green: &g, blue: &b, alpha: &a)
        return String(format: "#%02x%02x%02x", Int(r * 255), Int(g * 255), Int(b * 255))
    }
}

enum LocalFiles {
    private static var folder: URL? {
        try? FileManager.default.url(
            for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil,
            create: false
        ).appending(path: "Leaflet", directoryHint: .isDirectory)
    }

    static func resolve(_ path: String, in given: URL? = nil) -> String? {
        guard let dir = given ?? folder else { return nil }
        var found = path
        if !FileManager.default.fileExists(atPath: path),
            let r = path.range(of: "/Leaflet/", options: .backwards)
        {
            found = dir.appending(path: String(path[r.upperBound...])).path
        }
        return MobileInFolder(dir.path, found) ? found : nil
    }
}

enum BlockHeights {
    private static let heights = Mutex<[String: [(width: CGFloat, height: CGFloat)]]>([:])
    private static let widths = 4

    static func of(_ id: String, width: CGFloat) -> CGFloat? {
        heights.withLock { $0[id]?.last { abs($0.width - width) < 2 }?.height }
    }

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

final class WebBlockView: WKWebView, WKNavigationDelegate {
    var onHeight: ((CGSize) -> Void)?
    private var pinned: NSKeyValueObservation?

    private static let measure = """
        (function () {
          function send() {
            webkit.messageHandlers.size.postMessage(Math.ceil(document.documentElement.getBoundingClientRect().height));
          }
          addEventListener("load", function () { send(); new ResizeObserver(send).observe(document.body); });
        })();
        """

    private static let embedSize = """
        addEventListener("message", function (e) {
          var z = e.data && e.data.leafletSize;
          if (z && typeof z[0] === "number" && Math.abs(z[0] - innerHeight) > 24)
            webkit.messageHandlers.size.postMessage(Math.min(z[0], 1200));
        });
        """

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
            config.mediaTypesRequiringUserActionForPlayback = []
        }
        super.init(frame: .zero, configuration: config)
        relay.view = self
        isOpaque = false
        backgroundColor = .clear
        scrollView.backgroundColor = .clear
        scrollView.isScrollEnabled = false
        scrollView.contentInsetAdjustmentBehavior = .never
        pinned = scrollView.observe(\.contentOffset, options: [.new]) { view, _ in
            MainActor.assumeIsolated {
                if view.contentOffset != .zero { view.contentOffset = .zero }
            }
        }
        navigationDelegate = self
        isUserInteractionEnabled = interactive
        let dark = BlockLook.palette.isDark
        loadHTMLString(
            dark
                ? html.replacingOccurrences(of: "<html>", with: "<html data-theme=\"dark\">")
                : html,
            baseURL: nil)
    }

    required init?(coder: NSCoder) { fatalError("not coded") }

    // The page and the frame it holds the block's own HTML in load from about:blank and
    // about:srcdoc; nothing the HTML does takes either anywhere else.
    func webView(
        _ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
        decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void
    ) {
        decisionHandler(action.request.url?.scheme == "about" ? .allow : .cancel)
    }

    @MainActor
    private final class SizeRelay: NSObject, WKScriptMessageHandler {
        weak var view: WebBlockView?
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
