import LeafletCore
import SwiftUI
import UIKit

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
    static func color(_ name: String) -> UIColor {
        UIColor(hex: MobileTagColor(name)) ?? .systemTeal
    }

    static func ink(_ name: String, text: Color) -> Color {
        Color(color(name).mixed(with: UIColor(text), by: 0.35))
    }
}

extension UIColor {
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
