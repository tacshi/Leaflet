import Foundation
import Synchronization

final class FolderWatcher: NSObject, NSFilePresenter, @unchecked Sendable {
    let presentedItemURL: URL?
    let presentedItemOperationQueue: OperationQueue

    private let queue = DispatchQueue(label: "app.capyvibe.leaflet.icloud-watch")
    private let blobs: String
    private let onChange: @Sendable () -> Void
    private var waiting = false
    private let stopped = Atomic(false)

    init(_ folder: URL, onChange: @escaping @Sendable () -> Void) {
        presentedItemURL = folder
        presentedItemOperationQueue = OperationQueue()
        presentedItemOperationQueue.maxConcurrentOperationCount = 1
        presentedItemOperationQueue.underlyingQueue = queue
        blobs = folder.appending(path: "blobs").resolvingSymlinksInPath().path(
            percentEncoded: false)
        self.onChange = onChange
        super.init()
    }

    func start() { NSFileCoordinator.addFilePresenter(self) }

    func stop() {
        NSFileCoordinator.removeFilePresenter(self)
        stopped.store(true, ordering: .relaxed)
    }

    func presentedItemDidChange() { changed(nil) }

    func presentedSubitemDidAppear(at url: URL) { changed(url) }

    func presentedSubitemDidChange(at url: URL) { changed(url) }

    func presentedSubitem(at oldURL: URL, didMoveTo newURL: URL) { changed(newURL) }

    private func changed(_ url: URL?) {
        if let url {
            let path = url.resolvingSymlinksInPath().path(percentEncoded: false)
            if path == blobs || path.hasPrefix(blobs + "/") { return }
        }
        guard !waiting, !stopped.load(ordering: .relaxed) else { return }
        waiting = true
        queue.asyncAfter(deadline: .now() + 2) { [self] in
            waiting = false
            if !stopped.load(ordering: .relaxed) { onChange() }
        }
    }
}
