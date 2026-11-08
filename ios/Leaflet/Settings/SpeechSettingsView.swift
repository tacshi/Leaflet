import SwiftUI

/// SpeechSection is Settings' row for Speech to text: where recordings
/// are transcribed, which opens its settings.
struct SpeechSection: View {
    @Environment(AppModel.self) private var app
    @State private var status = ""

    var body: some View {
        Section(L.settingsSectionSpeech) {
            NavigationLink {
                SpeechSettingsView(status: $status)
            } label: {
                LabeledContent(L.recSttTranscription, value: status)
            }
        }
        .task {
            if let s = try? await app.client?.speechSettings() { status = s.status }
            // Where transcription runs follows signing in or out, and the
            // keys and settings, here or from another device.
            for await _ in app.changes(of: Self.tables) {
                if let s = try? await app.client?.speechSettings() { status = s.status }
            }
        }
    }

    /// tables are those whose changes change the speech settings: the
    /// session, the settings and the agent providers, whose keys a cloud
    /// endpoint uses.
    static let tables: Set<String> = ["session", "key_values", "model_providers", "*"]
}

/// SpeechSettingsView is Settings › Speech to text, as on the desktop: the
/// speech providers set up, each with its own page of settings, the
/// language, and a test of the microphone. There is no default: each
/// transcription picks its provider.
struct SpeechSettingsView: View {
    @Binding var status: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var loaded: SpeechSettings?
    @State private var stt: SttSettings?
    /// keepAudio is the choice made here, over the one read.
    @State private var keepAudio: String?
    @State private var endpointKey = ""
    /// savedKey is the endpoint's key as kept, and keyFocused says its field
    /// has the keyboard: the key is kept as the field lets it go, not as
    /// each letter comes, which would keep the first letters of it.
    @State private var savedKey = ""
    @FocusState private var keyFocused: Bool
    @State private var sonioxKey = ""
    @State private var mossKey = ""
    @State private var providers: [AgentProvider] = []
    @State private var testing = false
    @State private var saving = false
    /// result is how the last Test, or saving a key, went.
    @State private var result: (ok: Bool, text: String)?
    /// onDevice are the languages the device's own engine transcribes, by
    /// code, once it has said.
    @State private var onDevice: Set<String>?
    /// adding is the provider being added, whose page shows.
    @State private var adding: String?

    var body: some View {
        Form {
            if let loaded, let stt = Binding($stt) {
                engine(loaded, stt)
                recording(loaded, stt)
            }
        }
        .themedForm(palette)
        .navigationTitle(L.recSttTitle)
        .navigationBarTitleDisplayMode(.inline)
        // A provider being added, from the Add menu: its page, as its row's.
        .navigationDestination(item: $adding) { id in
            if let stt = Binding($stt) { providerPage(id, stt) }
        }
        .task {
            if DeviceTranscriber.isAvailable { onDevice = await DeviceTranscriber.languages() }
        }
        .task {
            await load()
            for await _ in app.changes(of: SpeechSection.tables) {
                await refresh()
                providers = ((try? await app.client?.providers()) ?? []).filter { !$0.isLocal }
            }
        }
        .onDisappear {
            saveKey()
            // What changed in the moment before Back, which the wait below
            // would drop as the view goes.
            if let stt, loaded != nil, stt != loaded?.stt {
                Task { await save(stt) }
            }
        }
        // Kept as it changes, once typing stops for a moment.
        .task(id: stt) {
            guard let stt, loaded != nil, stt != loaded?.stt else { return }
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            await save(stt)
        }
    }

    /// save keeps the settings as stt has them, and reads again what they
    /// make of themselves.
    private func save(_ stt: SttSettings) async {
        try? await app.client?.saveSpeechSettings(stt)
        await refresh()
    }

    private func load() async {
        guard let client = app.client, let s = try? await client.speechSettings() else { return }
        loaded = s
        stt = s.stt
        endpointKey = s.endpointKey
        savedKey = s.endpointKey
        status = s.status
        providers = ((try? await client.providers()) ?? []).filter { !$0.isLocal }
    }

    /// refresh reads again what the settings make of themselves: where
    /// transcription runs, and whether the endpoint is on this network.
    private func refresh() async {
        guard let s = try? await app.client?.speechSettings() else { return }
        loaded?.stt = s.stt
        loaded?.status = s.status
        loaded?.localEndpoint = s.localEndpoint
        loaded?.sonioxSaved = s.sonioxSaved
        loaded?.mossSaved = s.mossSaved
        loaded?.providers = s.providers
        loaded?.addable = s.addable
        // The core takes the first provider set up when the one chosen is
        // not, as when the first one's key is saved.
        if stt?.provider != s.stt.provider { stt?.provider = s.stt.provider }
        status = s.status
    }

