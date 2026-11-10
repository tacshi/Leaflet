import SwiftUI

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
            for await _ in app.changes(of: Self.tables) {
                if let s = try? await app.client?.speechSettings() { status = s.status }
            }
        }
    }

    static let tables: Set<String> = ["key_values", "*"]
}

struct SpeechSettingsView: View {
    @Binding var status: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var loaded: SpeechSettings?
    @State private var stt: SttSettings?
    @State private var keepAudio: String?
    @State private var endpointKey = ""
    @State private var savedKey = ""
    @FocusState private var keyFocused: Bool
    @State private var sonioxKey = ""
    @State private var mossKey = ""
    @State private var testing = false
    @State private var saving = false
    @State private var result: (ok: Bool, text: String)?
    @State private var onDevice: Set<String>?
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
            }
        }
        .onDisappear {
            saveKey()
            if let stt, loaded != nil, stt != loaded?.stt {
                Task { await save(stt) }
            }
        }
        .task(id: stt) {
            guard let stt, loaded != nil, stt != loaded?.stt else { return }
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            await save(stt)
        }
    }

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
    }

    private func refresh() async {
        guard let s = try? await app.client?.speechSettings() else { return }
        loaded?.stt = s.stt
        loaded?.status = s.status
        loaded?.sonioxSaved = s.sonioxSaved
        if !keyFocused, endpointKey == savedKey, s.stt.baseURL == stt?.baseURL {
            endpointKey = s.endpointKey
            savedKey = s.endpointKey
        }
        loaded?.mossSaved = s.mossSaved
        loaded?.providers = s.providers
        loaded?.addable = s.addable
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
                            endpoint(stt)
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
        if let s = try? await app.client?.speechSettings() {
            stt?.baseURL = s.stt.baseURL
            stt?.model = s.stt.model
        }
        await refresh()
    }

    @ViewBuilder
    private func endpoint(_ stt: Binding<SttSettings>) -> some View {
        field(L.commonEndpoint, "http://mac.local:8000/v1", stt.baseURL, url: true)
            .onChange(of: stt.wrappedValue.baseURL) { before, now in
                guard Self.origin(before) != Self.origin(now) else { return }
                endpointKey = ""
                savedKey = ""
            }
        field(L.commonModel, "gpt-transcribe", stt.model)
        let url = stt.wrappedValue.baseURL.trimmingCharacters(in: .whitespaces)
        if !url.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                SecureField(L.recSttApiKey, text: $endpointKey, prompt: Text(L.recSttOptional))
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .focused($keyFocused)
                    .onSubmit(saveKey)
                    .onChange(of: keyFocused) { if !keyFocused { saveKey() } }
                Text(L.recSttKeyInKeychain).font(.footnote).foregroundStyle(.secondary)
            }
            let model = stt.wrappedValue.model
            let typed = endpointKey == savedKey ? nil : endpointKey
            testButton { client in
                if let typed { try await client.setEndpointKey(url: url, key: typed) }
                return try await client.testEndpoint(url: url, model: model, key: "")
            }
        }
    }

    private static func origin(_ address: String) -> String? {
        let address = address.trimmingCharacters(in: .whitespaces)
        guard let url = URLComponents(string: address), let scheme = url.scheme,
            let host = url.host, !host.isEmpty
        else { return nil }
        return "\(scheme)://\(host)\(url.port.map { ":\($0)" } ?? "")".lowercased()
    }

    private func saveKey() {
        let (key, before) = (endpointKey, savedKey)
        guard loaded != nil, key != before, let url = stt?.baseURL else { return }
        savedKey = key
        Task {
            do { try await app.client?.setEndpointKey(url: url, key: key) } catch {
                savedKey = before
                result = (false, L.recSttKeyNotSaved(error: error.localizedDescription))
            }
            await refresh()
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
            Text(L.recSttSonioxOnDevice)
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
        Section {
            VoiceNotesFolderPicker()
        } footer: {
            Text(LI.voiceNotesHint)
        }
    }

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

private struct ProviderPage<Content: View>: View {
    @Environment(\.dismiss) private var dismiss
    @ViewBuilder let content: (DismissAction) -> Content

    var body: some View { content(dismiss) }
}

private struct VoiceNotesFolderPicker: View {
    @Environment(AppModel.self) private var app
    @AppStorage(VoiceNotes.folderKey) private var folder = ""

    var body: some View {
        let folders = app.library.folders.sorted {
            $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
        let chosen =
            folder == VoiceNotes.topLevel || folders.contains(where: { $0.id == folder })
            ? folder : ""
        Picker(LI.voiceNotesGoIn, selection: Binding(get: { chosen }, set: { folder = $0 })) {
            if chosen.isEmpty { Text(LI.voiceNotesFolder).tag("") }
            Text(L.dialogMoveTopLevel).tag(VoiceNotes.topLevel)
            ForEach(folders) { Text($0.name).tag($0.id) }
        }
        .pickerStyle(.navigationLink)
    }
}
