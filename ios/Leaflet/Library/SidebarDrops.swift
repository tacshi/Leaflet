import SwiftUI
import UIKit

/// Drops on the sidebar's rows. SwiftUI's dropDestination is never called for views inside a
/// List on iPadOS, so the list takes drops itself, through UIKit, and finds the row under the
/// finger from the frames the rows report.
@Observable @MainActor
final class SidebarDrops {
    /// What shows where a drop would land, in the window: a row tinted, or a line between rows.
    enum Mark: Equatable {
        case row(CGRect)
        case line(y: CGFloat, minX: CGFloat)
    }

    private(set) var mark: Mark?
    @ObservationIgnored fileprivate var frames: [String: CGRect] = [:]
    /// What is dragged, a page's ID or a folder's after FolderRow.dragPrefix, once it is read.
    @ObservationIgnored fileprivate var dragged: String?
    @ObservationIgnored weak var app: AppModel?

    static func folderKey(_ id: String) -> String { "folder:" + id }
    static func pageKey(_ id: String) -> String { "page:" + id }

    /// Where a drop lands.
    fileprivate enum Landing {
        case intoFolder(String)
        case besideFolder(String, before: Bool)
        case besidePage(PageRow, before: Bool)
        /// A folder dropped among pages: last among the folders of this folder, or of the top level.
        case level(String)
    }

    /// Half the space between two rows, whose content is shorter than the row.
    private static let half: CGFloat = 26
    /// How far from a folder row's middle a dragged folder still goes into it, not beside it.
    private static let inside: CGFloat = 9

    private func rowBox(_ frame: CGRect) -> CGRect {
        CGRect(x: frame.minX, y: frame.midY - 22, width: frame.width, height: 44)
    }

    /// Where dragged would land at point, in the window, and what marks it.
    private func landing(of dragged: String?, at point: CGPoint) -> (Landing, Mark)? {
        guard let lib = app?.library else { return nil }
        let folder = dragged.flatMap {
            $0.hasPrefix(FolderRow.dragPrefix)
                ? String($0.dropFirst(FolderRow.dragPrefix.count)) : nil
        }
        let nearest = frames.min { abs($0.value.midY - point.y) < abs($1.value.midY - point.y) }
        guard let (key, frame) = nearest, abs(frame.midY - point.y) <= Self.half else {
            let bottom = frames.values.map(\.maxY).max() ?? 0
            guard point.y > bottom, dragged != nil else { return nil }
            let minX = frames.values.map(\.minX).min() ?? 0
            return (.level(""), .line(y: bottom + 16, minX: minX))
        }
        let off = point.y - frame.midY
        if key.hasPrefix("folder:") {
            let target = String(key.dropFirst("folder:".count))
            guard let folder else { return (.intoFolder(target), .row(rowBox(frame))) }
            guard !lib.folder(target, isIn: folder) else { return nil }
            if abs(off) <= Self.inside { return (.intoFolder(target), .row(rowBox(frame))) }
            let y = frame.midY + (off < 0 ? -Self.half : Self.half)
            return (.besideFolder(target, before: off < 0), .line(y: y, minX: frame.minX))
        }
        guard let target = lib.page(String(key.dropFirst("page:".count))) else { return nil }
        if let folder {
            let top = lib.path(to: target).first ?? target
            let into = top.folderID.flatMap { lib.folder($0) }
            if let into, lib.folder(into.id, isIn: folder) { return nil }
            let first = into.map { lib.pages(in: $0) }?.first ?? lib.topPages.first
            let at = first.flatMap { frames[Self.pageKey($0.id)] } ?? frame
            return (.level(into?.id ?? ""), .line(y: at.midY - Self.half, minX: at.minX))
        }
        guard dragged != target.id else { return nil }
        let y = frame.midY + (off < 0 ? -Self.half : Self.half)
        return (.besidePage(target, before: off < 0), .line(y: y, minX: frame.minX))
    }

    fileprivate func over(_ point: CGPoint?) -> Bool {
        let next = point.flatMap { landing(of: dragged, at: $0)?.1 }
        if next != mark { mark = next }
        return next != nil
    }

    fileprivate func drop(_ dragged: String, at point: CGPoint) async {
        guard let app, let (landing, _) = landing(of: dragged, at: point) else { return }
        let lib = app.library
        let folder =
            dragged.hasPrefix(FolderRow.dragPrefix)
            ? String(dragged.dropFirst(FolderRow.dragPrefix.count)) : nil
        let page = folder == nil ? lib.page(dragged) : nil
        let movable = page.map { $0.canEdit && !$0.template && lib.own($0) } ?? false
        switch landing {
        case .intoFolder(let into):
            if let folder {
                await app.moveFolder(folder, into: into)
            } else if let page, movable {
                await app.move(page, parentID: "", folderID: into)
            }
        case .besideFolder(let target, let before):
            if let folder { await app.placeFolder(folder, beside: target, before: before) }
        case .besidePage(let target, let before):
            await app.place(dragged, beside: target, before: before)
        case .level(let into):
            if let folder {
                await app.moveFolder(folder, into: into)
            } else if let page, movable {
                await app.move(page, parentID: "", folderID: "")
            }
        }
    }
}

