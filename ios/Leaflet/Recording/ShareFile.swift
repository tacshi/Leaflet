import SwiftUI
import UIKit

/// SharedFile is a file the share sheet offers, in a folder of its own so it
/// keeps its name, which goes once the sheet closes.
struct SharedFile: Identifiable {
    let id = UUID()
    let url: URL

    /// folder holds what is being shared. What a quit left there goes at the
    /// next launch.
    private static var folder: URL {
        FileManager.default.temporaryDirectory.appending(
            path: "Exports", directoryHint: .isDirectory)
    }

    private static func place(_ name: String) throws -> URL {
        let dir = folder.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appending(path: name, directoryHint: .notDirectory)
    }

    /// init shares the file at path as name: a link to it where the system
    /// allows, as a long recording's audio is hundreds of megabytes.
    init(name: String, of path: URL) throws {
        let url = try Self.place(name)
        do {
            try FileManager.default.linkItem(at: path, to: url)
        } catch {
            try FileManager.default.copyItem(at: path, to: url)
        }
        self.url = url
    }

    /// init shares text as a file named name.
    init(name: String, text: String) throws {
        url = try Self.place(name)
        try Data(text.utf8).write(to: url, options: .atomic)
    }

    /// remove takes the file away with its folder.
    func remove() {
        try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
    }

    /// sweep takes away what an earlier run left.
    static func sweep() {
        try? FileManager.default.removeItem(at: folder)
    }
}

/// ShareSheet is the system's share sheet for a file.
struct ShareSheet: UIViewControllerRepresentable {
    let file: SharedFile

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: [file.url], applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
