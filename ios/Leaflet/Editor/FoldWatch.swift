import UIKit

@MainActor
final class FoldWatch {
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

    func resized() {
        if fold != nil { set(now) }
    }

    private func set(_ new: ClosedRange<CGFloat>?) {
        guard new != fold else { return }
        fold = new
        changed(new)
    }
}
