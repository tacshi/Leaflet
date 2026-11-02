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
        }
    }
}

/// SpeechSettingsView is Settings › Speech to text, as on the desktop: the
/// engine that transcribes recordings, its key, the language, and a test
/// of the microphone.
struct SpeechSettingsView: View {
    @Binding var status: String
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @State private var loaded: SpeechSettings?
    @State private var stt: SttSettings?
    @State private var endpointKey = ""
    /// savedKey is the endpoint's key as kept, and keyFocused says its field
    /// has the keyboard: the key is kept as the field lets it go, not as
    /// each letter comes, which would keep the first letters of it.
    @State private var savedKey = ""
    @FocusState private var keyFocused: Bool
    @State private var sonioxKey = ""
    @State private var providers: [AgentProvider] = []
    @State private var testing = false
    @State private var saving = false
    /// result is how the last Test, or saving a key, went.
    @State private var result: (ok: Bool, text: String)?

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
        .task { await load() }
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
        status = s.status
    }

    @ViewBuilder
    private func engine(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        Section {
            Picker(L.recSttTranscription, selection: stt.provider) {
                if s.onDevice || stt.wrappedValue.provider == "on-device" {
                    Text(L.recSttOnDevice).tag("on-device")
                }
                Text(verbatim: "Soniox").tag("soniox")
                Text(L.commonEndpoint).tag("openai-compatible")
            }
            .pickerStyle(.segmented)
            .onChange(of: stt.wrappedValue.provider) { result = nil }
            switch stt.wrappedValue.provider {
            case "on-device":
                Text(L.recSttOnDeviceDesc).font(.footnote).foregroundStyle(.secondary)
            case "soniox":
                soniox(s, stt)
            default:
                endpoint(s, stt)
            }
        } header: {
            Text(L.recSttTranscription)
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                Text(s.status)
                if let result {
                    Text(result.text).foregroundStyle(
                        result.ok ? palette.okText : palette.dangerText)
                }
            }
        }
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
        Toggle(L.recSttSpeakers, isOn: stt.speakerLabels)
    }

    @ViewBuilder
    private func recording(_ s: SpeechSettings, _ stt: Binding<SttSettings>) -> some View {
        let recorder = Recorder.shared
        let state = recorder.state
        Section(L.commonRecording) {
            Picker(
                L.recSttLanguage,
                selection: Binding(
                    get: {
                        s.languages.contains { $0.code == stt.wrappedValue.language }
                            ? stt.wrappedValue.language : "auto"
                    },
                    set: { stt.wrappedValue.language = $0 })
            ) {
                ForEach(s.languages, id: \.code) { Text($0.name).tag($0.code) }
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
                if let heard = heard(state) {
                    Text(heard).font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
    }

    /// heard says how the test of the microphone goes, or what it heard.
    private func heard(_ state: RecordingState) -> String? {
        if !state.testing { return state.heard.isEmpty ? nil : state.heard }
        if state.starting { return L.recSttMicStarting }
        if state.active {
            return state.liveText.isEmpty ? L.recSttListening(seconds: 4) : state.liveText
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