/// Makes a sidebar row a drop target, by reporting its frame.
struct SidebarDropRow: ViewModifier {
    let key: String
    @Environment(SidebarDrops.self) private var drops: SidebarDrops?

    func body(content: Content) -> some View {
        content
            .onGeometryChange(for: CGRect.self) {
                $0.frame(in: .global)
            } action: {
                drops?.frames[key] = $0
            }
            .onDisappear { drops?.frames[key] = nil }
    }
}

/// Draws, over the sidebar's list, where a drop would land.
struct SidebarDropMark: View {
    let drops: SidebarDrops

    var body: some View {
        GeometryReader { geo in
            let list = geo.frame(in: .global)
            switch drops.mark {
            case .row(let row):
                RoundedRectangle(cornerRadius: 10)
                    .fill(Color.accentColor.opacity(0.18))
                    .frame(width: max(list.width - 16, 0), height: row.height)
                    .position(x: list.width / 2, y: row.midY - list.minY)
            case .line(let y, let minX):
                let right = list.maxX - 16
                Capsule()
                    .fill(Color.accentColor)
                    .frame(width: max(right - minX, 0), height: 3)
                    .position(x: (minX + right) / 2 - list.minX, y: y - list.minY)
            case nil:
                EmptyView()
            }
        }
        .allowsHitTesting(false)
    }
}

/// Sits behind the sidebar's List and puts a drop interaction on the collection view under it,
/// in place of the List's own, which never lets a row's drop through.
struct SidebarDropHost: UIViewRepresentable {
    let drops: SidebarDrops

    func makeUIView(context: Context) -> HostView { HostView(drops: drops) }

    func updateUIView(_ view: HostView, context: Context) { view.attach() }

    final class HostView: UIView, UIDropInteractionDelegate {
        private let drops: SidebarDrops
        private lazy var interaction = UIDropInteraction(delegate: self)
        private weak var list: UICollectionView?
        private var tries = 0

        init(drops: SidebarDrops) {
            self.drops = drops
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        @available(*, unavailable)
        required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            tries = 0
            attach()
        }

        func attach() {
            guard window != nil else { return }
            if list == nil { list = findList() }
            guard let list else {
                if tries < 10 {
                    tries += 1
                    DispatchQueue.main.async { [weak self] in self?.attach() }
                }
                return
            }
            for other in list.interactions where other is UIDropInteraction && other !== interaction
            {
                list.removeInteraction(other)
            }
            if !list.interactions.contains(where: { $0 === interaction }) {
                list.addInteraction(interaction)
            }
        }

        /// The collection view the List draws with: the one, among this view's surroundings,
        /// that covers its middle.
        private func findList() -> UICollectionView? {
            let middle = convert(CGPoint(x: bounds.midX, y: bounds.midY), to: nil)
            var at = superview
            while let view = at {
                if let found = Self.collectionView(in: view, covering: middle) { return found }
                at = view.superview
            }
            return nil
        }

        private static func collectionView(in view: UIView, covering point: CGPoint)
            -> UICollectionView?
        {
            for sub in view.subviews {
                if let list = sub as? UICollectionView,
                    list.convert(list.bounds, to: nil).contains(point)
                {
                    return list
                }
                if let found = collectionView(in: sub, covering: point) { return found }
            }
            return nil
        }

        func dropInteraction(_ interaction: UIDropInteraction, canHandle session: UIDropSession)
            -> Bool
        {
            session.localDragSession != nil && session.canLoadObjects(ofClass: NSString.self)
        }

        func dropInteraction(
            _ interaction: UIDropInteraction, sessionDidEnter session: UIDropSession
        ) {
            drops.dragged = nil
            guard let provider = session.localDragSession?.items.first?.itemProvider else { return }
            _ = provider.loadObject(ofClass: NSString.self) { [drops] item, _ in
                let dragged = item as? String
                Task { @MainActor in drops.dragged = dragged }
            }
        }

        func dropInteraction(
            _ interaction: UIDropInteraction, sessionDidUpdate session: UIDropSession
        ) -> UIDropProposal {
            let lands = drops.over(window.map { session.location(in: $0) })
            return UIDropProposal(operation: lands ? .move : .cancel)
        }

        func dropInteraction(
            _ interaction: UIDropInteraction, sessionDidExit session: UIDropSession
        ) {
            _ = drops.over(nil)
        }

        func dropInteraction(_ interaction: UIDropInteraction, sessionDidEnd session: UIDropSession)
        {
            _ = drops.over(nil)
            drops.dragged = nil
        }

        func dropInteraction(_ interaction: UIDropInteraction, performDrop session: UIDropSession) {
            _ = drops.over(nil)
            guard let window else { return }
            let point = session.location(in: window)
            _ = session.loadObjects(ofClass: NSString.self) { [drops] items in
                guard let dragged = items.first as? String else { return }
                Task { @MainActor in await drops.drop(dragged, at: point) }
            }
        }
    }
}
