import SwiftUI

/// AccountSection is Settings › Account, as the desktop's: the name shown
/// on the user's comments and edits, the account and the Supabase project
/// Leaflet syncs through, with Sign Out, or signing in with Supabase; then,
/// signed in, the devices signed in to the account.
struct AccountSection: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var account: AccountInfo?
    @State private var signingOut = false
    /// unsynced counts the changes that could not go up before signing
    /// out, to ask about.
    @State private var unsynced = 0
    @State private var failure: String?
    /// name is the name as typed, saved as Return or leaving the field
    /// does on the desktop.
    @State private var name = ""
    @FocusState private var naming: Bool

    var body: some View {
        Section {
            // Up to as wide as the desktop's, for its placeholder to show
            // whole where there is room.
            FieldRow(label: L.settingsName, desc: L.settingsNameDesc, maxWidth: 220) {
                TextField(L.settingsName, text: $name, prompt: Text(L.settingsYourName))
                    .textContentType(.name)
                    .submitLabel(.done)
                    .focused($naming)
                    .onSubmit(saveName)
            }
            .onChange(of: naming) { if !naming { saveName() } }
            .onDisappear(perform: saveName)
            // One row, as the desktop's: who is signed in, with Sign Out
            // beside it, or what signing in does, opening it.
            if let account, account.signedIn {
                ActionRow(
                    label: L.settingsSignedIn,
                    desc: L.settingsSignedInProject(email: account.email, project: account.project)
                ) {
                    if signingOut {
                        ProgressView()
                    } else {
                        Button(L.commonSignOut, role: .destructive) {
                            Task { await signOut(anyway: false) }
                        }
                        .buttonStyle(.borderless)
                    }
                }
            } else {
                NavigationLink {
                    SignInView().themedForm(palette)
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(L.settingsSync)
                        Text(L.settingsSyncDesc).font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                .disabled(app.client == nil)
            }
        } header: {
            Text(L.settingsSectionAccount)
        } footer: {
            if let failure { Text(failure).foregroundStyle(palette.dangerText) }
        }
        .task(id: app.status?.signedIn) {
            account = try? await app.client?.account()
            if !naming { name = app.status?.displayName ?? "" }
        }
        .alert(
            L.settingsSignOutAnyway,
            isPresented: Binding(get: { unsynced > 0 }, set: { if !$0 { unsynced = 0 } })
        ) {
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonSignOut, role: .destructive) { Task { await signOut(anyway: true) } }
        } message: {
            Text(L.settingsSignOutUnsynced(count: unsynced))
        }

        if let account, account.signedIn {
            DevicesSection()
        }
    }

    /// saveName keeps the name typed; the field then shows the account's
    /// name as saved, as the desktop's does.
    private func saveName() {
        let typed = name.trimmingCharacters(in: .whitespaces)
        guard let client = app.client, !typed.isEmpty, typed != app.status?.displayName else {
            name = app.status?.displayName ?? ""
            return
        }
        failure = nil
        Task {
            do {
                try await client.setDisplayName(typed)
            } catch {
                failure = error.localizedDescription
            }
            await app.refreshStatus()
            if !naming { name = app.status?.displayName ?? "" }
        }
    }

    private func signOut(anyway: Bool) async {
        guard let client = app.client else { return }
        signingOut = true
        failure = nil
        defer { signingOut = false }
        do {
            let left = try await client.signOut(anyway: anyway)
            if left > 0 {
                unsynced = left
                return
            }
        } catch {
            failure = error.localizedDescription
        }
        await app.reload()
        account = try? await client.account()
    }
}

