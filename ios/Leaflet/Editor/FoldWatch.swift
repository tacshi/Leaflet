import UIKit

/// FoldWatch follows where a half folded display folds down a view, from
/// left to right in the view, room for the content on either side
/// included. A fold from side to side isn't one: text scrolls past it.
///
/// The system reserves the fold only once the display has stayed half
/// folded, and may yet open on, so one coming is said after a wait, for
/// the view not to lay out twice on its way: one moving or going, at once.
/// The system lets go of a fold just after the hinge last moves, with no
/// word of it, so the fold is read again as the same wait ends.
@MainActor
final class FoldWatch {
    /// reserved finds the fold the system has reserved in a view.
    var reserved: @MainActor (UIView) -> ClosedRange<CGFloat>? = { view in
        guard #available(iOS 27.1, *) else { return nil }
        return view.reservedRegions(kind: .division).first { $0.frame.height > $0.frame.width }
            .map { $0.frame.minX...$0.frame.maxX }
    }
    private unowned let view: UIView
    private let wait: Duration
    private let changed: (ClosedRange<CGFloat>?) -> Void
    private(set) var fold: ClosedRange<CGFloat>?
    private var waiting: Task<Void, Never>?

    init(view: UIView, wait: Duration, changed: @escaping (ClosedRange<CGFloat>?) -> Void) {
        self.view = view
        self.wait = wait
        self.changed = changed
    }

    private var now: ClosedRange<CGFloat>? { reserved(view) }

    /// hingeMoved looks at the fold again as the hinge moves.
    func hingeMoved() {
        guard now != nil else {
            waiting?.cancel()
            waiting = nil
            set(nil)
            return
        }
        if fold != nil {
            set(now)
            waiting?.cancel()
            waiting = nil
        }
        if waiting == nil {
            waiting = Task { @MainActor [weak self, wait] in
                try? await Task.sleep(for: wait)
                guard let self, !Task.isCancelled else { return }
                self.waiting = nil
                self.set(self.now)
            }
        }
    }

    /// resized looks at a fold seen again, which moves with the view: none
    /// starts here, for a display on its way open.
    func resized() {
        if fold != nil { set(now) }
    }

    private func set(_ new: ClosedRange<CGFloat>?) {
        guard new != fold else { return }
        fold = new
        changed(new)
    }
}
