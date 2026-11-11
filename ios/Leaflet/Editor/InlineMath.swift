import LeafletCore
import SwiftUI
import UIKit

/// A formula typeset for a line of text: its picture, and where it stands.
final class MathPicture: NSObject {
    let image: UIImage
    let width: CGFloat
    let ascent: CGFloat
    let descent: CGFloat
    /// Where the formula's left edge on its baseline is in the picture.
    let origin: CGPoint

    init(image: UIImage, width: CGFloat, ascent: CGFloat, descent: CGFloat, origin: CGPoint) {
        self.image = image
        self.width = width
        self.ascent = ascent
        self.descent = descent
        self.origin = origin
    }

    /// Draws the formula with its left edge at `x` on the baseline at `y`.
    func draw(x: CGFloat, baseline y: CGFloat) {
        image.draw(at: CGPoint(x: x - origin.x, y: y - origin.y))
    }
}

@MainActor
enum MathPictures {
    private struct Key: Hashable {
        let tex: String
        let size: CGFloat
        let ink: UInt32
        let scale: CGFloat
        /// The width a formula on lines of its own is broken to fit, or nil
        /// for one in a line of text.
        var width: CGFloat?
    }

    private struct Made: Decodable {
        let png: String
        let width: CGFloat
        let ascent: CGFloat
        let descent: CGFloat
        let left: CGFloat
        let top: CGFloat
    }

    private static var kept: [Key: MathPicture?] = [:]

    /// The picture of a formula in text of a size and a color, or nil for
    /// what is no formula yet, as one half typed.
    static func picture(_ tex: String, size: CGFloat, ink: UIColor, scale: CGFloat)
        -> MathPicture?
    {
        picture(tex, size: size, ink: ink, scale: scale, width: nil)
    }

    /// The picture of a formula block's formula, on lines of its own no
    /// wider than `width` where it has places to break.
    static func display(_ tex: String, size: CGFloat, width: CGFloat, ink: UIColor)
        -> MathPicture?
    {
        picture(
            tex, size: size, ink: ink, scale: max(UITraitCollection.current.displayScale, 2),
            width: width.rounded(.down))
    }

    private static func picture(
        _ tex: String, size: CGFloat, ink: UIColor, scale: CGFloat, width: CGFloat?
    ) -> MathPicture? {
        var (r, g, b, a): (CGFloat, CGFloat, CGFloat, CGFloat) = (0, 0, 0, 1)
        ink.getRed(&r, green: &g, blue: &b, alpha: &a)
        let part = { (v: CGFloat) in Int((min(max(v, 0), 1) * 255).rounded()) }
        let rgba = (part(r), part(g), part(b), part(a))
        var packed: UInt32 = 0
        for v in [rgba.0, rgba.1, rgba.2, rgba.3] { packed = packed << 8 | UInt32(v) }
        let key = Key(tex: tex, size: size, ink: packed, scale: scale, width: width)
        if let known = kept[key] { return known }
        if kept.count > 400 { kept.removeAll() }
        let made = make(tex, size: size, scale: scale, rgba, width: width)
        kept[key] = made
        return made
    }

    private static func make(
        _ tex: String, size: CGFloat, scale: CGFloat, _ ink: (Int, Int, Int, Int), width: CGFloat?
    ) -> MathPicture? {
        let raw =
            if let width {
                MobileDisplayMath(
                    tex, Double(size), Double(width), Double(scale), ink.0, ink.1, ink.2, ink.3)
            } else {
                MobileInlineMath(tex, Double(size), Double(scale), ink.0, ink.1, ink.2, ink.3)
            }
        guard !raw.isEmpty,
            let made = try? JSONDecoder().decode(Made.self, from: Data(raw.utf8)),
            let data = Data(base64Encoded: made.png),
            let image = UIImage(data: data, scale: scale)
        else { return nil }
        return MathPicture(
            image: image, width: made.width, ascent: made.ascent, descent: made.descent,
            origin: CGPoint(x: made.left, y: made.top))
    }
}

extension NSAttributedString.Key {
    /// On the first character of a typeset formula: its `MathPicture`,
    /// which the line draws in place of the source.
    static let leafletMathPicture = NSAttributedString.Key("leaflet.mathPicture")
}

extension EditorController {
    /// Shows the source of the formula the caret or the selection is at, and
    /// typesets the one it left.
    func mathFollowsSelection() {
        let now: NSRange? =
            textView.isFirstResponder && textView.isEditable ? textView.selectedRange : nil
        let was = mathOpen
        guard now != was else { return }
        mathOpen = now
        style.mathOpen = now
        let touched = [was, now].compactMap { $0 }
        DispatchQueue.main.async { [weak self] in
            guard let self, !self.session.updating, !self.session.composing else { return }
            self.restyleMath(around: touched)
        }
    }

    private func restyleMath(around ranges: [NSRange]) {
        let storage = text.storage
        let ns = storage.string as NSString
        var done: [NSRange] = []
        for range in ranges where range.location <= ns.length {
            let para = ns.paragraphRange(
                for: NSRange(
                    location: range.location,
                    length: min(range.length, ns.length - range.location)))
            guard !done.contains(para), para.length > 0 else { continue }
            done.append(para)
            var math = false
            storage.enumerateAttribute(.leafletMath, in: para) { value, _, stop in
                if value as? Bool == true {
                    math = true
                    stop.pointee = true
                }
            }
            guard math else { continue }
            storage.beginEditing()
            style.apply(to: storage, in: para)
            storage.endEditing()
        }
    }
}

/// A formula block: its formula typeset on lines of its own, or its source
/// where it is no formula.
struct MathBlock: View {
    let node: DocNode
    static let padding: CGFloat = 12

    private static func source(_ node: DocNode) -> String {
        (node.data?[MobileSourceKey(node.type)]?.string ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    @MainActor
    static func picture(_ node: DocNode, width: CGFloat) -> MathPicture? {
        MathPictures.display(
            source(node), size: BlockLook.textSize, width: max(width - 2 * padding, 40),
            ink: UIColor(BlockLook.palette.text))
    }

    @MainActor
    static func height(_ node: DocNode, width: CGFloat) -> CGFloat {
        guard let p = picture(node, width: width) else { return BlockCard.scaled(64) }
        return max(p.ascent + p.descent + 2 * padding, 44)
    }

    var body: some View {
        let palette = BlockLook.palette
        let tex = Self.source(node)
        GeometryReader { geo in
            Group {
                if tex.isEmpty {
                    Label(L.renderedWriteFormula, systemImage: "function")
                        .font(.subheadline).foregroundStyle(palette.textMuted)
                } else if let p = Self.picture(node, width: geo.size.width) {
                    Image(uiImage: p.image).accessibilityLabel(tex)
                } else {
                    Text(tex).font(.footnote.monospaced()).foregroundStyle(palette.danger)
                        .lineLimit(3)
                }
            }
            .frame(width: geo.size.width, height: geo.size.height)
        }
        .background(palette.surface, in: .rect(cornerRadius: 10))
    }
}
