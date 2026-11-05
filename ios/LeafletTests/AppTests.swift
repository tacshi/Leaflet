import DeviceKit
import Foundation
import Security
import Testing

@testable import Leaflet

/// AppTests cover the app's own state, apart from the core: a window's,
/// the order of the core's answers, and the device's name.
@MainActor
struct AppTests {
    /// A model server asks for no key, unless it keeps one: the core has
    /// that typed again as the server moves, so the form must take it.
    @Test func aModelServerThatKeepsAKeyAsksForIt() {
        let preset = ProviderPreset(
            id: "lmstudio", name: "LM Studio", baseURL: "http://localhost:1234/v1", auth: "none",
            group: "Local", local: true, suggested: nil, placeholder: "")
        var draft = ProviderForm.Draft(preset: preset)
        #expect(!ProviderForm.takesKey(draft))
        draft.provider.hasKey = true
        #expect(ProviderForm.takesKey(draft))
    }

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

    /// A page the window still shows stays open as its view goes, for the
    /// view it moves to: it closes once the window shows another.
    @Test func aPageTheWindowShowsStaysOpenAsItsViewGoes() async throws {
        let host = try PageTests.Host()
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let scene = SceneModel()
        scene.open(page.id)
        let model = PageModel(id: page.id, app: host)
        await model.open()
        scene.release(model)
        try await Task.sleep(for: .milliseconds(50))
        #expect(!model.closed)
        scene.open(nil)
        for _ in 0..<100 where !model.closed { try await Task.sleep(for: .milliseconds(10)) }
        #expect(model.closed)
    }

    /// A window closing closes the page it showed, whose view's task ends
    /// after the window's views have gone: held then, nothing would close
    /// it, and the core would keep an editor nobody has.
    @Test func aWindowClosingClosesThePageItShowed() async throws {
        let host = try PageTests.Host()
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let scene = SceneModel()
        scene.open(page.id)
        let model = PageModel(id: page.id, app: host)
        await model.open()
        scene.closeHeld()
        scene.release(model)
        for _ in 0..<100 where !model.closed { try await Task.sleep(for: .milliseconds(10)) }
        #expect(model.closed)

        // Back again, a page it shows stays open as its view goes.
        scene.reopened()
        let again = PageModel(id: page.id, app: host)
        await again.open()
        scene.release(again)
        try await Task.sleep(for: .milliseconds(50))
        #expect(!again.closed)
        scene.closeHeld()
        for _ in 0..<100 where !again.closed { try await Task.sleep(for: .milliseconds(10)) }
        #expect(again.closed)
    }

    /// A file put in a page goes in through the editor only while the
    /// window shows that page: copying one takes a while, and the window
    /// may have moved on.
    @Test func aWindowGivesTheEditorOfThePageItShowsAlone() throws {
        let host = try PageTests.Host()
        let model = PageModel(id: "a", app: host)
        let editor = EditorController(
            style: EditorStyle(textSize: 17, palette: Palette.palettes["Indigo"]!["light"]!))
        let scene = SceneModel()
        scene.page = model
        scene.editor = editor
        #expect(scene.editor(of: "a") === editor)
        #expect(scene.editor(of: "b") == nil)
        scene.letGo(model)
        #expect(scene.editor(of: "a") == nil)
    }

    /// Only the latest time the app went to the background stops sync: a
    /// stop cuts short the pass running, which may be the later one's.
    @Test func onlyTheLatestLeavingStopsSync() {
        var leavings = Leavings()
        let first = leavings.begin()
        #expect(leavings.isLatest(first))
        let second = leavings.begin()
        #expect(!leavings.isLatest(first))
        #expect(leavings.isLatest(second))
    }

    /// A background pass out of time stops sync, unless a window came to
    /// the front meanwhile, which sync goes on for.
    @Test func aPassOutOfTimeLeavesAWindowItsSync() async {
        var stops = 0
        await BackgroundSync.outOfTime(inFront: { true }, stop: { stops += 1 })
        #expect(stops == 0)
        await BackgroundSync.outOfTime(inFront: { false }, stop: { stops += 1 })
        #expect(stops == 1)
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
