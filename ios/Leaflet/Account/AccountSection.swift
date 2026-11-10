import AuthenticationServices
import SwiftUI

struct AccountSection: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var account: AccountInfo?
    @Environment(SettingsPrompt.self) private var prompt
    @State private var signingOut = false
    @State private var renewing = false
    @Environment(\.webAuthenticationSession) private var webSession
    @State private var failure: String?
    @State private var name = ""
    @FocusState private var naming: Bool

    var body: some View {
        Section {
            FieldRow(label: L.settingsName, desc: L.settingsNameDesc, maxWidth: 220) {
                TextField(L.settingsName, text: $name, prompt: Text(L.settingsYourName))
                    .textContentType(.name)
                    .submitLabel(.done)
                    .focused($naming)
                    .onSubmit(saveName)
            }
            .onChange(of: naming) { if !naming { saveName() } }
            .onDisappear(perform: saveName)
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
                if account.projectNeedsSignIn || renewing {
                    ActionRow(
                        label: L.settingsProjectSignIn, desc: L.settingsProjectSignInDesc
                    ) {
                        if renewing {
                            ProgressView()
                        } else {
                            Button(L.commonSignIn) { Task { await renewProject() } }
                        }
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
                .task(id: app.status?.signedIn) {
                    account = try? await app.client?.account()
                    if !naming { name = app.status?.displayName ?? "" }
                }
        } footer: {
            if let failure { Text(failure).foregroundStyle(palette.dangerText) }
        }
    }

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

    private func renewProject() async {
        guard let client = app.client else { return }
        renewing = true
        failure = nil
        defer { renewing = false }
        do {
            let begun = try client.beginSupabaseSignIn()
            let answer = try await webSession.authenticate(
                using: begun.url, callback: SignInView.callback, additionalHeaderFields: [:])
            try await client.finishProjectSignIn(begun, callback: answer)
            app.show(Toast(message: L.settingsProjectUpdated))
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
        } catch {
            failure = error.localizedDescription
        }
        account = try? await client.account()
    }

    private func signOut(anyway: Bool) async {
        guard let client = app.client else { return }
        signingOut = true
        failure = nil
        defer { signingOut = false }
        do {
            let left = try await client.signOut(anyway: anyway)
            if left > 0 {
                prompt.ask(
                    SettingsPrompt.Ask(
                        title: L.settingsSignOutAnyway,
                        message: L.settingsSignOutUnsynced(count: left),
                        action: L.commonSignOut,
                        confirm: { await signOut(anyway: true) }))
                return
            }
        } catch {
            failure = error.localizedDescription
        }
        await app.reload()
        account = try? await client.account()
    }
}

struct DevicesSection: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var devices: [Device]?
    @State private var failure: String?
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
                .task { await load() }
        }
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

struct DataSection: View {
    let signedIn: Bool
    let left: () async -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @Environment(SettingsPrompt.self) private var prompt
    @State private var exporting = false
    @State private var folder: URL?
    @State private var deleting = false
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
                        prompt.ask(
                            SettingsPrompt.Ask(
                                title: L.settingsDeleteAccountTitle,
                                message: L.settingsCannotUndo + " " + L.settingsTypeDelete,
                                action: L.settingsDeleteAccount, typed: "DELETE",
                                confirm: { await deleteAccount() }))
                    }
                    .disabled(deleting)
                }
            }
        } header: {
            Text(L.settingsYourData)
        } footer: {
            if let note { Text(note).foregroundStyle(failed ? palette.dangerText : .secondary) }
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
        let dir = FileManager.default.temporaryDirectory.appending(
            path: "Export-" + UUID().uuidString, directoryHint: .isDirectory)
        clear()
        folder = dir
        let exported: ExportedAll
        do {
            exported = try await client.exportAll(into: dir)
        } catch {
            say(error.localizedDescription, failed: true)
            clear()
            return
        }
        prompt.offer(
            SettingsPrompt.Move(
                file: exported.url,
                done: { result in
                    switch result {
                    case .success(let url):
                        let name = url.lastPathComponent
                        var said =
                            exported.missing > 0
                            ? L.settingsExportedMissing(count: exported.missing, name: name)
                            : L.settingsExported(name: name)
                        if exported.stale > 0 {
                            said += "\n" + L.settingsExportedStale(count: exported.stale)
                        }
                        say(said)
                    case .failure(let error):
                        say(L.settingsExportFailed(error: error.localizedDescription), failed: true)
                    }
                    clear()
                },
                cancelled: { clear() }))
    }

    private func clear() {
        if let folder { try? FileManager.default.removeItem(at: folder) }
        folder = nil
    }

    private func deleteAccount() async {
        guard let client = app.client else { return }
        deleting = true
        note = nil
        defer { deleting = false }
        do {
            try await client.deleteAccount()
            say(L.settingsAccountDeleted)
        } catch {
            say(error.localizedDescription, failed: true)
            await app.refreshStatus()
            if app.status?.signedIn ?? true { return }
        }
        await left()
    }
}
