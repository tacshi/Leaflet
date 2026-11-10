import LeafletCore
import SwiftUI
import UIKit

struct TimedRow<Content: View>: View {
    let time: String
    let widest: String
    var style = TimeStyle.caption
    let play: () -> Void
    @ViewBuilder let content: Content

    var body: some View {
        let palette = BlockLook.palette
        Button(action: play) {
            HStack(alignment: .firstTextBaseline, spacing: style.spacing) {
                ZStack(alignment: .leading) {
                    Text(verbatim: widest).hidden()
                    Text(verbatim: time).foregroundStyle(palette.accent)
                }
                .font(.system(style.font).monospacedDigit())
                .fixedSize()
                content
                Spacer(minLength: 0)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }
}

struct TimeStyle {
    let font: Font.TextStyle
    let uiFont: UIFont.TextStyle
    let spacing: CGFloat

    static let caption = TimeStyle(font: .caption, uiFont: .caption1, spacing: 12)
    static let notes = TimeStyle(font: .subheadline, uiFont: .subheadline, spacing: 10)
    static let lineInset: CGFloat = 6
    static let lineLift: CGFloat = 3

    func indent(_ widest: String) -> CGFloat {
        let font = UIFont.monospacedDigitSystemFont(
            ofSize: UIFont.preferredFont(forTextStyle: uiFont).pointSize, weight: .regular)
        return (widest as NSString).size(withAttributes: [.font: font]).width.rounded(.up) + spacing
    }
}

struct FoldButton: View {
    @Binding var open: Bool
    let count: Int
    let indent: CGFloat

    var body: some View {
        let palette = BlockLook.palette
        Button {
            open.toggle()
        } label: {
            HStack(spacing: 4) {
                Text(open ? L.recShowLess : L.recShowAll(count: count))
                Image(systemName: "chevron.down").imageScale(.small)
                    .rotationEffect(.degrees(open ? 180 : 0))
            }
            .font(.footnote.weight(.medium))
            .foregroundStyle(palette.accent)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .padding(.leading, indent)
    }
}

struct RollingLines<Row: View>: View {
    let count: Int
    let clock: LineClock
    let spacing: CGFloat
    @ViewBuilder let row: (Int) -> Row
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private static var before: Int { 1 }

    var body: some View {
        let top = max((clock.line ?? 0) - Self.before, 0)
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: spacing) {
                    ForEach(0..<count, id: \.self) { row($0).id($0) }
                }
                .padding(.vertical, TimeStyle.lineLift)
            }
            .scrollDisabled(true)
            .scrollIndicators(.hidden)
            .frame(height: BlockLook.textSize * 11)
            .mask {
                LinearGradient(
                    stops: [
                        .init(color: top > 0 ? .clear : .black, location: 0),
                        .init(color: .black, location: 0.1),
                        .init(color: .black, location: 0.9),
                        .init(color: .clear, location: 1),
                    ], startPoint: .top, endPoint: .bottom)
            }
            .onChange(of: top) {
                withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.35)) {
                    proxy.scrollTo(top, anchor: .top)
                }
            }
            .onAppear { proxy.scrollTo(top, anchor: .top) }
        }
    }
}

struct NotesPanel: View {
    enum Look: Equatable {
        case panel
        case card(speakers: Bool)

        var inCard: Bool { self != .panel }
        var time: TimeStyle { inCard ? .caption : .notes }
    }

    struct Speakers {
        let all: [String]
        let change: (Int, String) -> Void
        let add: (Int) -> Void
        let rename: (String, Int) -> Void
    }

    let lines: [VideoBlock.Line]
    let blockID: String
    let pageID: String?
    var speakers: Speakers?
    let play: (Int) -> Void
    var clock: LineClock?
    var follow: ((CGFloat) -> Void)?
    var look = Look.panel
    @State private var open = false

