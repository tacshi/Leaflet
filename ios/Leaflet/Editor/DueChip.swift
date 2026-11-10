import LeafletCore
import SwiftUI
import UIKit

struct Due: Equatable {
    var date: Date
    var timed: Bool

    private static func formatter(_ format: String) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.calendar = Calendar(identifier: .gregorian)
        f.timeZone = .autoupdatingCurrent
        f.dateFormat = format
        return f
    }
    private static let day = formatter("yyyy-MM-dd")
    private static let time = formatter("yyyy-MM-dd'T'HH:mm")

    init?(_ info: BlockInfo) {
        guard info.type == "todo_list", let s = info.data["due"]?.string else { return nil }
        if let d = Self.time.date(from: s) {
            (date, timed) = (d, true)
        } else if let d = Self.day.date(from: s) {
            (date, timed) = (d, false)
        } else {
            return nil
        }
    }

    init(date: Date, timed: Bool) {
        self.date = date
        self.timed = timed
    }

    var value: JSONValue { .string((timed ? Self.time : Self.day).string(from: date)) }

    struct Chip: Decodable {
        var label: String
        var overdue: Bool?
        var today: Bool?
    }

    func chip(_ now: Date = .now) -> Chip? {
        guard case .string(let s) = value else { return nil }
        let raw = MobileDue(L.language, s, Int64(now.timeIntervalSince1970 * 1000))
        return try? JSONDecoder().decode(Chip.self, from: Data(raw.utf8))
    }

    func label(now: Date = .now) -> String { chip(now)?.label ?? "" }

    func overdue(now: Date = .now) -> Bool { chip(now)?.overdue == true }

    func today(now: Date = .now) -> Bool { chip(now)?.today == true }

    var widest: String {
        let other =
            timed
            ? L.editorDueAt(day: L.editorDueDate(due: date), due: date)
            : L.editorDueDate(due: date)
        let now = label()
        return DueChip.size(now).width >= DueChip.size(other).width ? now : other
    }
}

enum DueChip {
    static let height: CGFloat = 20
    static let gap: CGFloat = 8
    private static let padding: CGFloat = 7
    private static let iconGap: CGFloat = 4

    private static var font: UIFont {
        UIFontMetrics(forTextStyle: .footnote).scaledFont(
            for: .systemFont(ofSize: 12, weight: .medium), maximumPointSize: 16)
    }
    private static var icon: UIImage? {
        UIImage(
            systemName: "calendar",
            withConfiguration: UIImage.SymbolConfiguration(
                pointSize: font.pointSize * 0.92, weight: .medium))
    }

    static func size(_ label: String) -> CGSize {
        let text = (label as NSString).size(withAttributes: [.font: font])
        let icon = icon?.size.width ?? 0
        return CGSize(
            width: ceil(padding * 2 + icon + iconGap + text.width),
            height: max(height, ceil(font.lineHeight + 4)))
    }

    static func room(_ info: BlockInfo) -> CGFloat {
        guard let due = Due(info) else { return 0 }
        return size(due.widest).width + gap
    }

    static var offerRoom: CGFloat { size(L.editorAddDate).width + gap }

    static func label(_ info: BlockInfo) -> String {
        Due(info)?.label() ?? L.editorAddDate
    }

    static func draw(_ info: BlockInfo, in rect: CGRect, palette: Palette) {
        let due = Due(info)
        let chip = due?.chip()
        var (fg, bg) = (palette.textMuted, palette.ground)
        if chip?.overdue == true {
            (fg, bg) = (palette.dangerText, palette.dangerBg)
        } else if chip?.today == true {
            (fg, bg) = (palette.accentText, palette.accentSoft)
        }
        let shape = UIBezierPath(roundedRect: rect, cornerRadius: 6)
        UIColor(bg).setFill()
        shape.fill()
        if due == nil {
            UIColor(palette.border).setStroke()
            let edge = UIBezierPath(roundedRect: rect.insetBy(dx: 0.5, dy: 0.5), cornerRadius: 6)
            edge.lineWidth = 1
            edge.stroke()
        }
        var x = rect.minX + padding
        if let img = icon?.withTintColor(UIColor(fg)) {
            img.draw(at: CGPoint(x: x, y: rect.midY - img.size.height / 2))
            x += img.size.width + iconGap
        }
        let text = NSAttributedString(
            string: chip?.label ?? L.editorAddDate,
            attributes: [.font: font, .foregroundColor: UIColor(fg)])
        text.draw(at: CGPoint(x: x, y: rect.midY - text.size().height / 2))
    }
}
