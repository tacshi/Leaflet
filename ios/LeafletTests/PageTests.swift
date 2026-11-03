import AVFoundation
import Foundation
import LeafletCore
import LeafletSpeech
import Testing

@testable import Leaflet

/// PageTests cover a page's model and what shows beside its text: the
/// order its edits and the changes from outside go in, the times the
/// byline and the comments say, when the Ask sheet goes, and the pieces
/// of a recording.
@MainActor
struct PageTests {
    /// Host is the app as a page's model needs it, over a core of its own,
    /// whose events it leaves to the test to give.
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

    /// opened is a new page, open in a model over host.
    private func opened(_ host: Host) async throws -> PageModel {
        let page = try await host.core.client.createPage(NewPage(title: "Notes"))
        let model = PageModel(id: page.id, app: host)
        await model.open()
        try #require(model.content != nil)
        return model
    }

    /// page is the model's page holding one paragraph of text.
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

    /// A change from outside shows at once, unless it is older than the
    /// one shown.
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

    /// A change from outside that comes while an edit is on its way waits
    /// for it, then shows: shown before, it would take away what was just
    /// typed until the core's merge came.
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

    /// A change from outside from before an edit the core merged is
    /// dropped, though it comes after: the merge, newer, has the edit.
    @Test func aChangeFromBeforeAMergedEditIsDropped() async throws {
        let host = try Host()
        let model = try await opened(host)
        let client = host.core.client
        let rev = model.rev
        // Another window's editor changes the page first, so the core
        // merges this one's edit over it, at a rev of its own.
        let other = try await client.openPage(model.id)
        try await client.editPage(
            model.id, editor: other.editor, content: page(model, "Other"), rev: other.rev)
        model.edit(page(model, "Typed"))
        model.handle(changed(model, "Other", at: rev + 1))
        await model.flush()
        #expect(model.external == 0)
        #expect(model.content?.blocks.first?.text == "Typed")
        // The merge, as the core then sends it, shows.
        model.handle(changed(model, "Merged", at: rev + 2))
        #expect(model.external == 1)
        #expect(model.content?.blocks.first?.text == "Merged")
        await client.closePage(model.id, editor: other.editor)
        await model.close()
    }

    /// An edit goes over the rev the editor shows, and the agent state is
    /// read once the page opens: before, the panel says nothing of it.
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

    /// The byline and the comments say when as the desktop does, as the
    /// core says it: just now, minutes ago, today or yesterday at a time,
    /// or the date, with its year when not this one's; no time says
    /// nothing.
    @Test func timesSayWhenAsTheDesktopDoes() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .current
        let now = try #require(
            cal.date(from: DateComponents(year: 2026, month: 11, day: 3, hour: 15)))
        let ago = { (seconds: TimeInterval) in now.addingTimeInterval(-seconds) }
        let say = { (t: Date) in Byline.humanTime(t, now: now) }
        #expect(say(Date(timeIntervalSince1970: 0)).isEmpty)
        #expect(say(ago(30)) == L.pageTimeJustNow)
        #expect(say(ago(5 * 60)) == L.pageTimeMinutesAgo(count: 5))
        #expect(say(ago(3 * 3600)) == L.pageTimeTodayAt(at: ago(3 * 3600)))
        #expect(say(ago(20 * 3600)) == L.pageTimeYesterdayAt(at: ago(20 * 3600)))
        #expect(say(ago(30 * 86400)) == L.pageTimeDate(date: ago(30 * 86400)))
        #expect(say(ago(400 * 86400)) == L.pageTimeDateYear(date: ago(400 * 86400)))
    }

    /// run is an Ask's run, as the core says it.
    private func run(_ status: String, edits: [String] = [], error: String? = nil)
        -> AgentState.Run
    {
        AgentState.Run(
            id: "r", surface: "inline", agentName: "Agent", instruction: "", status: status,
            undone: false, error: error, edits: edits, blockIDs: nil, summary: "", stoppable: true,
            answer: nil)
    }

    /// The Ask sheet goes once the run writes in the page, or is done with
    /// edits; an answer in words stays, to insert, and so does an error.
    @Test func theAskSheetGoesOnceTheRunWrites() {
        #expect(!AskSheet.closes(run("running")))
        #expect(AskSheet.closes(run("running", edits: ["Inserted a block"])))
        #expect(AskSheet.closes(run("done", edits: ["Inserted a block"])))
        #expect(!AskSheet.closes(run("done")))
        #expect(!AskSheet.closes(run("failed", edits: ["Inserted a block"], error: "No key")))
    }

    /// What the microphone hears becomes the core's 16 kHz, 16-bit mono
    /// PCM: a third as many samples, at the level heard. The converter
    /// holds back a little of the first buffer, as it resamples a stream,
    /// and hands it on with the next ones: nothing is lost on the way.
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
        // All of eight buffers but what it holds back, under 20 ms.
        #expect((8 * 1600 - 320...8 * 1600).contains(total))
        let middle = pcm.withUnsafeBytes {
            $0.loadUnaligned(fromByteOffset: pcm.count / 2 & ~1, as: Int16.self)
        }
        #expect(abs(Int(middle) - 16384) < 1000)
    }

    /// A whole stream reaches the core, its end too: what the converter
    /// holds back comes as the stream finishes, and nothing after.
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

    /// Audio heard before Stop goes to its recording though it comes after:
    /// only Close, another engine, or a pause, takes it away.
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

    /// A transcription on the device that stops going on is given up, as
    /// stuck, for the core to say so, rather than holding the core's
    /// thread for good; one that goes on, however long, is waited for.
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
