import LeafletCore
import SwiftUI
import UserNotifications
import os

@main
struct LeafletApp: App {
    @State private var app = AppModel()

    init() {
        Logger.app.info("Leaflet core built with \(MobileVersion(), privacy: .public)")
        UNUserNotificationCenter.current().delegate = ReminderDelegate.shared
        SharedFile.sweep()
    }

    var body: some Scene {
        WindowGroup(id: "main") {
            RootView()
                .environment(app)
        }
        .commands { LeafletCommands(app: app) }
        WindowGroup(for: String.self) { $pageID in
            RootView(opening: pageID)
                .environment(app)
        }
    }
}

extension Logger {
    static let app = Logger(subsystem: Bundle.main.bundleIdentifier ?? "Leaflet", category: "app")
}
