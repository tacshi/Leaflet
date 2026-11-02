import SwiftUI
import UIKit

/// TagChip shows a tag as the desktop does: a pill tinted in the tag's
/// color, its name in that color mixed toward the text's.
struct TagChip<Trailing: View>: View {
    let name: String
    @ViewBuilder var trailing: () -> Trailing
    @Environment(\.palette) private var palette

    var body: some View {
        let color = TagChip.color(name)
        HStack(spacing: 4) {
            Text(name).lineLimit(1)
            trailing()
        }
        .font(.footnote.weight(.semibold))
        .padding(.horizontal, 7)
        .padding(.vertical, 2)
        .background(Color(color.withAlphaComponent(0.16)), in: .capsule)
        .foregroundStyle(TagChip.ink(name, text: palette.text))
    }
}

extension TagChip where Trailing == EmptyView {
    init(name: String) { self.init(name: name) { EmptyView() } }
}

extension TagChip {
    /// color is a tag's color, as the desktop's tagColor chooses it: one of
    /// the text colors but gray and brown, by the FNV-1a hash of its name
    /// in any case, so a tag has the same one on every device.
    static func color(_ name: String) -> UIColor {
        let bright = EditorController.swatches.map(\.1).filter {
            $0 != "#92400e" && $0 != "#71717a"
        }
        var hash: UInt32 = 2_166_136_261
        for byte in name.lowercased().utf8 {
            hash ^= UInt32(byte)
            hash = hash &* 16_777_619
        }
        return UIColor(hex: bright[Int(hash % UInt32(bright.count))]) ?? .systemTeal
    }

    /// ink is the color of a tag's name: its color, darker or, on a dark
    /// theme, lighter, mixed toward the text's.
    static func ink(_ name: String, text: Color) -> Color {
        Color(color(name).mixed(with: UIColor(text), by: 0.35))
    }
}

extension UIColor {
    /// mixed is the color a fraction t of the way to other.
    func mixed(with other: UIColor, by t: CGFloat) -> UIColor {
        var (r1, g1, b1, a1): (CGFloat, CGFloat, CGFloat, CGFloat) = (0, 0, 0, 0)
        var (r2, g2, b2, a2): (CGFloat, CGFloat, CGFloat, CGFloat) = (0, 0, 0, 0)
        getRed(&r1, green: &g1, blue: &b1, alpha: &a1)
        other.getRed(&r2, green: &g2, blue: &b2, alpha: &a2)
        return UIColor(
            red: r1 + (r2 - r1) * t, green: g1 + (g2 - g1) * t, blue: b1 + (b2 - b1) * t,
            alpha: a1 + (a2 - a1) * t)
    }
}
