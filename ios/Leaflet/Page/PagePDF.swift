import UIKit
import WebKit

@MainActor
enum PagePDF {
    static let loadWait: Duration = .seconds(20)
    static let margin: CGFloat = 0.6 * 72

    static func make(_ page: ExportedPDF) async throws -> Data {
        let file = URL(filePath: page.path)
        defer { try? FileManager.default.removeItem(at: file) }
        let paper = CGRect(x: 0, y: 0, width: page.width * 72, height: page.height * 72)
        let config = WKWebViewConfiguration()
        config.websiteDataStore = WebPage.store
        config.userContentController.addUserScript(
            WKUserScript(
                source: WebBlockView.noRTC, injectionTime: .atDocumentStart, forMainFrameOnly: false
            ))
        let view = WKWebView(frame: paper, configuration: config)
        view.alpha = 0
        view.isUserInteractionEnabled = false
        let windows = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
        (windows.first(where: \.isKeyWindow) ?? windows.first)?.insertSubview(view, at: 0)
        defer { view.removeFromSuperview() }
        let loading = Loading()
        view.navigationDelegate = loading
        view.loadFileURL(file, allowingReadAccessTo: file)
        if await !loading.wait(upTo: loadWait) {
            view.stopLoading()
        }
        _ = try? await view.callAsyncJavaScript(
            "await document.fonts.ready", contentWorld: .page)

        let renderer = UIPrintPageRenderer()
        renderer.addPrintFormatter(view.viewPrintFormatter(), startingAtPageAt: 0)
        renderer.setValue(paper, forKey: "paperRect")
        renderer.setValue(paper.insetBy(dx: margin, dy: margin), forKey: "printableRect")
        let pages = renderer.numberOfPages
        return UIGraphicsPDFRenderer(bounds: paper).pdfData { context in
            renderer.prepare(forDrawingPages: NSRange(location: 0, length: pages))
            for i in 0..<pages {
                context.beginPage()
                renderer.drawPage(at: i, in: context.pdfContextBounds)
            }
        }
    }

    @MainActor
    private final class Loading: NSObject, WKNavigationDelegate {
        private var waiting: CheckedContinuation<Void, Never>?
        private var done = false
        private var late = false

        func wait(upTo limit: Duration) async -> Bool {
            let timeout = Task { @MainActor [weak self] in
                try? await Task.sleep(for: limit)
                guard !Task.isCancelled, let self else { return }
                self.late = true
                self.finish()
            }
            if !done {
                await withCheckedContinuation { waiting = $0 }
            }
            timeout.cancel()
            return !late
        }

        private func finish() {
            done = true
            waiting?.resume()
            waiting = nil
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { finish() }

        func webView(
            _ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error
        ) { finish() }

        func webView(
            _ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!,
            withError error: any Error
        ) { finish() }

        func webView(
            _ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
            decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void
        ) {
            decisionHandler(action.navigationType == .other ? .allow : .cancel)
        }
    }
}
