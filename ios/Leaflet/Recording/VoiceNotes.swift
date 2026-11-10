import AVFoundation
import AppIntents
import Foundation
import UIKit

@MainActor
enum VoiceNotes {
    static weak var app: AppModel?

    static let folderKey = "voiceNoteFolder"
    static let topLevel = "-"

    private static var toggling: Task<Void, any Error>?

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
        if now.testing { await client.stopRecording() }
        if !inFront,
            AVAudioApplication.shared.recordPermission == .undetermined
                || !RecordingActivity.shared.start()
        {
            try await intent.continueInForeground(nil, alwaysConfirm: false)
        }
        RecordingActivity.shared.start()
        Recorder.shared.quiet()
        let page: String
        do {
            page = try await client.recordNewPage(folderID: await folder(app, client))
            guard try await started(page) else { return }
        } catch {
            if (try? await client.recordingState())?.recording != true {
                RecordingActivity.shared.end()
            }
            throw error
        }
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

    private static var inFront: Bool { UIApplication.shared.applicationState == .active }

    static func folder(_ app: AppModel, _ client: CoreClient) async -> String {
        let saved = UserDefaults.standard.string(forKey: folderKey) ?? ""
        if saved == topLevel { return "" }
        if !app.libraryLoaded { await app.reload(.library) }
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

    private static func started(_ page: String) async throws -> Bool {
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

    struct Failure: LocalizedError {
        let message: String
        init(_ message: String) { self.message = message }
        var errorDescription: String? { message }
    }
}

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
