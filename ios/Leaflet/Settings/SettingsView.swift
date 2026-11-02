import SwiftUI

/// SettingsView is Settings: how Leaflet looks, and how sync stands.
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
        // Read here, for the body to follow it as it is typed: the alert's
        // actions aren't drawn where a read of it would be.
        let wordTyped = prompt.question?.typed.map { prompt.typed == $0 } ?? true
        NavigationStack {
            Form {
                Group {
                    AccountSection()

                    Section {
                        Picker(L.settingsTheme, selection: $app.theme) {
                            ForEach(ThemeMode.allCases) { Text($0.label).tag($0) }
                        }
                        // As the desktop's: the system's language, then each of
                        // the catalogs' by its name in itself.
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
                                    // Drawn in its own color: a picker tints
                                    // its rows' symbols with the app's accent.
                                    Image(
                                        uiImage: AccentDot.image(
                                            Palette.palettes[name]!["light"]!.accent))
                                }
                                // Blue, the system's, is the default.
                                .tag(name == "Blue" ? "" : name)
                            }
                        }
                        .pickerStyle(.navigationLink)
                        // Its separator under the row's start, as the
                        // others': the chosen accent's label would set it
                        // under its own text.
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
            // The sections' questions and files, on the form: on a row, or
            // a section's header, they would go as the list scrolls it away.
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
        // Drawn again in a language chosen here.
        .id(app.shown)
    }
}

/// SettingsPrompt is what a section of Settings asks, or a file it offers
/// to move, which the form shows: a section's rows come and go as the
/// list scrolls, and what they present would go with them.
@MainActor
@Observable
final class SettingsPrompt {
    /// Ask is a question with an action besides Cancel, which typed, when
    /// set, is a word to type for, as DELETE.
    struct Ask {
        var title: String
        var message: String
        var action: String
        var typed: String?
        var confirm: @MainActor () async -> Void
    }

    /// Move offers a file the app made, to move where the user picks; done
    /// hears where it went, or why it didn't, and cancelled that it stays.
    struct Move {
        var file: URL
        var done: @MainActor (Result<URL, any Error>) -> Void
        var cancelled: @MainActor () -> Void
    }

    var asking = false
    private(set) var question: Ask?
    /// typed is what is typed for the question's word.
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

    /// moved tells the move's section how it ended, once: nil if the user
    /// kept the file where it was.
    func moved(_ result: Result<URL, any Error>?) {
        let m = move
        move = nil
        if let result { m?.done(result) } else { m?.cancelled() }
    }
}

/// SyncSection is Settings › Sync and storage, in one section as on the
/// desktop: iCloud sync, then while pages sync, how sync stands, and
/// working offline.
private struct SyncSection: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        Section(L.settingsSectionOffline) {
            ICloudRows()
            if app.syncs {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                    Text(status).font(.footnote).foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
                Button(L.settingsSyncNow) { Task { await app.syncNow() } }
                    .disabled(app.syncing || app.workOffline)
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

/// ConflictsSection is Settings › Sync and storage › Conflicts, as on the
/// desktop: each block two devices changed at once, its page and what was
/// kept, with a menu to settle it another way, or to open its page.
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
                        // A block one side deleted and the other edited was
                        // kept, and can be deleted again.
                        if cf.deleted != nil {
                            Button(L.settingsDeleteIt, role: .destructive) {
                                resolve(cf, .delete)
                            }
                            Button(L.settingsKeepIt) { resolve(cf, .keep) }
                        } else {
                            Button(L.settingsUseThisDevice) { resolve(cf, .thisDevice) }
                            Button(L.settingsKeepNewer) { resolve(cf, .keep) }
                        }
                        Button(L.settingsOpenPage) {
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

    /// desc says what was kept, as the desktop's row does.
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

/// SettingLabel is a setting's name, and what it does if any, as
/// LabeledContent's label shows them.
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

// LabeledContent puts what is too wide to fit beside its label on a line of
// its own, as on a phone: a field's text, aligned to the end, then stands
// loose in the row, and a button under the label's start. These rows keep
// it beside the label, which wraps.

/// FieldRow is a setting typed in, the field beside its label, narrowing
/// down to minWidth to keep there.
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
                // At the row's end, as wide as the row is: narrower than
                // the room left, it would stop short of it.
                .frame(maxWidth: .infinity, alignment: .trailing)
        }
    }
}

/// ActionRow is a setting done with a button, or what shows in its place,
/// at the row's end.
struct ActionRow<Action: View>: View {
    let label: String
    var desc: String?
    @ViewBuilder let action: Action

    var body: some View {
        HStack {
            SettingLabel(label: label, desc: desc)
                .frame(maxWidth: .infinity, alignment: .leading)
            // Borderless, for a tap on the row away from the button to
            // leave it be.
            action.buttonStyle(.borderless).fixedSize()
        }
    }
}

/// AccentDot draws an accent's swatch as a bitmap of its own color: a
/// picker shows symbols, and images it may template, in one color.
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
