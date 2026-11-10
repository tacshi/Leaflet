import DeviceKit
import Foundation
import Security
import Testing

@testable import Leaflet

@MainActor
struct AppTests {
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

    @Test func onlyTheLatestLeavingStopsSync() {
        var leavings = Leavings()
        let first = leavings.begin()
        #expect(leavings.isLatest(first))
        let second = leavings.begin()
        #expect(!leavings.isLatest(first))
        #expect(leavings.isLatest(second))
    }

    @Test func agentsAtWorkAreWaitedForThenStoppedAsTimeRunsOut() {
        #expect(AgentHold.next(atWork: false, remaining: 30, stopping: false) == .end)
        #expect(AgentHold.next(atWork: true, remaining: 30, stopping: false) == .wait)
        #expect(
            AgentHold.next(atWork: true, remaining: .greatestFiniteMagnitude, stopping: false)
                == .wait)
        #expect(AgentHold.next(atWork: true, remaining: 3, stopping: false) == .stop)
        #expect(AgentHold.next(atWork: true, remaining: 3, stopping: true) == .wait)
        #expect(AgentHold.next(atWork: false, remaining: 3, stopping: true) == .end)
    }

    @Test func anAgentIsAtWorkWhileARunOfItsRuns() {
        var state = AgentState.empty
        #expect(!state.atWork)
        state.runs = [
            AgentState.Run(
                id: "r", surface: "chat", agentName: "A", instruction: "Tidy", status: "done",
                undone: false, edits: [], summary: "", stoppable: false)
        ]
        #expect(!state.atWork)
        state.runs[0].status = "running"
        #expect(state.atWork)
        state.runs = []
        state.running = true
        #expect(state.atWork)
    }

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

    @Test func keychainErrorsEndWithTheirStatus() {
        for status in [errSecUserCanceled, errSecAuthFailed, errSecInteractionNotAllowed, -1] {
            let text = Keychain.Failure(status: status).localizedDescription
            #expect(text.hasSuffix(" (OSStatus \(status))"), "\(text)")
        }
    }

    @Test func theSidebarComesUpToTheFold() {
        #expect(FoldReader.width(of: 867, fold: 455.5...495.5) == 475.5)
        #expect(FoldReader.width(of: 867, fold: nil) == nil)
        #expect(FoldReader.width(of: 867, fold: 150...190) == nil)
        #expect(FoldReader.width(of: 600, fold: 400...440) == nil)
    }

    @Test func theDeviceGoesByItsModel() {
        #expect(PlatformBridge.label(of: .iPadPro13M5) == "iPad Pro (13-inch) (M5)")
        #expect(PlatformBridge.label(of: .iPhoneDuo) == "iPhone Duo")
        #expect(PlatformBridge.label(of: .simulator(.iPhone17)) == "Simulator (iPhone 17)")
        #expect(PlatformBridge.label(of: .unknown("iPad99,1")) == "iPad")
        #expect(!PlatformBridge.label(of: DeviceKit.Device.current).isEmpty)
    }
}
