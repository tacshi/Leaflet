import AppIntents
import Foundation

struct RecordVoiceNoteIntent: AudioRecordingIntent {
    static let title = LocalizedStringResource(
        "Record Voice Note", table: "Recording",
        comment: "The Action Button, Control Center and Shortcuts: start or stop a voice note.")
    static let description = IntentDescription(
        LocalizedStringResource(
            "Records a voice note in a new page, or stops the one recording.",
            table: "Recording", comment: "Shortcuts: what Record Voice Note does."))
    static let supportedModes: IntentModes = [.background, .foreground(.dynamic)]

    func perform() async throws -> some IntentResult {
        #if WIDGETS
            try appOnly()
        #else
            try await VoiceNotes.toggle(self)
        #endif
        return .result()
    }
}

struct StopRecordingIntent: LiveActivityIntent {
    static let title = LocalizedStringResource(
        "Stop Recording", table: "Recording",
        comment: "A recording's Live Activity: stop the recording.")
    static let isDiscoverable = false

    func perform() async throws -> some IntentResult {
        #if WIDGETS
            try appOnly()
        #else
            await VoiceNotes.stop()
        #endif
        return .result()
    }
}

struct PauseRecordingIntent: LiveActivityIntent {
    static let title = LocalizedStringResource(
        "Pause or Resume Recording", table: "Recording",
        comment: "A recording's Live Activity: pause the recording, or go on with it.")
    static let isDiscoverable = false

    func perform() async throws -> some IntentResult {
        #if WIDGETS
            try appOnly()
        #else
            await VoiceNotes.togglePause()
        #endif
        return .result()
    }
}

#if WIDGETS
    private func appOnly() throws { throw AppOnly() }

    private struct AppOnly: LocalizedError {
        var errorDescription: String? {
            String(
                localized: LocalizedStringResource(
                    "Open Leaflet to record a voice note.", table: "Recording",
                    comment:
                        "The Action Button, Control Center or a Live Activity's button, should iOS run it outside the app, which records."
                ))
        }
    }
#endif
