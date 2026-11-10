import AuthenticationServices
import SwiftUI

struct ProvidersSection: View {
    @Binding var editing: ProviderForm.Draft?
    let version: Int
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var providers: [AgentProvider] = []
    @State private var presets: [ProviderPreset] = []
    @Environment(SettingsPrompt.self) private var prompt
    @State private var failure: String?

    var body: some View {
        @Bindable var app = app
        Section {
            if providers.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Text(L.settingsNoProviders)
                    Text(L.settingsNoProvidersDesc).font(.caption).foregroundStyle(.secondary)
                }
            }
            ForEach(providers) { p in
                Button {
                    editing = ProviderForm.Draft(
                        provider: p, preset: presets.first { $0.id == p.preset })
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("\(p.name) · \(p.defaultModel)").foregroundStyle(.primary)
                        Text(Self.place(of: p)).font(.caption).foregroundStyle(.secondary)
                    }
                }
                .swipeActions {
                    Button(L.commonRemove, role: .destructive) { askRemove(p) }
                        .tint(.red)
                }
            }
            if !providers.isEmpty { defaults }
        } header: {
            HeaderWithAction(title: L.settingsSectionProviders) { addMenu }
                .task(id: version) { await load() }
                .task {
                    for await _ in app.changes(of: ["model_providers", "session", "*"]) {
                        await load()
                    }
                }
        } footer: {
            if let failure {
                Text(failure).foregroundStyle(palette.dangerText)
            } else {
                Text(L.settingsProvidersDesc)
            }
        }
    }

    private var addMenu: some View {
        Menu {
            ForEach(groups, id: \.self) { group in
                Section {
                    ForEach(addable.filter { $0.group == group }) { preset in
                        Button(
                            preset.auth == "account"
                                ? L.settingsSignInWith(name: preset.name) : preset.name
                        ) {
                            editing = ProviderForm.Draft(preset: preset)
                        }
                    }
                }
            }
        } label: {
            Label(L.settingsAddProvider, systemImage: "plus")
        }
    }

    @ViewBuilder
    private var defaults: some View {
        @Bindable var app = app
        Toggle(isOn: $app.agentsReadOtherPages) {
            Text(L.settingsReadOtherPages)
            Text(L.settingsReadOtherPagesDesc)
        }
        .onChange(of: app.agentsReadOtherPages) { _, on in
            app.save(["agents_read_other_pages": on])
        }
        Toggle(isOn: $app.agentsUseWeb) {
            Text(L.settingsUseWeb)
            Text(L.settingsUseWebDesc)
        }
        .onChange(of: app.agentsUseWeb) { _, on in
            app.save(["agents_use_web": on])
        }
    }

    static func place(of p: AgentProvider) -> String {
        var place = L.settingsProviderCloud
        if p.isAccount {
            place =
                (p.email ?? "").isEmpty
                ? L.settingsProviderPlan : L.settingsProviderPlanEmail(email: p.email ?? "")
        } else if p.isLocal {
            place = L.settingsProviderLocal(url: p.baseURL)
        } else if !p.hasKey {
            place = L.settingsProviderNeedsKey
        }
        if p.supportsTools == false, !p.isAccount {
            place = L.settingsProviderChatOnly(where: place)
        }
        return place
    }

    static func removeMessage(for p: AgentProvider) -> String {
        p.isAccount ? L.settingsRemoveProviderAccount : L.settingsRemoveProviderLocal
    }

    private var addable: [ProviderPreset] {
        let signedIn = providers.contains(where: \.isAccount)
        return presets.filter { $0.auth != "account" || !signedIn }
    }

    private var groups: [String] {
        var seen: [String] = []
        for p in addable where !seen.contains(p.group) { seen.append(p.group) }
        return seen
    }

    private func load() async {
        guard let client = app.client else { return }
        providers = (try? await client.providers()) ?? []
        if presets.isEmpty { presets = (try? await client.presets()) ?? [] }
    }

    private func askRemove(_ p: AgentProvider) {
        prompt.ask(
            SettingsPrompt.Ask(
                title: L.settingsRemoveProviderTitle(name: p.name),
                message: Self.removeMessage(for: p), action: L.commonRemove,
                confirm: { await remove(p) }))
    }

    private func remove(_ p: AgentProvider) async {
        failure = nil
        do {
            try await app.client?.removeProvider(p.id)
        } catch {
            failure = error.localizedDescription
        }
        await load()
    }
}

extension AppModel {
    func changes(of tables: Set<String>) -> AsyncStream<Void> {
        AsyncStream(bufferingPolicy: .bufferingNewest(1)) { changed in
            let token = listen { event in
                if case .changed(let table, _) = event, tables.contains(table) {
                    changed.yield()
                }
            }
            changed.onTermination = { [weak self] _ in
                Task { @MainActor in self?.stopListening(token) }
            }
        }
    }
}

