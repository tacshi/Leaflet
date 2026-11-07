import LeafletCore
import SwiftUI
import UserNotifications
import os

/// LeafletApp is the iPhone and iPad app: a window of the library and a
/// page, and on iPad windows of their own for pages.
@main
struct LeafletApp: App {
    @State private var app = AppModel()

    init() {
        Logger.app.info("Leaflet core built with \(MobileVersion(), privacy: .public)")
        // Before the app finishes launching, for a tap on a reminder that
        // launches it.
        UNUserNotificationCenter.current().delegate = ReminderDelegate.shared
        SharedFile.sweep()
    }

    var body: some Scene {
        WindowGroup(id: "main") {
            RootView()
                .environment(app)
        }
        .commands { LeafletCommands(app: app) }
        // A page opened in a window of its own.
        WindowGroup(for: String.self) { $pageID in
            RootView(opening: pageID)
                .environment(app)
        }
    }
}

extension Logger {
    /// app is where the app logs what it does, under its bundle identifier.
    static let app = Logger(subsystem: Bundle.main.bundleIdentifier ?? "Leaflet", category: "app")
}
