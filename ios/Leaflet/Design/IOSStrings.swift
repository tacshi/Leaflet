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
    static var appearance: String {
        String(
            localized: "Appearance",
            bundle: L.bundle, comment: "Settings: the section of the theme, accent and text size.")
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
                "Leaflet can't use the microphone. Turn it on in Settings › Privacy & Security › Microphone.",
            bundle: L.bundle,
            comment: "A recording could not start: the user has not let the app use the microphone."
        )
    }
    static var microphoneInCall: String {
        String(
            localized: "a call is using it. Record once the call ends.", bundle: L.bundle,
            comment:
                "Why a recording could not start: a phone or FaceTime call holds the microphone. Follows 'The microphone did not start: '."
        )
    }
}
