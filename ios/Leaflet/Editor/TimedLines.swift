import LeafletCore
import SwiftUI
import UIKit

// Lines after their times, as a recording's transcript and a video's
// notes show them, as the desktop draws them: a tap on a line plays its
// block from the line's time.

/// TimedRow is a line after its time: a tap on either plays from it. The
/// time is as wide as widest, in its style, for the lines to start
/// together, spacing before the line.
struct TimedRow<Content: View>: View {
    let time: String
    let widest: String
    /// style is the time's text style: a transcript's caption, or the
    /// larger one of a video's notes, which are in the page's text size.
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

/// TimeStyle is how a line's time shows: its text style, and the space
/// after it.
struct TimeStyle {
    let font: Font.TextStyle
    let uiFont: UIFont.TextStyle
    let spacing: CGFloat

    /// caption is a transcript's: small, beside its smaller text.
    static let caption = TimeStyle(font: .caption, uiFont: .caption1, spacing: 12)
    /// notes is a video's notes': beside the page's own text.
    static let notes = TimeStyle(font: .subheadline, uiFont: .subheadline, spacing: 10)
    /// lineInset and lineLift are how far the light on the line being said
    /// reaches past its text, to its sides and above and below it, into the
    /// gap between lines: the text stays where it was.
    static let lineInset: CGFloat = 6
    static let lineLift: CGFloat = 3

    /// indent is how far the lines' text starts: past the widest time and
    /// the space after it.
    func indent(_ widest: String) -> CGFloat {
        let font = UIFont.monospacedDigitSystemFont(
            ofSize: UIFont.preferredFont(forTextStyle: uiFont).pointSize, weight: .regular)
        return (widest as NSString).size(withAttributes: [.font: font]).width.rounded(.up) + spacing
    }
}

/// FoldButton shows a long transcript's or notes' other lines, or folds
/// them again, under their text, indent in, past their times.
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

/// RollingLines is the window of a long transcript, until it is opened: one
/// height, moving on through the lines by itself as they are said. Nothing
/// scrolls it by hand, so that a drag over it scrolls the page.
struct RollingLines<Row: View>: View {
    let count: Int
    let clock: LineClock
    let spacing: CGFloat
    /// row is a line, by its index.
    @ViewBuilder let row: (Int) -> Row
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// before are the lines kept above the one being said.
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

/// NotesPanel is a block's notes, a video's or a file's: each line after its
/// time, if it has one, which a tap on the line plays from; a time in a line
/// without one plays from there. They are in the page's font and size, in a
/// panel of their own, as a video's description is on its site, tinted as a
/// code block is; an audio file's are its card's, as a recording's lines.
struct NotesPanel: View {
    /// Look is how the notes show: in a panel, or in an audio file's card,
    /// with speakers saying who said each line over it.
    enum Look: Equatable {
        case panel
        case card(speakers: Bool)

        var inCard: Bool { self != .panel }
        var time: TimeStyle { inCard ? .caption : .notes }
    }

    /// Speakers are the ways to say who said a line, from its menu: the
    /// speakers the notes name, and what to do with a line's place.
    struct Speakers {
        let all: [String]
        let change: (Int, String) -> Void
        let add: (Int) -> Void
        let rename: (String, Int) -> Void
    }

    let lines: [VideoBlock.Line]
    let blockID: String
    /// pageID is the page's, for links to the lines' moments.
    let pageID: String?
    /// speakers says who can be named on a line, or nil where they can't.
    var speakers: Speakers?
    /// play plays the block from a time, in seconds.
    let play: (Int) -> Void
    /// clock says what is being said, for a block that plays here.
    var clock: LineClock?
    /// follow scrolls to the line being said, at y in the block.
    var follow: ((CGFloat) -> Void)?
    var look = Look.panel
    /// open shows long notes whole.
    @State private var open = false

    var body: some View {
        let palette = BlockLook.palette
        // The times' column is as wide as the longest of them, its digits
        // all as wide.
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

/// NoteRow is a line of NotesPanel.
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
                // Its times are links, which play from there.
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

    /// words is the line's text, under its speaker where they show over it.
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

    /// seekScheme is the scheme of the links a time in a line is, to play
    /// from: leaflet-seek://83.
    private static let seekScheme = "leaflet-seek"

    /// text is a line's text, its times in the accent, and, with links,
    /// links that play from each.
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

    /// lineMenu is a line's menu, from a long press: a link to the moment
    /// it is about, to put in another page or a chat.
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

/// LinePlace tells follow where the line it is behind is in its block.
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

/// LineClock is the line being said as a block's audio plays, by its index.
/// Rows watch it, so only the rows a change is for draw again.
@MainActor
@Observable
final class LineClock {
    /// followKey keeps whether transcripts follow the audio, and space is
    /// the block's own, where a line's place is.
    static let followKey = "player.follow"
    static let space = "file-block"

    private(set) var line: Int?

    /// tick goes to sec seconds in, among lines that start at starts; one
    /// without a time, below 0, goes with the line before it.
    func tick(_ sec: Double, starts: [Double]) {
        var now: Int?
        for (i, start) in starts.enumerated() where start >= 0 {
            if start > sec { break }
            now = i
        }
        if line != now { line = now }
    }

    /// clear says nothing is being said.
    func clear() {
        if line != nil { line = nil }
    }

    /// followAction turns following along on or off, from a block's menu.
    static var followAction: BlockAction {
        let on = PlayerSettings.shared.following
        return BlockAction(
            title: L.cardFollowAlong,
            symbol: on ? "checkmark" : "text.line.first.and.arrowtriangle.forward"
        ) {
            toggleFollow()
        }
    }

    /// toggleFollow changes whether transcripts follow the audio.
    static func toggleFollow() { PlayerSettings.shared.toggleFollowing() }
}