struct ProviderForm: View {
    struct Draft: Identifiable, Hashable {
        let id = UUID()
        var provider: AgentProvider
        var preset: ProviderPreset?

        init(provider: AgentProvider, preset: ProviderPreset?) {
            self.provider = provider
            self.preset = preset
        }

        init(preset: ProviderPreset) {
            provider = AgentProvider(
                id: "", preset: preset.id, name: preset.name, baseURL: preset.baseURL,
                authKind: preset.auth == "account" ? "account" : "api_key",
                defaultModel: preset.suggested?.first ?? "", isLocal: preset.local, models: nil,
                hasKey: false)
            self.preset = preset
        }
    }

    @State var draft: Draft
    let saved: () async -> Void
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @Environment(\.dismiss) private var dismiss
    @State private var key = ""
    @State private var failure: String?
    @State private var saving = false
    @State private var testing = false
    @State private var test: ProviderTest?
    @State private var removing = false
    @State private var signingIn = false
    @State private var signedIn = false
    @State private var webSession: ChatGPTWebSession?
    @State private var pageClosed = false
    @State private var attempt: Int64?

    private var isAccount: Bool { draft.provider.isAccount }

    private var hasAccount: Bool { signedIn || (!draft.provider.id.isEmpty && isAccount) }

    private var description: String {
        if isAccount { return L.settingsProviderDescAccount }
        var desc = L.settingsProviderDescDevice
        if draft.preset?.local ?? false { desc = L.settingsProviderDescLocal }
        if draft.preset?.id == "claude" { desc = L.settingsProviderClaudeNote(desc: desc) }
        return desc
    }

    static func takesKey(_ draft: Draft) -> Bool {
        draft.preset?.auth != "none" || draft.provider.hasKey
    }

    private var models: [String] {
        if let m = draft.provider.models, !m.isEmpty { return m }
        return draft.preset?.suggested ?? []
    }

