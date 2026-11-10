import SwiftUI
import UIKit

struct SharedFile: Identifiable {
    let id = UUID()
    let url: URL

    private static var folder: URL {
        FileManager.default.temporaryDirectory.appending(
            path: "Exports", directoryHint: .isDirectory)
    }

    private static func place(_ name: String) throws -> URL {
        let dir = folder.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appending(path: name, directoryHint: .notDirectory)
    }

    init(name: String, of path: URL) throws {
        let url = try Self.place(name)
        do {
            try FileManager.default.linkItem(at: path, to: url)
        } catch {
            try FileManager.default.copyItem(at: path, to: url)
        }
        self.url = url
    }

    init(name: String, text: String) throws {
        url = try Self.place(name)
        try Data(text.utf8).write(to: url, options: .atomic)
    }

    func remove() {
        try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
    }

    static func sweep() {
        let files = FileManager.default
        try? files.removeItem(at: folder)
        let tmp = files.temporaryDirectory
        for name in (try? files.contentsOfDirectory(atPath: tmp.path)) ?? []
        where name.hasPrefix(exportPrefix) {
            try? files.removeItem(at: tmp.appending(path: name))
        }
    }

    /// The start of the name of a folder an export of everything is made in,
    /// which is swept with the rest when one was left behind.
    static let exportPrefix = "Export-"
}

struct ShareSheet: UIViewControllerRepresentable {
    let file: SharedFile

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: [file.url], applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
