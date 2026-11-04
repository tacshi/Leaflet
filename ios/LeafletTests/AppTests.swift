import DeviceKit
import Foundation
import Security
import Testing

@testable import Leaflet

/// AppTests cover the app's own state, apart from the core: a window's,
/// the order of the core's answers, and the device's name.
@MainActor
struct AppTests {
    @Test func openingAnotherPageLetsGoOfWhatWasStartedOnTheLast() {
        let scene = SceneModel()
        scene.open("a")
        scene.newComment = NewComment(blockID: "b1", quote: "Milk")
        scene.asking = AskRequest(focus: ["b1"], selection: "Milk", agent: nil)
        scene.open("b")
        #expect(scene.newComment == nil)
        #expect(scene.asking == nil)

        scene.newComment = NewComment(blockID: "b2", quote: "")
        scene.goBack()
        #expect(scene.selection == "a")
        #expect(scene.newComment == nil)
    }

    @Test func withNoPageThePanelShortcutShowsTheWorkspaceChat() {
        let scene = SceneModel()
        scene.inspectorTab = .comments
        scene.inspectorShown = true
        scene.showPanel(.agent)
        #expect(!scene.inspectorShown)
        scene.showPanel(.comments)
        #expect(!scene.inspectorShown)
        scene.showPanel(.agent)
        #expect(scene.inspectorShown)

        // Stacked, as on iPhone, no panel is beside a page: the chat is the
        // list's sheet.
        scene.inspectorShown = false
        scene.stacked = true
        scene.showPanel(.agent)
        #expect(scene.workspaceShown && !scene.inspectorShown)
    }

    @Test func aPageOpenedFromTheWorkspaceChatOpensBesideIt() {
        let scene = SceneModel()
        scene.askWorkspace(sheet: true)
        #expect(scene.workspaceShown && scene.allPages && scene.focusChat)
        scene.allPages = false
        scene.inspectorShown = true
        scene.inspectorTab = .comments
        scene.openFromChat("made", workspace: true, sheet: true)
        #expect(scene.selection == "made")
        #expect(scene.allPages && scene.inspectorTab == .agent)
        #expect(!scene.workspaceShown && !scene.inspectorShown)

        // A page's own chat, beside the page, stays as it was.
        scene.allPages = false
        scene.inspectorShown = true
        scene.openFromChat("other", at: "b1", moment: 83, workspace: false, sheet: false)
        #expect(scene.selection == "other" && scene.inspectorShown && !scene.allPages)
        #expect(scene.target == "b1" && scene.targetAt == 83)
    }

    @Test func aRecordingPageRecordsOnlyAsItOpens() {
        let scene = SceneModel()
        scene.recordOnOpen = "rec"
        scene.open("rec")
        #expect(scene.recordOnOpen == "rec")
        scene.open("other")
        #expect(scene.recordOnOpen == nil)
    }

    @Test func anOlderAnswerDoesNotReplaceANewerOne() {
        var asked = Newest()
        let first = asked.ask("agents")
        let second = asked.ask("agents")
        let keptSecond = asked.answered(second, "agents")
        let keptFirst = asked.answered(first, "agents")
        #expect(keptSecond)
        #expect(!keptFirst)
        // Each kind of read is numbered apart.
        let threads = asked.ask("threads")
        let keptThreads = asked.answered(threads, "threads")
        #expect(keptThreads)
    }

    @Test func answersAskedFasterThanTheyComeStillShow() {
        var asked = Newest()
        let first = asked.ask("agents")
        let second = asked.ask("agents")
        let keptFirst = asked.answered(first, "agents")
        let keptSecond = asked.answered(second, "agents")
        #expect(keptFirst)
        #expect(keptSecond)
    }

    /// The core reads the status off the end of a keychain error, to tell
    /// a prompt turned down from a device not unlocked yet.
    @Test func keychainErrorsEndWithTheirStatus() {
        for status in [errSecUserCanceled, errSecAuthFailed, errSecInteractionNotAllowed, -1] {
            let text = Keychain.Failure(status: status).localizedDescription
            #expect(text.hasSuffix(" (OSStatus \(status))"), "\(text)")
        }
    }

    @Test func theDeviceGoesByItsModel() {
        #expect(PlatformBridge.label(of: .iPadPro13M5) == "iPad Pro (13-inch) (M5)")
        #expect(PlatformBridge.label(of: .simulator(.iPhone17)) == "Simulator (iPhone 17)")
        // One newer than DeviceKit knows, by its kind.
        #expect(PlatformBridge.label(of: .unknown("iPad99,1")) == "iPad")
        #expect(!PlatformBridge.label(of: DeviceKit.Device.current).isEmpty)
    }
}
