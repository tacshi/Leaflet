import AuthenticationServices
import SwiftUI

/// ProvidersSection is Settings › Agent providers: the services agents run
/// on, adding one from the desktop's presets, and the defaults agents run
/// with.
struct ProvidersSection: View {
    /// editing is the provider Settings shows the form of, pushed on its
    /// navigation: a sheet from a row of the settings' form doesn't stay.
    @Binding var editing: ProviderForm.Draft?
    /// version changes as a provider is saved, to read them again.
    let version: Int
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var providers: [AgentProvider] = []
    @State private var presets: [ProviderPreset] = []
    @State private var defaultAgent = ""
    @Environment(SettingsPrompt.self) private var prompt
    @State private var failure: String?

    var body: some View {
        @Bindable var app = app
        Section {
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
            Menu(L.settingsAddProvider) {
                // The groups apart, unnamed, as the desktop's menu has them.
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
            }
        } header: {
            // On the header, a view of its own: on the section, it would go
            // on each of its rows, a read of the providers each.
            Text(L.settingsSectionProviders)
                .task(id: version) { await load() }
                // Signed in or out here, or a provider changed on another
                // device: signing out takes the account's providers away.
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

        if !providers.isEmpty {
            // As the desktop's Defaults: the agent pages run, and how
            // agents run.
            Section(L.settingsDefaults) {
                if providers.count > 1 {
                    // Kept as the user picks it, and only then: load()
                    // showing the first provider for a default the core
                    // cleared, its provider removed, keeps none.
                    Picker(
                        selection: Binding(
                            get: { defaultAgent },
                            set: { value in
                                defaultAgent = value
                                if !value.isEmpty { app.save(["default_model": value]) }
                            })
                    ) {
                        ForEach(providers) { p in
                            Text("\(p.name) · \(p.defaultModel)").tag(p.id + "|")
                        }
                    } label: {
                        Text(L.settingsDefaultAgent)
                        Text(L.settingsDefaultAgentDesc)
                    }
                }
                Toggle(isOn: $app.agentsReadOtherPages) {
                    Text(L.settingsReadOtherPages)
                    Text(L.settingsReadOtherPagesDesc)
                }
                .onChange(of: app.agentsReadOtherPages) { _, on in
                    app.save(["agents_read_other_pages": on])
                }
            }
        }
    }

    /// place says where a provider runs, as the desktop's list does.
    static func place(of p: AgentProvider) -> String {
        var place = L.settingsProviderCloud
        if p.isAccount {
            place =
                (p.email ?? "").isEmpty
                ? L.settingsProviderPlan : L.settingsProviderPlanEmail(email: p.email ?? "")
        } else if p.isLocal {
            place = L.settingsProviderLocal(url: p.baseURL)
        }
        // Agents always use tools on a ChatGPT plan, whatever an older
        // test found.
        if p.supportsTools == false, !p.isAccount {
            place = L.settingsProviderChatOnly(where: place)
        }
        return place
    }

    static func removeMessage(for p: AgentProvider) -> String {
        if p.isAccount { return L.settingsRemoveProviderAccount }
        if p.isLocal { return L.settingsRemoveProviderLocal }
        return L.settingsRemoveProviderCloud
    }

    /// addable are the presets to add: one ChatGPT sign-in is enough, and
    /// its row edits it.
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
        if let raw = try? await client.settings(),
            let s = try? JSONDecoder().decode([String: JSONValue].self, from: Data(raw.utf8))
        {
            // The default names a provider, whose model is set on it; with
            // none, the first, as the desktop shows it.
            let id = (s["default_model"]?.string ?? "").split(separator: "|").first.map(String.init)
            defaultAgent =
                providers.first { $0.id == id }.map { $0.id + "|" } ?? providers.first.map {
                    $0.id + "|"
                } ?? ""
        }
    }

    /// askRemove asks before removing p, as the desktop does.
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
    /// changes yields as the core says one of tables changed, until the
    /// task reading it ends: Settings reads again what it shows of them.
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

/// ProviderForm adds or changes a provider: its name, address, key and
/// model, or for ChatGPT, the sign-in.
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
    @State private var key = ""  // mobsf-ignore: ios_hardcoded_secret -- the key typed in.
    @State private var failure: String?
    @State private var saving = false
    @State private var testing = false
    @State private var test: ProviderTest?
    @State private var removing = false
    /// signingIn is set while OpenAI's page is open; signedIn, once a
    /// ChatGPT sign-in is made here, to save.
    @State private var signingIn = false
    @State private var signedIn = false
    @State private var webSession: ChatGPTWebSession?
    /// pageClosed is set as the person closes OpenAI's page.
    @State private var pageClosed = false