    @ViewBuilder
    private func engine(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        Section {
            if s.providers.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Text(L.recSttNoProviders)
                    Text(L.recSttNoProvidersDesc).font(.caption).foregroundStyle(.secondary)
                }
            }
            ForEach(s.providers) { p in
                NavigationLink {
                    providerPage(p.id, stt)
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(p.name).foregroundStyle(.primary)
                        Text(p.place ?? "").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .swipeActions {
                    if p.removable == true {
                        Button(L.commonRemove, role: .destructive) {
                            Task { await remove(p.id) }
                        }
                        .tint(.red)
                    }
                }
            }
            // Each transcription picks its provider: speakers are labelled by
            // those that can, Soniox and MOSS.
            if s.providers.contains(where: { ["soniox", "moss"].contains($0.id) }) {
                Toggle(L.recSttSpeakers, isOn: stt.speakerLabels)
            }
        } header: {
            HeaderWithAction(title: L.recSttTranscription) {
                Menu {
                    ForEach(s.addable) { p in
                        Button(p.name) {
                            result = nil
                            adding = p.id
                        }
                    }
                } label: {
                    Label(L.settingsAddProvider, systemImage: "plus")
                }
                .disabled(s.addable.isEmpty)
            }
        } footer: {
            if let result {
                Text(result.text).foregroundStyle(result.ok ? palette.okText : palette.dangerText)
            }
        }
    }

    /// providerPage is a speech provider's own settings: its key, address
    /// or model, a test of them, and removing it.
    private func providerPage(_ id: String, _ stt: Binding<SttSettings>) -> some View {
        ProviderPage { dismiss in
            Form {
                Section {
                    if let s = loaded {
                        switch id {
                        case "on-device":
                            Text(L.recSttOnDeviceDesc).font(.footnote).foregroundStyle(.secondary)
                        case "soniox":
                            soniox(s, stt)
                        case "moss":
                            moss(s, stt)
                        default:
                            endpoint(s, stt)
                        }
                    }
                } footer: {
                    if let result {
                        Text(result.text).foregroundStyle(
                            result.ok ? palette.okText : palette.dangerText)
                    }
                }
                if loaded?.providers.first(where: { $0.id == id })?.removable == true {
                    Section {
                        Button(L.commonRemove, role: .destructive) {
                            Task {
                                await remove(id)
                                dismiss()
                            }
                        }
                    }
                }
            }
            .themedForm(palette)
            .navigationTitle(SpeechProvider.name(of: id))
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    /// remove takes a provider off the list, its key off this device.
    private func remove(_ id: String) async {
        do {
            try await app.client?.removeSpeechProvider(id)
            if id == "openai-compatible" {
                endpointKey = ""
                savedKey = ""
            }
            result = nil
        } catch {
            result = (false, error.localizedDescription)
        }
        // The address the core cleared, which the fields here still have.
        if let s = try? await app.client?.speechSettings() {
            stt?.baseURL = s.stt.baseURL
            stt?.model = s.stt.model
            stt?.providerID = s.stt.providerID
        }
        await refresh()
    }

    @ViewBuilder
    private func endpoint(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        // A server on another machine: none runs on the iPad itself.
        field(L.commonEndpoint, "http://mac.local:8000/v1", stt.baseURL, url: true)
        field(L.commonModel, "gpt-transcribe", stt.model)
        let url = stt.wrappedValue.baseURL.trimmingCharacters(in: .whitespaces)
        if url.isEmpty {
            // The key's row comes with an address: where it is says where
            // the key is kept.
        } else if s.localEndpoint {
            SecureField(L.recSttApiKey, text: $endpointKey, prompt: Text(L.recSttOptional))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .focused($keyFocused)
                .onSubmit(saveKey)
                .onChange(of: keyFocused) { if !keyFocused { saveKey() } }
            // The test has the fields as they are now.
            let (model, key) = (stt.wrappedValue.model, endpointKey)
            testButton { try await $0.testEndpoint(url: url, model: model, key: key) }
        } else {
            // The server calls a cloud endpoint with an agent provider's key.
            Picker(
                L.recSttKeyFrom,
                selection: Binding(
                    get: { stt.wrappedValue.providerID ?? "" },
                    set: { stt.wrappedValue.providerID = $0.isEmpty ? nil : $0 })
            ) {
                Text(L.recSttNoKey).tag("")
                ForEach(providers) { Text($0.name).tag($0.id) }
            }
            Text(providers.isEmpty ? L.recSttAddProvider : L.recSttKeyFromDesc)
                .font(.footnote).foregroundStyle(.secondary)
        }
    }

    /// saveKey keeps the endpoint's key as the field has it, if it
    /// changed.
    private func saveKey() {
        let (key, before) = (endpointKey, savedKey)
        guard loaded != nil, key != before else { return }
        savedKey = key
        Task {
            do { try await app.client?.setEndpointKey(key) } catch {
                savedKey = before
                result = (false, L.recSttKeyNotSaved(error: error.localizedDescription))
            }
        }
    }

    @ViewBuilder
    private func soniox(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            SecureField(
                L.recSttSonioxKey, text: $sonioxKey,
                prompt: Text(s.sonioxSaved ? L.recSttKeySaved : L.recSttPasteKey)
            )
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            Text(s.signedIn ? L.recSttSonioxOnServer : L.recSttSonioxOnDevice)
                .font(.footnote).foregroundStyle(.secondary)
        }
        HStack {
            let key = sonioxKey
            testButton { try await $0.testSoniox(key) }
            Spacer()
            Button(L.commonSave) {
                saving = true
                Task {
                    defer { saving = false }
                    do {
                        let place = try await app.client?.saveSonioxKey(sonioxKey) ?? ""
                        sonioxKey = ""
                        result = (true, place)
                        await refresh()
                    } catch {
                        result = (false, error.localizedDescription)
                    }
                }
            }
            .disabled(saving || sonioxKey.trimmingCharacters(in: .whitespaces).isEmpty)
        }
        .buttonStyle(.borderless)
        field(L.commonModel, "stt-rt-v5", stt.sonioxModel)
    }

    @ViewBuilder
    private func moss(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            SecureField(
                L.recSttMossKey, text: $mossKey,
                prompt: Text(s.mossSaved ? L.recSttKeySaved : L.recSttPasteKey)
            )
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            Text(L.recSttMossDesc).font(.footnote).foregroundStyle(.secondary)
        }
        HStack {
            let key = mossKey
            testButton { try await $0.testMoss(key) }
            Spacer()
            Button(L.commonSave) {
                saving = true
                Task {
                    defer { saving = false }
                    do {
                        let place = try await app.client?.saveMossKey(mossKey) ?? ""
                        mossKey = ""
                        result = (true, place)
                        await refresh()
                    } catch {
                        result = (false, error.localizedDescription)
                    }
                }
            }
            .disabled(saving || mossKey.trimmingCharacters(in: .whitespaces).isEmpty)
        }
        .buttonStyle(.borderless)
    }

    /// languages are those the language picker offers: on the device, only
    /// those its engine transcribes, and the one chosen, to change.
    private func languages(_ s: SpeechSettings, _ stt: SttSettings) -> [SpeechSettings.Language] {
        guard stt.provider == "on-device", let onDevice else { return s.languages }
        return s.languages.filter {
            $0.code == "auto" || $0.code == stt.language || onDevice.contains($0.code)
        }
    }

    @ViewBuilder
    private func recording(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        let recorder = Recorder.shared
        let state = recorder.state
        let languages = languages(s, stt.wrappedValue)
        Section(L.commonRecording) {
            Picker(
                L.recSttLanguage,
                selection: Binding(
                    get: {
                        languages.contains { $0.code == stt.wrappedValue.language }
                            ? stt.wrappedValue.language : "auto"
                    },
                    set: { stt.wrappedValue.language = $0 })
            ) {
                ForEach(languages, id: \.code) { Text($0.name).tag($0.code) }
            }
            .pickerStyle(.navigationLink)
            VStack(alignment: .leading, spacing: 4) {
                Button(
                    state.testing ? L.recSttTesting : L.recSttRecordTest,
                    systemImage: "record.circle"
                ) {
                    recorder.testMicrophone()
                }
                .disabled(state.recording || state.testing)
                if let heard = heard(state, live: recorder.liveText) {
                    Text(heard).font(.footnote).foregroundStyle(.secondary)
                }
            }
            Picker(
                L.settingsKeepAudio,
                selection: Binding(
                    get: { keepAudio ?? s.keepAudio },
                    set: { keep in
                        keepAudio = keep
                        Task { try? await app.client?.setKeepAudio(keep) }
                    })
            ) {
                Text(L.settingsKeepAudioUntilUploaded).tag("until_uploaded")
                Text(L.settingsKeepAudio30Days).tag("30_days")
                Text(L.settingsKeepAudioAlways).tag("always")
            }
        }
    }

    /// heard says how the test of the microphone goes, or what it heard:
    /// live, as it hears it.
    private func heard(_ state: RecordingState, live: String) -> String? {
        if !state.testing { return state.heard.isEmpty ? nil : state.heard }
        if state.starting { return L.recSttMicStarting }
        if state.active {
            return live.isEmpty ? L.recSttListening(seconds: 4) : live
        }
        return L.recTranscribing
    }

    private func field(
        _ label: String, _ placeholder: String, _ text: Binding<String>, url: Bool = false
    )
        -> some View
    {
        FieldRow(label: label) {
            TextField(label, text: text, prompt: Text(verbatim: placeholder))
                .keyboardType(url ? .URL : .default)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
        }
    }

    /// testButton is a Test button that runs test, saying how it went.
    private func testButton(_ test: @escaping @Sendable (CoreClient) async throws -> String)
        -> some View
    {
        Button(testing ? L.recSttTesting : L.recSttTest) {
            guard let client = app.client else { return }
            testing = true
            result = nil
            Task {
                defer { testing = false }
                do {
                    result = (true, try await test(client))
                } catch {
                    result = (false, error.localizedDescription)
                }
            }
        }
        .disabled(testing)
    }
}

/// ProviderPage is a pushed page that can go back by itself, as removing
/// its provider does.
private struct ProviderPage<Content: View>: View {
    @Environment(\.dismiss) private var dismiss
    @ViewBuilder let content: (DismissAction) -> Content

    var body: some View { content(dismiss) }
}
