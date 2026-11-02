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
