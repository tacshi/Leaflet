import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.palette) private var palette
    @Environment(\.dismiss) private var dismiss
    @State private var editingProvider: ProviderForm.Draft?
    @State private var providersVersion = 0
    @State private var prompt = SettingsPrompt()

    var body: some View {
        @Bindable var app = app
        @Bindable var prompt = prompt
        @Bindable var iCloud = app.iCloud
        let wordTyped = prompt.question?.typed.map { prompt.typed == $0 } ?? true
        NavigationStack {
            Form {
                Group {
                    AccountSection()

                    Section {
                        Picker(L.settingsTheme, selection: $app.theme) {
                            ForEach(ThemeMode.allCases) { Text($0.label).tag($0) }
                        }
                        Picker(
                            L.settingsLanguage,
                            selection: Binding(
                                get: { app.language }, set: { app.setLanguage($0) })
                        ) {
                            Text(L.settingsLanguageSystem).tag("")
                            ForEach(L.languages, id: \.tag) { Text($0.name).tag($0.tag) }
                        }
                        Picker(L.settingsAccent, selection: $app.accent) {
                            ForEach(Palette.accents, id: \.self) { name in
                                Label {
                                    Text(L.settingsAccentName(name: name))
                                } icon: {
                                    Image(
                                        uiImage: AccentDot.image(
                                            Palette.palettes[name]!["light"]!.accent))
                                }
                                .tag(name == "Blue" ? "" : name)
                            }
                        }
                        .pickerStyle(.navigationLink)
                        .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
                        Picker(L.settingsTextSize, selection: $app.textSize) {
                            Text(L.settingsTextSizeSmall).tag(15.0)
                            Text(L.settingsTextSizeDefault).tag(17.0)
                            Text(L.settingsTextSizeLarge).tag(19.0)
                            Text(L.settingsTextSizeLarger).tag(21.0)
                        }
                    } header: {
                        Text(L.settingsSectionAppearance)
                    } footer: {
                        Text(L.settingsAppearanceDesc)
                    }

                    ProvidersSection(editing: $editingProvider, version: providersVersion)

                    SpeechSection()

                    SyncSection()

                    if !app.conflicts.isEmpty {
                        ConflictsSection()
                    }

                    if app.status?.signedIn ?? false {
                        DevicesSection()
                    }

                    DataSection(signedIn: app.status?.signedIn ?? false) {
                        await app.reload()
                    }
                }
                .listRowBackground(palette.surface)
            }
            .themedForm(palette)
            .alert(
                prompt.question?.title ?? "", isPresented: $prompt.asking,
                presenting: prompt.question
            ) { q in
                if let word = q.typed {
                    TextField(word, text: $prompt.typed)
                        .textInputAutocapitalization(.characters)
                        .autocorrectionDisabled()
                }
                Button(L.commonCancel, role: .cancel) {}
                Button(q.action, role: .destructive) { Task { await q.confirm() } }
                    .disabled(!wordTyped)
            } message: { q in
                Text(q.message)
            }
            .fileMover(isPresented: $prompt.moving, file: prompt.move?.file) {
                prompt.moved($0)
            } onCancellation: {
                prompt.moved(nil)
            }
            .sheet(isPresented: $iCloud.picking) { ICloudRows.picker(app) }
            .navigationTitle(L.settingsTitle)
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(item: $editingProvider) { draft in
                ProviderForm(draft: draft) { providersVersion += 1 }
                    .themedForm(palette)
            }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(L.commonDone) { dismiss() }
                }
            }
            .onChange(of: app.theme) { app.saveLook() }
            .onChange(of: app.accent) { app.saveLook() }
            .onChange(of: app.textSize) { app.saveLook() }
        }
        .environment(prompt)
        .id(app.shown)
    }
}

@MainActor
@Observable
final class SettingsPrompt {
    struct Ask {
        var title: String
        var message: String
        var action: String
        var typed: String?
        var confirm: @MainActor () async -> Void
    }

    struct Move {
        var file: URL
        var done: @MainActor (Result<URL, any Error>) -> Void
        var cancelled: @MainActor () -> Void
    }

    var asking = false
    private(set) var question: Ask?
    var typed = ""
    var moving = false
    private(set) var move: Move?

    func ask(_ question: Ask) {
        self.question = question
        typed = ""
        asking = true
    }

    func offer(_ move: Move) {
        self.move = move
        moving = true
    }

    func moved(_ result: Result<URL, any Error>?) {
        let m = move
        move = nil
        if let result { m?.done(result) } else { m?.cancelled() }
    }
}

