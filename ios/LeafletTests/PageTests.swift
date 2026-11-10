import AVFoundation
import Foundation
import LeafletCore
import LeafletSpeech
import Testing

@testable import Leaflet

@MainActor
struct PageTests {
    final class Host: PageHost {
        let core: ScratchCore
        var client: CoreClient? { core.client }
        var toasts: [String] = []

        init() throws { core = try ScratchCore("PageTests") }

        func opened() async -> CoreClient? { client }
        func listen(_ fn: @escaping (CoreEvent) -> Void) -> UUID { UUID() }
        func stopListening(_ id: UUID) {}
        func show(_ toast: Toast) { toasts.append(toast.message) }
    }

    private func opened(_ host: Host) async throws -> PageModel {
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let model = PageModel(id: page.id, app: host)
        await model.open()
        try #require(model.content != nil)
        return model
    }

    private func page(_ model: PageModel, _ text: String) -> DocContent {
        var c = model.content!
        c.document.children = [
            DocNode(
                id: "a", type: "paragraph",
                data: ["delta": .array([.object(["insert": .string(text)])])], children: nil)
        ]
        return c
    }

    private func changed(_ model: PageModel, _ text: String, at rev: Int64) -> CoreEvent {
        .pageUpdated(
            pageID: model.id, content: page(model, text), rev: rev, reason: "sync", newStep: false)
    }

    @Test func aChangeFromOutsideShowsUnlessOlder() async throws {
        let host = try Host()
        let model = try await opened(host)
        let rev = model.rev
        model.handle(changed(model, "Synced", at: rev + 2))
        #expect(model.external == 1)
        #expect(model.rev == rev + 2)
        #expect(model.content?.blocks.first?.text == "Synced")
        model.handle(changed(model, "Older", at: rev + 1))
        #expect(model.external == 1)
        #expect(model.content?.blocks.first?.text == "Synced")
        await model.close()
    }

