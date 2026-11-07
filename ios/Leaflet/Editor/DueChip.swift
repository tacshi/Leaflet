import LeafletCore
import SwiftUI
import UIKit

/// Due is a to-do's due date, as the core keeps it in the block's "due":
/// a day in the user's own time zone, or a day with its time of day
/// (doc.DueLayout and doc.DueTimeLayout).
struct Due: Equatable {
    var date: Date
    var timed: Bool

    private static func formatter(_ format: String) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.calendar = Calendar(identifier: .gregorian)
        // The user's time zone as it is when a date is read or written,
        // not as the app started in: they travel.
        f.timeZone = .autoupdatingCurrent
        f.dateFormat = format
        return f
    }
    private static let day = formatter("yyyy-MM-dd")
    private static let time = formatter("yyyy-MM-dd'T'HH:mm")

    /// init reads a block's due date, as doc.ParseDue: a day is at its
    /// start.
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

    /// value is the date as the block keeps it, as doc.Node.SetDue.
    var value: JSONValue { .string((timed ? Self.time : Self.day).string(from: date)) }

    /// Chip is what the date says at a time, as the core has it, the
    /// desktop's (mobile.Due): when it is, Today, Tomorrow, Yesterday, or
    /// the day, with its year when not this one's, and its time when timed;
    /// whether it has passed, its time when timed, or else its day; and
    /// whether it is today.
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

    /// label says when it is, from now.
    func label(now: Date = .now) -> String { chip(now)?.label ?? "" }

    /// overdue reports whether it has passed.
    func overdue(now: Date = .now) -> Bool { chip(now)?.overdue == true }

    func today(now: Date = .now) -> Bool { chip(now)?.today == true }

    /// widest is the longest its label grows to as the days go by: the
    /// room the to-do's text leaves for its chip.
    var widest: String {
        let other =
            timed
            ? L.editorDueAt(day: L.editorDueDate(due: date), due: date)
            : L.editorDueDate(due: date)
        let now = label()
        return DueChip.size(now).width >= DueChip.size(other).width ? now : other
    }
}

/// DueChip draws a to-do's due date after its text, as the desktop's due
/// chip, or Add date while the caret is in a to-do without one; a tap on
/// it opens DuePopover.
enum DueChip {
    static let height: CGFloat = 20
    /// gap is the room between the to-do's text and its chip.
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

    /// room is what a to-do's text leaves at the end of its lines for its
    /// date's chip.
    static func room(_ info: BlockInfo) -> CGFloat {
        guard let due = Due(info) else { return 0 }
        return size(due.widest).width + gap
    }

    /// offerRoom is the room for Add date, which a to-do whose text leaves
    /// none for it takes while it offers one.
    static var offerRoom: CGFloat { size(L.editorAddDate).width + gap }

    /// label is what the chip of a to-do says: its date, or Add date.
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
            // On the page's own ground, an offer shows by its edge.
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
