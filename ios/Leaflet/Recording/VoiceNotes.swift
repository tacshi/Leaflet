import AVFoundation
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

    /// toggling is the last press's run: each waits for the one before,
    /// so two presses close together never both start a voice note, nor
    /// make its folder twice; the second stops the first.
    private static var toggling: Task<Void, any Error>?

    /// toggle stops the recording in progress, or starts a voice note.
    static func toggle(_ intent: RecordVoiceNoteIntent) async throws {
        let before = toggling
        let run = Task {
            _ = await before?.result
            try await toggleNow(intent)
        }
        toggling = run
        try await withTaskCancellationHandler {
            try await run.value
        } onCancel: {
            run.cancel()
        }
    }

    private static func toggleNow(_ intent: RecordVoiceNoteIntent) async throws {
        guard let app, let client = await app.opened() else { throw Failure(LI.coreFailed) }
        await Recorder.shared.ready()
        let now = try await client.recordingState()
        if now.recording, !now.testing {
            app.voiceNote = nil
            await Recorder.shared.stopRecording()
            return
        }
        // A test of the microphone, from Settings, gives way to it.
        if now.testing { await client.stopRecording() }
        // Shown first: iOS stops a recording from the background that has
        // none. Where it refuses one, or the microphone is still to be
        // allowed, which only the app in front can ask, Leaflet comes to
        // the front, where a recording needs none, and shows it there.
        if !inFront,
            AVAudioApplication.shared.recordPermission == .undetermined
                || !RecordingActivity.shared.start()
        {
            try await intent.continueInForeground(nil, alwaysConfirm: false)
        }
        RecordingActivity.shared.start()
        // What plays stops, as for a recording in a page.
        Recorder.shared.stopPlaying()
        let page: String
        do {
            page = try await client.recordNewPage(folderID: await folder(app, client))
            // Still starting, its page isn't made yet: the Live Activity
            // shows it meanwhile.
            guard try await started(page) else { return }
        } catch {
            // Unless a recording runs all the same, as one started in the
            // app meanwhile.
            if (try? await client.recordingState())?.recording != true {
                RecordingActivity.shared.end()
            }
            throw error
        }
        // Once the library has it, as a window drops pages it hasn't.
        await app.reload(.library)
        app.voiceNote = page
    }

    static func stop() async {
        guard let app, await app.opened() != nil else { return }
        app.voiceNote = nil
        await Recorder.shared.stopRecording()
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
        if !app.libraryLoaded { await app.reload(.library) }
        // Unread, the library can't tell a folder deleted from one it
        // hasn't read: the one chosen stays, and none is made.
        guard app.libraryLoaded else { return saved }
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

    /// started waits for the microphone of the voice note for page to come
    /// on, which makes the page, and throws why it didn't: it comes on
    /// after the recording starts, once iOS lets it. Still starting after
    /// a while, it returns false.
    private static func started(_ page: String) async throws -> Bool {
        // The recorder hears each change of the recording as the core says
        // it; the page's are the ones for this voice note.
        let watch = Task { () throws -> Bool in
            for await s in Observations({ Recorder.shared.state }) where s.pageID == page {
                if s.active { return true }
                if !s.starting {
                    throw Failure(s.error.isEmpty ? LI.voiceNoteNotStarted : s.error)
                }
            }
            return false
        }
        let timeout = Task {
            try? await Task.sleep(for: .seconds(10))
            watch.cancel()
        }
        defer { timeout.cancel() }
        return try await withTaskCancellationHandler {
            try await watch.value
        } onCancel: {
            watch.cancel()
        }
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