    var body: some View {
        let palette = BlockLook.palette
        let widest =
            lines.compactMap(\.time).max { $0.count < $1.count }
            .map { String($0.map { $0.isNumber ? "0" : $0 }) } ?? "0:00"
        let folds = MobileFoldsTranscript(lines.count)
        let shown = folds && !open ? Array(lines.prefix(Int(MobileFoldedLines))) : lines
        let spacing: CGFloat = look.inCard ? 8 : 4
        return VStack(alignment: .leading, spacing: spacing) {
            VStack(alignment: .leading, spacing: spacing) {
                if look.inCard, folds, !open, let clock {
                    RollingLines(count: lines.count, clock: clock, spacing: spacing) { index in
                        row(lines[index], index, widest: widest, clock: clock, follow: nil)
                    }
                } else {
                    ForEach(Array(shown.enumerated()), id: \.offset) { index, line in
                        row(line, index, widest: widest, clock: clock, follow: follow)
                    }
                }
            }
            .padding(.horizontal, look.inCard ? -TimeStyle.lineInset : 0)
            if folds {
                FoldButton(open: $open, count: lines.count, indent: look.time.indent(widest))
            }
        }
        .padding(.horizontal, look.inCard ? 0 : 12)
        .padding(.vertical, look.inCard ? 0 : 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(look.inCard ? .clear : palette.code, in: .rect(cornerRadius: 10))
    }

    private func row(
        _ line: VideoBlock.Line, _ index: Int, widest: String, clock: LineClock?,
        follow: ((CGFloat) -> Void)?
    ) -> some View {
        NoteRow(
            line: line, index: index, widest: widest, blockID: blockID, pageID: pageID,
            speakers: speakers, clock: clock, follow: follow, look: look, play: play)
    }
}

private struct NoteRow: View {
    let line: VideoBlock.Line
    let index: Int
    let widest: String
    let blockID: String
    let pageID: String?
    let speakers: NotesPanel.Speakers?
    let clock: LineClock?
    let follow: ((CGFloat) -> Void)?
    let look: NotesPanel.Look
    let play: (Int) -> Void

    var body: some View {
        let palette = BlockLook.palette
        let current = clock?.line == index
        Group {
            if let time = line.time, line.at >= 0 {
                TimedRow(time: time, widest: widest, style: look.time) {
                    play(line.at)
                } content: {
                    words(links: false)
                }
            } else {
                words(links: true)
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
        .font(
            look.inCard
                ? .system(size: BlockLook.textSize - 2) : Fonts.pageText(size: BlockLook.textSize)
        )
        .foregroundStyle(palette.text)
        .padding(.horizontal, look.inCard ? TimeStyle.lineInset : 0)
        .padding(.vertical, look.inCard ? TimeStyle.lineLift : 0)
        .background(current ? palette.accentSoft : .clear, in: .rect(cornerRadius: 6))
        .background {
            if current { LinePlace(follow: follow) }
        }
        .padding(.vertical, look.inCard ? -TimeStyle.lineLift : 0)
        .contextMenu { lineMenu(line) }
    }

    private func words(links: Bool) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            if look == .card(speakers: true), let speaker = line.speaker, !speaker.isEmpty {
                Text(speaker)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(BlockLook.palette.textMuted)
            }
            Text(text(line.parts, links: links))
                .multilineTextAlignment(.leading)
        }
    }

    private static let seekScheme = "leaflet-seek"

    private func text(_ parts: [VideoBlock.Line.Part], links: Bool) -> AttributedString {
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

    @ViewBuilder private func lineMenu(_ line: VideoBlock.Line) -> some View {
        let at = line.at >= 0 ? line.at : line.parts.first { $0.at >= 0 }?.at
        if let at, let pageID, !pageID.isEmpty {
            Button(L.videoCopyMoment(time: MobileClock(at)), systemImage: "link") {
                UIPasteboard.general.string = MobileMomentLink(pageID, blockID, at)
            }
        }
        if let speakers, !speakers.all.isEmpty {
            let place = line.line ?? 0
            Menu(L.cardChangeSpeaker, systemImage: "person.2") {
                ForEach(speakers.all, id: \.self) { name in
                    Button {
                        speakers.change(place, name)
                    } label: {
                        if name == line.speaker {
                            Label(name, systemImage: "checkmark")
                        } else {
                            Text(name)
                        }
                    }
                }
                Divider()
                Button(L.cardNewSpeaker, systemImage: "plus") { speakers.add(place) }
            }
            if let name = line.speaker {
                Button(L.cardRenameSpeaker(name: name), systemImage: "pencil") {
                    speakers.rename(name, place)
                }
            }
        }
    }
}

private struct LinePlace: View {
    let follow: ((CGFloat) -> Void)?

    var body: some View {
        Color.clear.onGeometryChange(for: CGFloat.self) {
            $0.frame(in: .named(LineClock.space)).minY
        } action: {
            follow?($0)
        }
    }
}

@MainActor
@Observable
final class LineClock {
    nonisolated static let followKey = "player.follow"
    nonisolated static let space = "file-block"

    private(set) var line: Int?

    func tick(_ sec: Double, starts: [Double]) {
        var now: Int?
        for (i, start) in starts.enumerated() where start >= 0 {
            if start > sec { break }
            now = i
        }
        if line != now { line = now }
    }

    func clear() {
        if line != nil { line = nil }
    }

    static var followAction: BlockAction {
        let on = PlayerSettings.shared.following
        return BlockAction(
            title: L.cardFollowAlong,
            symbol: on ? "checkmark" : "text.line.first.and.arrowtriangle.forward"
        ) {
            toggleFollow()
        }
    }

    static func toggleFollow() { PlayerSettings.shared.toggleFollowing() }
}