    var body: some View {
        Form {
            Group {
                if isAccount {
                    accountSection
                } else {
                    Section {
                        TextField(L.settingsName, text: $draft.provider.name)
                        TextField(
                            draft.preset.map {
                                $0.placeholder.isEmpty ? L.settingsBaseURL : $0.placeholder
                            }
                                ?? L.settingsBaseURL,
                            text: $draft.provider.baseURL
                        )
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        if Self.takesKey(draft) {
                            SecureField(
                                L.settingsAPIKey, text: $key,
                                prompt: Text(
                                    draft.provider.hasKey ? "••••••••" : L.settingsAPIKey))
                        }
                    } footer: {
                        Text(description)
                    }
                }
                if let test {
                    Section {
                        Label(
                            test.message,
                            systemImage: test.ok
                                ? "checkmark.circle.fill" : "exclamationmark.triangle.fill"
                        )
                        .foregroundStyle(test.ok ? palette.okText : palette.dangerText)
                    }
                }
                if let failure {
                    Section { Text(failure).foregroundStyle(palette.dangerText) }
                }
                if !isAccount || hasAccount {
                    Section(L.commonModel) {
                        TextField(L.settingsModelID, text: $draft.provider.defaultModel)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                        ForEach(models, id: \.self) { m in
                            Button {
                                draft.provider.defaultModel = m
                            } label: {
                                HStack {
                                    Text(m).foregroundStyle(.primary)
                                    Spacer()
                                    if m == draft.provider.defaultModel {
                                        Image(systemName: "checkmark").foregroundStyle(.tint)
                                    }
                                }
                            }
                        }
                    }
                }
                if !draft.provider.id.isEmpty {
                    Section {
                        Button(L.commonRemove, role: .destructive) { removing = true }
                    }
                }
            }
            .listRowBackground(palette.surface)
        }
        .navigationTitle(
            draft.provider.id.isEmpty
                ? L.settingsConnectProvider(name: draft.preset?.name ?? draft.provider.name)
                : L.settingsProviderEdit(name: draft.provider.name)
        )
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                if testing {
                    ProgressView()
                } else {
                    Button(L.settingsTest) { runTest() }
                        .disabled(
                            isAccount ? !hasAccount : draft.provider.baseURL.isEmpty)
                }
            }
            ToolbarItem(placement: .confirmationAction) {
                if saving {
                    ProgressView()
                } else {
                    Button(L.commonSave) { save() }
                        .disabled(
                            (isAccount ? !hasAccount : draft.provider.baseURL.isEmpty)
                                || draft.provider.defaultModel.isEmpty)
                }
            }
        }
        .alert(L.settingsRemoveProviderTitle(name: draft.provider.name), isPresented: $removing) {
            Button(L.commonCancel, role: .cancel) {}
            Button(L.commonRemove, role: .destructive) { remove() }
        } message: {
            Text(ProvidersSection.removeMessage(for: draft.provider))
        }
        .onDisappear {
            webSession?.close()
            if !saving, let attempt { app.client?.forgetChatGPTSignIn(attempt) }
        }
    }

    @ViewBuilder private var accountSection: some View {
        Section {
            if hasAccount {
                HStack(spacing: 10) {
                    Image(systemName: "checkmark").foregroundStyle(palette.okText)
                    Text(
                        (draft.provider.email ?? "").isEmpty
                            ? L.settingsSignedInAccount
                            : L.settingsSignedInAs(email: draft.provider.email ?? "")
                    )
                    .foregroundStyle(palette.okText)
                    Spacer()
                    Button(L.settingsSwitchAccount) { signIn() }
                        .disabled(signingIn)
                }
            } else {
                Button {
                    signIn()
                } label: {
                    if signingIn {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text(L.settingsWaitingBrowser)
                        }
                    } else {
                        Label(L.settingsChatGPTSignIn, systemImage: "person.crop.circle")
                    }
                }
                .disabled(signingIn || app.client == nil)
            }
        } footer: {
            Text(hasAccount ? description : L.settingsChatGPTNote)
        }
    }

    private func signIn() {
        guard let client = app.client else { return }
        signingIn = true
        failure = nil
        test = nil
        pageClosed = false
        Task {
            defer { signingIn = false }
            do {
                let begun = try await client.beginChatGPTSignIn(draft.provider.id)
                attempt = begun.attempt
                let session = ChatGPTWebSession()
                webSession = session
                let shown = session.open(begun.url) {
                    pageClosed = true
                    Task {
                        try? await Task.sleep(for: .seconds(5))
                        client.cancelChatGPTSignIn(begun.attempt)
                    }
                }
                guard shown else {
                    client.cancelChatGPTSignIn(begun.attempt)
                    webSession = nil
                    failure = L.settingsChatGPTSignInFailed
                    return
                }
                let account = try await client.finishChatGPTSignIn()
                session.close()
                webSession = nil
                signedIn = true
                draft.provider.email = account.email
                draft.provider.name = "ChatGPT"
                if let models = try? await client.chatGPTModels(), !models.isEmpty {
                    draft.provider.models = models
                    if !models.contains(draft.provider.defaultModel) {
                        draft.provider.defaultModel = models[0]
                    }
                }
            } catch {
                webSession?.close()
                webSession = nil
                if !pageClosed { failure = error.localizedDescription }
            }
        }
    }

    private func runTest() {
        guard let client = app.client else { return }
        var p = draft.provider
        p.apiKey = key.isEmpty ? nil : key
        testing = true
        test = nil
        Task {
            do {
                let found = try await client.testProvider(p)
                test = found
                if found.ok {
                    draft.provider.supportsTools = found.supportsTools
                }
                if let m = found.models, !m.isEmpty {
                    draft.provider.models = m
                    if !m.contains(draft.provider.defaultModel) {
                        draft.provider.defaultModel = m[0]
                    }
                }
            } catch {
                test = ProviderTest(
                    ok: false, supportsTools: false, message: error.localizedDescription)
            }
            testing = false
        }
    }

    private func save() {
        guard let client = app.client else { return }
        var p = draft.provider
        p.apiKey = key.isEmpty ? nil : key
        saving = true
        Task {
            do {
                try await client.saveProvider(p)
                await saved()
                dismiss()
            } catch {
                failure = error.localizedDescription
                saving = false
            }
        }
    }

    private func remove() {
        guard let client = app.client else { return }
        saving = true
        Task {
            do {
                try await client.removeProvider(draft.provider.id)
                await saved()
                dismiss()
            } catch {
                failure = error.localizedDescription
            }
            saving = false
        }
    }
}

@MainActor
final class ChatGPTWebSession: NSObject, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?
    private var anchor: ASPresentationAnchor?

    func open(_ url: URL, closed: @escaping @MainActor () -> Void) -> Bool {
        let windows = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
        guard let anchor = windows.first(where: \.isKeyWindow) ?? windows.first else {
            return false
        }
        self.anchor = anchor
        let session = ASWebAuthenticationSession(
            url: url, callback: .customScheme("leaflet-chatgpt")
        ) { [weak self] _, error in
            guard let error = error as? ASWebAuthenticationSessionError,
                error.code == .canceledLogin
            else { return }
            Task { @MainActor in
                if self?.session != nil { closed() }
            }
        }
        session.presentationContextProvider = self
        self.session = session
        guard session.start() else {
            self.session = nil
            return false
        }
        return true
    }

    func close() {
        let session = self.session
        self.session = nil
        session?.cancel()
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession)
        -> ASPresentationAnchor
    {
        MainActor.assumeIsolated {
            guard let anchor else { fatalError("ChatGPTWebSession asked for a window before open") }
            return anchor
        }
    }
}

struct HeaderWithAction<Action: View>: View {
    let title: String
    @ViewBuilder let action: () -> Action

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title)
            Spacer()
            action()
                .font(.subheadline.weight(.semibold))
                .textCase(nil)
                .labelStyle(.titleAndIcon)
        }
    }
}
