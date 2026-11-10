import SwiftUI
import WidgetKit

@main
struct LeafletWidgets: WidgetBundle {
    var body: some Widget {
        RecordingLiveActivity()
        RecordVoiceNoteControl()
    }
}

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
        .description(RecordVoiceNoteIntent.description.descriptionText)
    }
}
