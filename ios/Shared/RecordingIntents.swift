import AppIntents
import Foundation

// The intents the Action Button, Control Center, Siri and Shortcuts run,
// and the Live Activity's buttons. The widgets have them too, to name them
// as their actions; the app performs them, in its own process, where the
// microphone and the core are: WIDGETS is set in the widgets alone.

/// RecordVoiceNoteIntent starts a voice note, a recording in a page of its
/// own, or stops the one recording: one press of the Action Button
/// starts, the next stops.
struct RecordVoiceNoteIntent: AudioRecordingIntent {
    static let title = LocalizedStringResource(
        "Record Voice Note", table: "Recording",
        comment: "The Action Button, Control Center and Shortcuts: start or stop a voice note.")
    /// description says what it does, in Shortcuts and in Control Center's
    /// list of controls.
    static let description = IntentDescription(
        LocalizedStringResource(
            "Records a voice note in a new page, or stops the one recording.",
            table: "Recording", comment: "Shortcuts: what Record Voice Note does."))
    /// It records from the background, with the Live Activity showing it;
    /// where iOS won't let it, it brings Leaflet to the front and records
    /// there.
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

/// StopRecordingIntent is the Live Activity's Stop.
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

/// PauseRecordingIntent is the Live Activity's Pause, and Resume while it
/// is paused.
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
    /// appOnly fails an intent the widgets were given to perform, which
    /// only the app can: done quietly, a press would seem to have worked.
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