/// DevicesSection is Account › Devices: the devices signed in to the
/// account, the one active last first, each but this one to sign out.
private struct DevicesSection: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var devices: [Device]?
    @State private var failure: String?
    /// signingOut is the device being signed out.
    @State private var signingOut: String?

    var body: some View {
        Section {
            if let failure {
                ActionRow(label: L.settingsDevicesFailed(error: failure)) {
                    Button(L.commonRetry) { Task { await load() } }
                }
            } else if let devices {
                ForEach(devices) { d in
                    ActionRow(label: d.name, desc: desc(d)) {
                        if !d.this && !d.revoked {
                            Button(L.commonSignOut) { Task { await signOut(d) } }
                                .disabled(signingOut != nil)
                        }
                    }
                }
            } else {
                Text(L.settingsDevicesLoading).foregroundStyle(.secondary)
            }
        } header: {
            Text(L.settingsDevices)
        }
        .task { await load() }
    }

    private func desc(_ d: Device) -> String {
        if d.revoked { return L.settingsDeviceSignedOut }
        if d.this { return L.settingsDeviceThis }
        return L.settingsDeviceLastActive(when: Byline.humanTime(d.lastActive))
    }

    private func load() async {
        guard let client = app.client else { return }
        do {
            devices = try await client.devices()
            failure = nil
        } catch {
            failure = error.localizedDescription
        }
    }

    private func signOut(_ d: Device) async {
        signingOut = d.id
        defer { signingOut = nil }
        do {
            try await app.client?.signOutDevice(d.id)
            await load()
        } catch {
            app.show(Toast(message: error.localizedDescription))
        }
    }
}

/// DataSection is Settings › Your data, last: every page exported as a zip
/// of Markdown files, to save where the user picks, and, signed in,
/// deleting the account once DELETE is typed, as on the desktop.
struct DataSection: View {
    let signedIn: Bool
    /// left runs once the account is deleted and this device signed out.
    let left: () async -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var exporting = false
    @State private var exported: ExportedAll?
    @State private var deleteShown = false
    @State private var confirm = ""
    @State private var deleting = false
    /// note says how the export or deleting went; failed, in the danger
    /// color.
    @State private var note: String?
    @State private var failed = false

    var body: some View {
        Section {
            ActionRow(label: L.settingsExportAll, desc: L.settingsExportAllDesc) {
                Button(L.settingsExportButton) { Task { await export() } }
                    .disabled(exporting || app.client == nil)
            }
            if signedIn {
                ActionRow(label: L.settingsDeleteAccount, desc: L.settingsDeleteAccountDesc) {
                    Button(L.settingsDeleteButton, role: .destructive) {
                        confirm = ""
                        deleteShown = true
                    }
                    .disabled(deleting)
                }
            }
        } header: {
            Text(L.settingsYourData)
        } footer: {
            if let note { Text(note).foregroundStyle(failed ? palette.dangerText : .secondary) }
        }
        .fileMover(
            isPresented: Binding(get: { exported != nil }, set: { if !$0 { exported = nil } }),
            file: exported?.url
        ) { result in
            let done = exported
            switch result {
            case .success(let url):
                let name = url.lastPathComponent
                let missing = done?.missing ?? 0
                say(
                    missing > 0
                        ? L.settingsExportedMissing(count: missing, name: name)
                        : L.settingsExported(name: name))
            case .failure(let error):
                say(L.settingsExportFailed(error: error.localizedDescription), failed: true)
            }
        } onCancellation: {
            // The zip the user did not keep goes.
            if let url = exported?.url { try? FileManager.default.removeItem(at: url) }
        }
        .alert(L.settingsDeleteAccountTitle, isPresented: $deleteShown) {
            TextField("DELETE", text: $confirm)
                .textInputAutocapitalization(.characters)
                .autocorrectionDisabled()
            Button(L.commonCancel, role: .cancel) {}
            Button(L.settingsDeleteAccount, role: .destructive) { Task { await deleteAccount() } }
                .disabled(confirm != "DELETE")
        } message: {
            Text(L.settingsCannotUndo + " " + L.settingsTypeDelete)
        }
    }

    private func say(_ text: String, failed: Bool = false) {
        note = text
        self.failed = failed
    }

    private func export() async {
        guard let client = app.client else { return }
        exporting = true
        note = nil
        defer { exporting = false }
        // A folder of its own each time, which the move then empties.
        let dir = FileManager.default.temporaryDirectory.appending(
            path: "Export-" + UUID().uuidString, directoryHint: .isDirectory)
        do {
            exported = try await client.exportAll(into: dir)
        } catch {
            say(error.localizedDescription, failed: true)
        }
    }

    private func deleteAccount() async {
        guard confirm == "DELETE", let client = app.client else { return }
        deleting = true
        note = nil
        defer { deleting = false }
        do {
            try await client.deleteAccount()
            say(L.settingsAccountDeleted)
        } catch {
            say(error.localizedDescription, failed: true)
        }
        await left()
    }
}
