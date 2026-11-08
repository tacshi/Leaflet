import Foundation

/// LI says the messages only the iOS app has, from
/// Resources/Localizable.xcstrings; those it shares with the desktop come
/// from L.
enum LI {
    static var openInNewWindow: String {
        String(
            localized: "Open in New Window",
            bundle: L.bundle, comment: "A page's menu: open it in a window of its own.")
    }
    static var showInspector: String {
        String(
            localized: "Show Panel",
            bundle: L.bundle,
            comment: "A page's toolbar: show the Agent, Comments and Outline panel.")
    }
    static var outlineCollapse: String {
        String(
            localized: "Collapse Heading",
            bundle: L.bundle, comment: "The outline: hide the headings under this one.")
    }
    static var outlineExpand: String {
        String(
            localized: "Expand Heading",
            bundle: L.bundle, comment: "The outline: show the headings under this one.")
    }
    static var showSubpages: String {
        String(
            localized: "Show Subpages", bundle: L.bundle,
            comment: "The sidebar: show the pages inside this one.")
    }
    static var hideSubpages: String {
        String(
            localized: "Hide Subpages", bundle: L.bundle,
            comment: "The sidebar: hide the pages inside this one.")
    }
    static var showTaggedPages: String {
        String(
            localized: "Show Tagged Pages", bundle: L.bundle,
            comment: "The sidebar's Tags tab: show the pages with this tag.")
    }
    static var hideTaggedPages: String {
        String(
            localized: "Hide Tagged Pages", bundle: L.bundle,
            comment: "The sidebar's Tags tab: hide the pages with this tag.")
    }
    static var coreFailed: String {
        String(
            localized: "Leaflet couldn't open its data",
            bundle: L.bundle, comment: "Shown when the app's database can't open.")
    }
    static var signInFromDesktop: String {
        String(
            localized:
                "Set up sync in Leaflet on your Mac or PC, then sign in here with the same Supabase account.",
            bundle: L.bundle,
            comment: "Signing in: the phone uses the Supabase project the desktop app set up.")
    }
    static var indent: String {
        String(
            localized: "Indent", bundle: L.bundle,
            comment: "The editor: nest the block in the one before it.")
    }
    static var outdent: String {
        String(
            localized: "Outdent", bundle: L.bundle,
            comment: "The editor: take the block out a level.")
    }
    static var strikethrough: String {
        String(
            localized: "Strikethrough", bundle: L.bundle,
            comment: "The editor: strike the selected text through.")
    }
    static var underline: String {
        String(
            localized: "Underline", bundle: L.bundle,
            comment: "The editor: underline the selected text.")
    }
    static var moveBlockUp: String {
        String(
            localized: "Move Block Up",
            bundle: L.bundle, comment: "The editor: move the block before the one above it.")
    }
    static var moveBlockDown: String {
        String(
            localized: "Move Block Down",
            bundle: L.bundle, comment: "The editor: move the block after the one below it.")
    }
    static var turnInto: String {
        String(
            localized: "Turn Into", bundle: L.bundle,
            comment: "The editor: change the block to another kind.")
    }
    static var insert: String {
        String(
            localized: "Insert", bundle: L.bundle,
            comment: "The editor's keyboard bar: insert a block.")
    }
    static var hideKeyboard: String {
        String(
            localized: "Hide Keyboard", bundle: L.bundle,
            comment: "The editor's keyboard bar: put the keyboard away."
        )
    }
    static var removeLink: String {
        String(
            localized: "Remove Link", bundle: L.bundle,
            comment: "The link editor: take the link off the text.")
    }
    static var newWindow: String {
        String(
            localized: "New Window", bundle: L.bundle,
            comment: "The File menu: open another window of the app.")
    }
    static var goMenu: String {
        String(
            localized: "Go", bundle: L.bundle,
            comment: "The menu bar: the menu of Back and Forward.")
    }
    static var microphoneDenied: String {
        String(
            localized:
                "Leaflet isn't allowed to use it. Turn it on in Settings › Privacy & Security › Microphone.",
            bundle: L.bundle,
            comment:
                "Why a recording could not start: the user has not let the app use the microphone. Follows 'The microphone did not start: '."
        )
    }
    static var agentOptions: String {
        String(
            localized: "Agent Options", bundle: L.bundle,
            comment:
                "The Agent panel: the menu of the agent to ask, a new conversation, and the agent providers."
        )
    }
    static var resumeRecording: String {
        String(
            localized: "Resume", bundle: L.bundle,
            comment: "A recording's pill: go on recording after Pause.")
    }
    static var microphoneInCall: String {
        String(
            localized: "a call is using it. Record once the call ends.", bundle: L.bundle,
            comment:
                "Why a recording could not start: a phone or FaceTime call holds the microphone. Follows 'The microphone did not start: '."
        )
    }
    static var microphoneTaken: String {
        String(
            localized: "another app or a call is using it.", bundle: L.bundle,
            comment:
                "Why a recording paused: a call, Siri or another app's audio took the microphone. Follows 'The microphone stopped: '; the recording goes on by itself when it is given back."
        )
    }
    static var microphoneInBackground: String {
        String(
            localized:
                "iOS lets it start again only while Leaflet is open. Open Leaflet, then resume.",
            bundle: L.bundle,
            comment:
                "Why a recording could not go on: iOS refuses to start the microphone again from the background. Follows 'The microphone stopped: '."
        )
    }
    static var markDone: String {
        String(
            localized: "Mark as Done", bundle: L.bundle,
            comment: "VoiceOver's action on a to-do in the editor: check it.")
    }
    static var markNotDone: String {
        String(
            localized: "Mark as Not Done", bundle: L.bundle,
            comment: "VoiceOver's action on a checked to-do in the editor: uncheck it.")
    }
    static var expand: String {
        String(
            localized: "Expand", bundle: L.bundle,
            comment: "VoiceOver's action on a closed toggle in the editor: show what is inside it.")
    }
    static var collapse: String {
        String(
            localized: "Collapse", bundle: L.bundle,
            comment:
                "VoiceOver's action on an open toggle in the editor: fold away what is inside it.")
    }
    static var openLink: String {
        String(
            localized: "Open Link", bundle: L.bundle,
            comment: "VoiceOver's action on a link in the editor's text: open it.")
    }
    static var syncFailed: String {
        String(
            localized: "Last sync failed", bundle: L.bundle,
            comment:
                "The sidebar's foot: the last sync pass failed; pulling the list down tries again."
        )
    }
    static var agentNotStarted: String {
        String(
            localized: "The agent didn't start. Try again.", bundle: L.bundle,
            comment: "Asking an agent from the editor: the run could not start.")
    }
    static func removeTag(_ tag: String) -> String {
        String(
            localized: "Remove \(tag)", bundle: L.bundle,
            comment:
                "The tag editor: VoiceOver's name of the button that takes the tag (%@) off the page."
        )
    }
    static var voiceNotesFolder: String {
        String(
            localized: "Voice Notes", bundle: L.bundle,
            comment:
                "The folder voice notes go in, made as the first one is recorded from the Action Button, Control Center or Siri."
        )
    }
    static var voiceNoteNotStarted: String {
        String(
            localized: "The recording didn't start. Try again.", bundle: L.bundle,
            comment:
                "Record Voice Note, from the Action Button or Siri: the microphone never came on.")
    }
    static var voiceNotesGoIn: String {
        String(
            localized: "Voice Notes Go In", bundle: L.bundle,
            comment:
                "Settings › Speech to text: the folder that voice notes recorded from the Action Button, Control Center or Siri go in."
        )
    }
    static var voiceNotesHint: String {
        String(
            localized:
                "Record Voice Note records in a page of its own. Put it on the Action Button in Settings › Action Button, or add it to Control Center or the Lock Screen.",
            bundle: L.bundle,
            comment:
                "Settings › Speech to text, under Voice Notes Go In: where to find Leaflet's Record Voice Note control."
        )
    }
}
