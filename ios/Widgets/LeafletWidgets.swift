import SwiftUI
import WidgetKit

/// LeafletWidgets are what Leaflet shows outside the app: a recording's
/// Live Activity, and Record Voice Note for Control Center, the Lock
/// Screen and the Action Button.
@main
struct LeafletWidgets: WidgetBundle {
    var body: some Widget {
        RecordingLiveActivity()
        RecordVoiceNoteControl()
    }
}

/// RecordVoiceNoteControl starts a voice note, or stops the one recording,
/// as the app's Record Voice Note does: the app performs it.
struct RecordVoiceNoteControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: "RecordVoiceNote") {
            ControlWidgetButton(action: RecordVoiceNoteIntent()) {
                Label {
                    Text(
                        "Voice Note", tableName: "Recording",
                        comment:
                            "Control Center, the Lock Screen and the Action Button: the control that records a voice note."
                    )
                } icon: {
                    Image(systemName: "mic")
                }
            }
        }
        .displayName(RecordVoiceNoteIntent.title)
        .description(
            LocalizedStringResource(
                "Records a voice note in a new page, or stops the one recording.",
                table: "Recording", comment: "Shortcuts: what Record Voice Note does."))
    }
}