    @Test func theOutlineFollowsTheHeadings() async throws {
        let host = try Host()
        let model = try await opened(host)
        var c = model.content!
        c.document.children = [
            DocNode(
                id: "h1", type: "heading",
                data: ["level": .int(1), "delta": .array([.object(["insert": .string("Plan")])])],
                children: nil),
            DocNode(
                id: "h2", type: "heading", data: ["level": .int(2), "delta": .array([])],
                children: nil),
        ]
        model.handle(
            .pageUpdated(
                pageID: model.id, content: c, rev: model.rev + 1, reason: "sync", newStep: false))
        for _ in 0..<100 where model.outline.count < 2 {
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(model.outline.map(\.id) == ["h1", "h2"])
        #expect(model.outline.map(\.parent) == [nil, "h1"])
        #expect(model.outline.first?.text == "Plan")
        await model.close()
    }

    @Test func aChangeWaitsForTheEditOnItsWay() async throws {
        let host = try Host()
        let model = try await opened(host)
        let rev = model.rev
        model.edit(page(model, "Typed"))
        model.handle(changed(model, "Synced", at: rev + 1))
        #expect(model.external == 0)
        #expect(model.content?.blocks.first?.text == "Typed")
        await model.flush()
        #expect(model.external == 1)
        #expect(model.rev == rev + 1)
        #expect(model.content?.blocks.first?.text == "Synced")
        #expect(host.toasts.isEmpty)
        await model.close()
    }

    @Test func aChangeFromBeforeAMergedEditIsDropped() async throws {
        let host = try Host()
        let model = try await opened(host)
        let client = host.core.client
        let rev = model.rev
        let other = try await client.openPage(model.id)
        try await client.editPage(
            model.id, editor: other.editor, content: page(model, "Other"), rev: other.rev)
        model.edit(page(model, "Typed"))
        model.handle(changed(model, "Other", at: rev + 1))
        await model.flush()
        #expect(model.external == 0)
        #expect(model.content?.blocks.first?.text == "Typed")
        model.handle(changed(model, "Merged", at: rev + 2))
        #expect(model.external == 1)
        #expect(model.content?.blocks.first?.text == "Merged")
        await client.closePage(model.id, editor: other.editor)
        await model.close()
    }

    @Test func aPageOpensWithItsAgentsRead() async throws {
        let host = try Host()
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let model = PageModel(id: page.id, app: host)
        #expect(!model.agentsLoaded)
        await model.open()
        #expect(model.agentsLoaded)
        #expect(model.shown == model.rev)
        await model.close()
    }

    @Test func timesSayWhenAsTheDesktopDoes() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .current
        let now = try #require(
            cal.date(from: DateComponents(year: 2026, month: 11, day: 3, hour: 15)))
        let ago = { (seconds: TimeInterval) in now.addingTimeInterval(-seconds) }
        let say = { (t: Date) in Byline.humanTime(t, now: now) }
        #expect(say(Date(timeIntervalSince1970: 0)).isEmpty)
        #expect(say(ago(30)) == L.pageTimeJustNow)
        #expect(say(ago(5 * 60)).contains("5"))
        #expect(say(ago(5 * 60)) != L.pageTimeTodayAt(at: ago(5 * 60)))
        #expect(say(ago(3 * 3600)) == L.pageTimeTodayAt(at: ago(3 * 3600)))
        #expect(say(ago(20 * 3600)) == L.pageTimeYesterdayAt(at: ago(20 * 3600)))
        #expect(say(ago(30 * 86400)) == L.pageTimeDate(date: ago(30 * 86400)))
        #expect(say(ago(400 * 86400)) == L.pageTimeDateYear(date: ago(400 * 86400)))
    }

    private func run(_ status: String, edits: [String] = [], error: String? = nil)
        -> AgentState.Run
    {
        AgentState.Run(
            id: "r", surface: "inline", agentName: "Agent", instruction: "", status: status,
            undone: false, error: error, edits: edits, blockIDs: nil, summary: "", stoppable: true,
            answer: nil)
    }

    @Test func theAskSheetGoesOnceTheRunWrites() {
        #expect(!AskSheet.closes(run("running")))
        #expect(AskSheet.closes(run("running", edits: ["Inserted a block"])))
        #expect(AskSheet.closes(run("done", edits: ["Inserted a block"])))
        #expect(!AskSheet.closes(run("done")))
        #expect(!AskSheet.closes(run("failed", edits: ["Inserted a block"], error: "No key")))
    }

    @Test func undoInThePageWaitsForTheRunThatWorks() {
        var older = run("done", edits: ["Inserted a block"])
        older.id = "older"
        var state = AgentState.empty
        state.runs = [older]
        #expect(state.runToUndo?.id == "older")

        state.runs.append(run("running"))
        state.running = true
        #expect(state.runToUndo == nil)
        state.runs[1] = run("running", edits: ["Inserted a block"])
        #expect(state.runToUndo == nil)

        state.runs[1] = run("done", edits: ["Inserted a block"])
        state.running = false
        #expect(state.runToUndo?.id == "r")
        state.runs[1].undone = true
        #expect(state.runToUndo?.id == "older")
    }

    @Test func aCommentSaysHowItCame() {
        let comment = { (kind: String, asked: String?) in
            CommentThread.Comment(
                id: "c", blockID: "b", authorKind: kind, authorName: "Ann", body: "Hi",
                resolved: false, createdAt: 0, mayDelete: false, askedBy: asked)
        }
        #expect(CommentRow.note(comment("guest", nil)) == L.panelViaPublicLink)
        #expect(CommentRow.note(comment("agent", "asked by Ann")) == "asked by Ann")
        #expect(CommentRow.note(comment("user", nil)) == nil)
    }

    @Test func theMicrophonesAudioBecomesTheCoresPCM() throws {
        let from = try #require(AVAudioFormat(standardFormatWithSampleRate: 48000, channels: 1))
        let to = try #require(
            AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: 16000, channels: 1, interleaved: true))
        let converter = try #require(AVAudioConverter(from: from, to: to))
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: from, frameCapacity: 4800))
        buffer.frameLength = 4800
        let samples = try #require(buffer.floatChannelData)
        for i in 0..<4800 { samples[0][i] = 0.5 }
        let first = try #require(Microphone.convert(buffer, with: converter, to: to))
        #expect(first.count < 1600 * 2)
        var pcm = Data()
        var total = first.count / 2
        for _ in 0..<7 {
            pcm = try #require(Microphone.convert(buffer, with: converter, to: to))
            total += pcm.count / 2
        }
        #expect((8 * 1600 - 320...8 * 1600).contains(total))
        let middle = pcm.withUnsafeBytes {
            $0.loadUnaligned(fromByteOffset: pcm.count / 2 & ~1, as: Int16.self)
        }
        #expect(abs(Int(middle) - 16384) < 1000)
    }

    @Test func aWholeStreamReachesTheCore() throws {
        let from = try #require(AVAudioFormat(standardFormatWithSampleRate: 48000, channels: 1))
        let resampler = try #require(Resampler(from: from, rate: 16000))
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: from, frameCapacity: 4800))
        buffer.frameLength = 4800
        let samples = try #require(buffer.floatChannelData)
        for i in 0..<4800 { samples[0][i] = 0.5 }
        var total = 0
        for _ in 0..<8 { resampler.convert(buffer) { total += $0.count / 2 } }
        #expect(total < 8 * 1600)
        let end = try #require(resampler.finish())
        #expect(total + end.count / 2 == 8 * 1600)
        resampler.convert(buffer) { _ in Issue.record("audio after the end") }
        #expect(resampler.finish() == nil)
    }

    @Test func theLastAudioBeforeStopIsKept() {
        let engine = AVAudioEngine()
        let rec = Microphone.Recording(rate: 16000, sink: MobileAudioSink())
        rec.engine = engine
        #expect(Microphone.hands(rec, from: engine))
        rec.stopped = true
        #expect(Microphone.hands(rec, from: engine))
        #expect(!Microphone.hands(rec, from: AVAudioEngine()))
        rec.paused = true
        #expect(!Microphone.hands(rec, from: engine))
    }

    @Test func aStuckTranscriptionIsGivenUp() async throws {
        let stuck = await Task.detached {
            do {
                _ = try DeviceTranscriber.wait(Pulse(), stall: .milliseconds(1500)) {
                    try await Task.sleep(for: .seconds(60))
                    return []
                }
                return nil as String?
            } catch {
                return (error as? SpeechFailure)?.code
            }
        }.value
        #expect(stuck == "stuck")

        let segments = try await Task.detached {
            let pulse = Pulse()
            return try DeviceTranscriber.wait(pulse, stall: .milliseconds(1500)) {
                for _ in 0..<5 {
                    try await Task.sleep(for: .milliseconds(600))
                    pulse.beat()
                }
                return [SpeechSegment(startMs: 0, endMs: 1000, text: "Hello")]
            }
        }.value
        #expect(segments.map(\.text) == ["Hello"])
    }
}
