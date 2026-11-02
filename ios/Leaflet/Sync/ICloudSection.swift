import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// ICloudSection is iCloud sync in Settings › Sync and storage: the
/// switch, and the folder in iCloud Drive pages sync through.
struct ICloudSection: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        let sync = app.iCloud
        let on = app.status?.iCloudOn ?? false
        let signedIn = app.status?.signedIn ?? false
        Section {
            Toggle(
                L.settingsICloud,
                isOn: Binding(
                    get: { sync.turning ?? on },
                    set: { value in
                        Task {
                            await sync.set(value)
                            await app.refreshStatus()
                        }
                    })
            )
            .disabled(signedIn || sync.turning != nil)
            LabeledContent(LI.iCloudFolder, value: sync.folderName ?? LI.iCloudNoFolder)
            Button(LI.iCloudChooseFolder) { sync.choose() }
                .disabled(signedIn || sync.turning != nil)
        } footer: {
            Text(footer(on: on, signedIn: signedIn))
        }
    }

    /// picker picks the folder, as Choose Folder asks; Settings shows it,
    /// over the whole form, as a row's sheet would go with the row.
    static func picker(_ app: AppModel) -> some View {
        FolderPicker { url in
            Task {
                await app.iCloud.picked(url, syncing: app.status?.iCloudOn ?? false)
                await app.refreshStatus()
            }
        }
        .ignoresSafeArea()
    }

    private func footer(on: Bool, signedIn: Bool) -> String {
        let sync = app.iCloud
        if signedIn { return L.settingsICloudSupabaseOn }
        if let failure = sync.failure { return failure }
        if sync.lost { return LI.iCloudFolderLost }
        if on { return L.settingsICloudOnDesc }
        // The desktop's description ends without a stop: the hint goes
        // on a line of its own, in any language.
        return sync.folderName == nil
            ? L.settingsICloudDesc + "\n" + LI.iCloudChooseHint : L.settingsICloudDesc
    }
}

/// FolderPicker has the user pick a folder in Files, for the app to open
/// it from then on.
private struct FolderPicker: UIViewControllerRepresentable {
    var picked: (URL) -> Void

    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forOpeningContentTypes: [.folder])
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ picker: UIDocumentPickerViewController, context: Context) {
        context.coordinator.picked = picked
    }

    func makeCoordinator() -> Coordinator { Coordinator(picked: picked) }

    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        var picked: (URL) -> Void

        init(picked: @escaping (URL) -> Void) { self.picked = picked }

        func documentPicker(
            _ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]
        ) {
            if let url = urls.first { picked(url) }
        }
    }
}
