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
/// opens a page at one of its blocks, for a link to it.
struct PageLinks {
    let title: @MainActor (String) -> String?
    let open: @MainActor (String) -> Void
    var openAt: (@MainActor (String, String) -> Void)?
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
            return MobileMathPage(d["formula"]?.string ?? "")
        case "diagram":
            return MobileDiagramPage(d["source"]?.string ?? "", palette.surface.hex)
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
        case "table": CGFloat(max(TableGrid.rows(node).count, 1)) * scaled(40) + 2
        // Math and diagrams take the height their page measures, once it
        // has; until then, a guess.
        // At least the room of their Edit, an empty formula too.
        case "math_equation": max(BlockHeights.of(node.id, width: width) ?? 96, 44)
        case "diagram":
            max(BlockHeights.of(node.id, width: width) ?? min(max(width * 0.3, 160), 320), 44)
        case "video": min(width * 9 / 16, 420) + 44
        case "chart": scaled(300)
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
            VideoBlock(node: node)
        case "chart":
            ChartBlock(node: node)
        case "table":
            TableGrid(node: node, edit: edit)
        case "subpage":
            SubpageBlock(node: node, links: links)
        case "recording":
            RecordingBlock(node: node, canEdit: edit != nil, editor: editor, onHeight: onHeight)
        default:
            CardBlock(node: node)
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
/// at the caret, which can't reach the block's button.
@MainActor
@Observable
final class VideoPlayback {
    static let shared = VideoPlayback()
    var playing: Set<String> = []
}

/// VideoBlock shows a video's picture and title, and its player in place
/// once play is pressed.
struct VideoBlock: View {
    let node: DocNode
    private var playing: Bool { VideoPlayback.shared.playing.contains(node.id) }

    private struct Player: Decodable {
        let page: String
        let origin: String
        let thumbnail: String
    }

    var body: some View {
        let link = node.data?["url"]?.string ?? ""
        let player = try? JSONDecoder().decode(
            Player.self, from: Data(MobileVideoPlayer(link).utf8))
        VStack(alignment: .leading, spacing: 8) {
            // The black area takes the block's room; the picture fills it
            // without setting its size, so it never spills past the block.
            Color.black
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .overlay {
                    if playing, let player {
                        WebPage(
                            html: player.page, base: URL(string: player.origin), interactive: true)
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
                            VideoPlayback.shared.playing.insert(node.id)
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
            Text(node.data?["title"]?.string ?? link)
                .font(.subheadline.weight(.medium))
                .lineLimit(1)
        }
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
        case "link_preview": "link"
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
        let view = WKWebView(frame: .zero, configuration: config)
        view.isOpaque = false
        view.backgroundColor = .clear
        view.scrollView.backgroundColor = .clear
        view.scrollView.isScrollEnabled = interactive
        view.navigationDelegate = context.coordinator
        context.coordinator.loaded = html
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

/// TableGrid shows a table's cells, its first row as the header.
struct TableGrid: View {
    let node: DocNode
    /// edit opens the table's sheet from a tap, as a click edits a cell on
    /// the desktop; nil on a page that can't be changed.
    var edit: (@MainActor (DocNode) -> Void)?

    /// rows are the table's cells' text, by row, from its column-major
    /// cells (table/cell blocks with data.rowPosition and colPosition).
    static func rows(_ node: DocNode) -> [[String]] {
        var cells: [Int: [Int: String]] = [:]
        var cols = 0
        for cell in node.children ?? [] {
            let r = Int(cell.data?["rowPosition"]?.int ?? 0)
            let c = Int(cell.data?["colPosition"]?.int ?? 0)
            cells[r, default: [:]][c] = (cell.children ?? []).map(\.text).joined(separator: " ")
            cols = max(cols, c + 1)
        }
        return cells.keys.sorted().map { r in (0..<cols).map { cells[r]?[$0] ?? "" } }
    }

    var body: some View {
        if let edit {
            Button {
                edit(node)
            } label: {
                grid
            }
            .buttonStyle(.plain)
        } else {
            grid
        }
    }

    private var grid: some View {
        let rows = Self.rows(node)
        let palette = BlockLook.palette
        return Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
            ForEach(rows.indices, id: \.self) { r in
                GridRow {
                    ForEach(rows[r].indices, id: \.self) { c in
                        // In the page's font, a little smaller, as the
                        // desktop's cells.
                        Text(rows[r][c])
                            .font(Fonts.pageText(size: BlockLook.textSize * 0.88))
                            .fontWeight(r == 0 ? .semibold : .regular)
                            .foregroundStyle(palette.text)
                            .lineLimit(1)
                            .padding(.horizontal, 10)
                            .frame(
                                maxWidth: .infinity, minHeight: BlockCard.scaled(40),
                                alignment: .leading
                            )
                            .background(r == 0 ? palette.panel : .clear)
                            .border(palette.divider, width: 0.5)
                    }
                }
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border, lineWidth: 1))
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

    init(html: String, interactive: Bool) {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = WebPage.store
        config.allowsInlineMediaPlayback = true
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
    /// web view's content controller keeps it.
    private final class SizeRelay: NSObject, WKScriptMessageHandler {
        weak var view: WebBlockView?

        func userContentController(
            _ c: WKUserContentController, didReceive message: WKScriptMessage
        ) {
            guard let h = message.body as? Double, h > 0 else { return }
            MainActor.assumeIsolated {
                guard let view else { return }
                view.onHeight?(CGSize(width: view.bounds.width, height: CGFloat(h)))
            }
        }
    }
}
