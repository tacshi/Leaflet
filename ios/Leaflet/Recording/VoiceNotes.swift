import AppIntents
import Foundation
import UIKit

/// VoiceNotes are recordings started from outside the app: the Action
/// Button, Control Center, Siri and Shortcuts run Record Voice Note,
/// which records in a page of its own, in the folder Settings › Speech
/// to text names, and the Live Activity's buttons pause and stop it.
@MainActor
enum VoiceNotes {
    /// app is the app's model, which the intents act through: it sets
    /// itself here as it starts, before any intent runs.
    static weak var app: AppModel?

    /// folderKey is where the folder voice notes go in is kept: a folder's
    /// ID, topLevel for none, or nothing for the Voice Notes folder, made
    /// as the first voice note needs it.
    static let folderKey = "voiceNoteFolder"
    static let topLevel = "-"

    /// toggle stops the recording in progress, or starts a voice note.
    static func toggle(_ intent: RecordVoiceNoteIntent) async throws {
        guard let app, let client = await app.opened() else { throw Failure(LI.coreFailed) }
        await Recorder.shared.ready()
        let now = try await client.recordingState()
        if now.recording, !now.testing {
            app.voiceNote = nil
            await client.stopRecording()
            return
        }
        // Shown first: iOS stops a recording from the background that has
        // none. Where it refuses one, Leaflet comes to the front, where a
        // recording needs none, and shows it from there.
        if !RecordingActivity.shared.start(), !inFront {
            try await intent.continueInForeground(nil, alwaysConfirm: false)
            RecordingActivity.shared.start()
        }
        let page: PageRow
        do {
            page = try await client.recordNewPage(folderID: await folder(app, client))
            try await started(client)
        } catch {
            RecordingActivity.shared.end()
            throw error
        }
        // Once the library has it, as a window drops pages it hasn't.
        await app.reload()
        app.voiceNote = page.id
    }

    static func stop() async {
        guard let app, let client = await app.opened() else { return }
        app.voiceNote = nil
        await client.stopRecording()
    }

    static func togglePause() async {
        guard let client = await app?.opened() else { return }
        await client.togglePauseRecording()
    }

    /// inFront says the app is in front, where a recording needs no Live
    /// Activity to go on.
    private static var inFront: Bool { UIApplication.shared.applicationState == .active }

    /// folder returns the folder a voice note goes in, as Settings names
    /// it, or "" for the top level: the Voice Notes folder unless another
    /// is chosen, made as the first voice note needs it, or again once
    /// deleted.
    static func folder(_ app: AppModel, _ client: CoreClient) async -> String {
        let saved = UserDefaults.standard.string(forKey: folderKey) ?? ""
        if saved == topLevel { return "" }
        if !app.libraryLoaded { await app.reload() }
        let folders = app.library.folders
        if folders.contains(where: { $0.id == saved }) { return saved }
        let name = LI.voiceNotesFolder
        let id: String
        if let f = folders.first(where: { ($0.parentID ?? "").isEmpty && $0.name == name }) {
            id = f.id
        } else if let f = try? await client.createFolder(name) {
            id = f.id
        } else {
            return ""
        }
        UserDefaults.standard.set(id, forKey: folderKey)
        return id
    }

    /// started waits for the microphone to come on, and throws why it
    /// didn't: it comes on after the recording starts, once iOS lets it.
    private static func started(_ client: CoreClient) async throws {
        let deadline = ContinuousClock.now + .seconds(10)
        while ContinuousClock.now < deadline {
            let s = try await client.recordingState()
            if s.active { return }
            if !s.starting { throw Failure(s.error.isEmpty ? LI.voiceNoteNotStarted : s.error) }
            try await Task.sleep(for: .milliseconds(100))
        }
        // Still starting: the Live Activity shows it, and the page says
        // why, should it never start.
    }

    /// Failure is why a voice note couldn't start, as Shortcuts and Siri
    /// say it.
    struct Failure: LocalizedError {
        let message: String
        init(_ message: String) { self.message = message }
        var errorDescription: String? { message }
    }
}

/// LeafletShortcuts are the intents iOS offers without the user making a
/// shortcut: for the Action Button, Siri and Spotlight.
struct LeafletShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: RecordVoiceNoteIntent(),
            phrases: [
                "Record a voice note in \(.applicationName)",
                "Start a \(.applicationName) voice note",
                "\(.applicationName) voice note",
            ],
            shortTitle: LocalizedStringResource(
                "Record Voice Note", table: "Recording",
                comment: "The Action Button and Shortcuts: start or stop a voice note."),
            systemImageName: "mic")
    }
}
