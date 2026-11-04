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