    private var isAccount: Bool { draft.provider.isAccount }

    /// account says the form has a ChatGPT sign-in: made here, or the
    /// provider's own.
    private var hasAccount: Bool { signedIn || (!draft.provider.id.isEmpty && isAccount) }

    /// description says where the provider runs: a plan signed in to, a
    /// model server, a cloud one kept on this device, as one is signed
    /// out, or on the server.
    private var description: String {
        if isAccount { return L.settingsProviderDescAccount }
        var desc = L.settingsProviderDescCloud
        let signedIn = app.status?.signedIn ?? false
        if draft.preset?.local ?? false {
            desc = L.settingsProviderDescLocal
        } else if draft.provider.isLocal || (draft.provider.id.isEmpty && !signedIn) {
            desc = L.settingsProviderDescDevice
        }
        if draft.preset?.id == "claude" { desc = L.settingsProviderClaudeNote(desc: desc) }
        return desc
    }

    /// models are the ones to pick from: the provider's own once a test
    /// has listed them, else the preset's suggestions.
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
                        // A preset's placeholder is the address to fill in,
                        // as Custom endpoint's.
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
                        if draft.preset?.auth != "none" {
                            SecureField(
                                L.settingsAPIKey, text: $key,
                                prompt: Text(
                                    draft.provider.hasKey ? "••••••••" : L.settingsAPIKey))
                        }
                    } footer: {
                        Text(description)
                    }
                }
                // What a test or a save found shows above the models, which
                // may run past the bottom of the form.
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
            // A sign-in made here and not saved is given up.
            webSession?.close()
            if !saving { app.client?.forgetChatGPTSignIn() }
        }
    }

    /// accountSection signs in with ChatGPT, or shows who is signed in, as
    /// the desktop's dialog does.
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

    /// signIn signs in with ChatGPT: OpenAI's page opens in a web session
    /// while the core waits on its loopback for the answer, and closes as
    /// it comes.
    private func signIn() {
        guard let client = app.client else { return }
        signingIn = true
        failure = nil
        test = nil
        pageClosed = false
        Task {
            defer { signingIn = false }
            do {
                let url = try await client.beginChatGPTSignIn(draft.provider.id)
                let session = ChatGPTWebSession()
                webSession = session
                let shown = session.open(url) {
                    // Closed before OpenAI answered, the sign-in is given
                    // up, and that is no failure. Its page says to close it
                    // once OpenAI has answered: the sign-in then has a
                    // moment to finish first.
                    pageClosed = true
                    Task {
                        try? await Task.sleep(for: .seconds(5))
                        if !signedIn { client.cancelChatGPTSignIn() }
                    }
                }
                // A page that can't show leaves nothing to sign in with.
                guard shown else {
                    client.cancelChatGPTSignIn()
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
                // The plan's models, once its page is away.
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

    /// runTest tries the provider as the form has it, and offers the
    /// models it serves.
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

/// ChatGPTWebSession shows OpenAI's sign-in page over the app. The page
/// ends at the core's loopback, not at a scheme the session waits for, so
/// the app closes the session as the core hears the answer.
@MainActor
final class ChatGPTWebSession: NSObject, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?
    /// anchor is the window the page opens over.
    private var anchor: ASPresentationAnchor?

    /// open shows url, and reports whether it could; closed runs if the
    /// person closes the page.
    func open(_ url: URL, closed: @escaping @MainActor () -> Void) -> Bool {
        // The settings' window, which the page opens over.
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
                // Not when close took it away.
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

    /// close takes the page away, as the sign-in is done.
    func close() {
        let session = self.session
        self.session = nil
        session?.cancel()
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession)
        -> ASPresentationAnchor
    {
        MainActor.assumeIsolated {
            // open found it before it started the session, which asks.
            guard let anchor else { fatalError("ChatGPTWebSession asked for a window before open") }
            return anchor
        }
    }
}