private struct SyncSection: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        Section(L.settingsSectionOffline) {
            ICloudRows()
            if app.syncs {
                ActionRow(label: title, desc: status) {
                    Button(L.settingsSyncNow) { Task { await app.syncNow() } }
                        .disabled(app.syncing || app.workOffline)
                }
                Toggle(isOn: $app.workOffline) {
                    Text(L.settingsWorkOffline)
                    Text(L.settingsWorkOfflineDesc)
                }
                .onChange(of: app.workOffline) { _, on in
                    app.save(["work_offline": on])
                    if !on { Task { await app.client?.kick() } }
                }
                Toggle(isOn: $app.allPagesOffline) {
                    Text(L.settingsAllPagesOffline)
                    Text(L.settingsAllPagesOfflineDesc)
                }
                .onChange(of: app.allPagesOffline) { _, on in
                    app.save(["all_pages_offline": on])
                }
            }
        }
    }

    private var title: String {
        if let n = app.status?.pending, n > 0 { return L.settingsChangesWaiting(count: n) }
        return L.settingsSynced
    }

    private var status: String {
        if app.workOffline { return L.settingsOfflineByChoice }
        if app.syncing { return L.settingsSyncing }
        if let failure = app.syncFailure { return L.settingsSyncFailed(error: failure) }
        return L.settingsUpToDate
    }
}

private struct ConflictsSection: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.palette) private var palette
    @Environment(\.dismiss) private var dismiss
    @State private var failure: String?

    var body: some View {
        Section {
            ForEach(app.conflicts) { cf in
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(cf.pageTitle.isEmpty ? L.commonUntitled : cf.pageTitle)
                        Text(Self.desc(cf)).font(.footnote).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Menu(L.settingsResolve) {
                        if cf.deleted != nil {
                            Button(L.settingsDeleteIt, role: .destructive) {
                                resolve(cf, .delete)
                            }
                            Button(L.settingsKeepIt) { resolve(cf, .keep) }
                        } else {
                            if cf.keptHere {
                                Button(L.settingsUseOtherDevice) { resolve(cf, .otherDevice) }
                            } else {
                                Button(L.settingsUseThisDevice) { resolve(cf, .thisDevice) }
                            }
                            Button(L.settingsKeepNewer) { resolve(cf, .keep) }
                        }
                        Button(L.settingsOpenPage) {
                            guard app.opens(cf.pageID) else { return }
                            scene.open(cf.pageID)
                            dismiss()
                        }
                    }
                    .fixedSize()
                }
                .accessibilityElement(children: .contain)
            }
        } header: {
            Text(L.settingsConflicts)
        } footer: {
            if let failure { Text(failure).foregroundStyle(palette.dangerText) }
        }
    }

    static func desc(_ cf: Conflict) -> String {
        switch cf.deleted {
        case "here": L.settingsConflictDeletedHere(text: cf.kept)
        case "there": L.settingsConflictDeletedThere(text: cf.kept)
        default: L.settingsConflictKeptOver(kept: cf.kept, other: cf.other)
        }
    }

    private func resolve(_ cf: Conflict, _ how: Conflict.Resolution) {
        failure = nil
        Task {
            do {
                try await app.client?.resolveConflict(cf.id, how: how)
            } catch {
                failure = error.localizedDescription
            }
            await app.refreshConflicts()
        }
    }
}

struct SettingLabel: View {
    let label: String
    var desc: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
            if let desc { Text(desc).font(.subheadline).foregroundStyle(.secondary) }
        }
    }
}

struct FieldRow<Field: View>: View {
    let label: String
    var desc: String?
    var minWidth: CGFloat = 96
    var maxWidth: CGFloat = .infinity
    @ViewBuilder let field: Field

    var body: some View {
        HStack {
            SettingLabel(label: label, desc: desc).layoutPriority(1)
            field
                .multilineTextAlignment(.trailing)
                .frame(minWidth: minWidth, maxWidth: maxWidth)
                .frame(maxWidth: .infinity, alignment: .trailing)
        }
    }
}

struct ActionRow<Action: View>: View {
    let label: String
    var desc: String?
    @ViewBuilder let action: Action

    var body: some View {
        HStack {
            SettingLabel(label: label, desc: desc)
                .frame(maxWidth: .infinity, alignment: .leading)
            action.buttonStyle(.borderless).fixedSize()
        }
    }
}

enum AccentDot {
    static func image(_ color: Color) -> UIImage {
        let side: CGFloat = 18
        let image = UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { _ in
            UIColor(color).setFill()
            UIBezierPath(ovalIn: CGRect(x: 0, y: 0, width: side, height: side)).fill()
        }
        return image.withRenderingMode(.alwaysOriginal)
    }
}
