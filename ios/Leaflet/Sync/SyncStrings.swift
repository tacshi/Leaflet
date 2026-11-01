import Foundation

extension LI {
    static var iCloudFolder: String {
        String(
            localized: "Folder",
            bundle: L.bundle,
            comment: "Settings › Sync and storage: the folder in iCloud Drive pages sync through.")
    }
    static var iCloudNoFolder: String {
        String(
            localized: "None",
            bundle: L.bundle,
            comment: "Settings › Sync and storage: no folder in iCloud Drive is picked yet.")
    }
    static var iCloudChooseFolder: String {
        String(
            localized: "Choose Folder…",
            bundle: L.bundle,
            comment:
                "Settings › Sync and storage: pick the folder in iCloud Drive pages sync through.")
    }
    static var iCloudChooseHint: String {
        String(
            localized:
                "Choose the Leaflet folder your Mac syncs through, or iCloud Drive to start one.",
            bundle: L.bundle,
            comment: "Settings › Sync and storage: under iCloud sync, how to pick the folder.")
    }
    static var iCloudFolderLost: String {
        String(
            localized: "Leaflet can't open the folder it synced through. Choose it again.",
            bundle: L.bundle,
            comment:
                "Settings › Sync and storage: the folder picked before can't be opened; pick it again."
        )
    }
}
