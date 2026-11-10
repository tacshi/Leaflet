import SwiftUI
import UIKit
import UniformTypeIdentifiers

struct ICloudRows: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        let sync = app.iCloud
        let on = app.status?.iCloudOn ?? false
        let signedIn = app.status?.signedIn ?? false
        Toggle(
            isOn: Binding(
                get: { sync.turning ?? on },
                set: { value in
                    Task {
                        await sync.set(value)
                        await app.refreshStatus()
                    }
                })
        ) {
            Text(L.settingsICloud)
            Text(desc(on: on, signedIn: signedIn))
        }
        .disabled(signedIn || sync.turning != nil)
        ActionRow(label: LI.iCloudFolder, desc: sync.folderName ?? LI.iCloudNoFolder) {
            Button(LI.iCloudChooseFolder) { sync.choose() }
                .disabled(signedIn || sync.turning != nil)
        }
    }

    static func picker(_ app: AppModel) -> some View {
        FolderPicker { url in
            Task {
                await app.iCloud.picked(url, syncing: app.status?.iCloudOn ?? false)
                await app.refreshStatus()
            }
        }
        .ignoresSafeArea()
    }

    private func desc(on: Bool, signedIn: Bool) -> String {
        let sync = app.iCloud
        if signedIn { return L.settingsICloudSupabaseOn }
        if let failure = sync.failure { return failure }
        if sync.lost { return LI.iCloudFolderLost }
        if on { return L.settingsICloudOnDesc }
        return sync.folderName == nil
            ? L.settingsICloudDesc + "\n" + LI.iCloudChooseHint : L.settingsICloudDesc
    }
}

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
