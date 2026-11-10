import AVFoundation
import CallKit
import LeafletCore
import UIKit

/// Microphone is the device's microphone as the core's recordings hear
/// it: the audio engine's input, turned into 16-bit mono PCM at the rate
/// the core asks for. The core calls it from its own threads, naming the
/// recording each call is for by its session; it does its work on a queue
/// of its own, which also hands each recording its audio, in order.
final class Microphone: NSObject, MobileMicrophoneProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "app.capyvibe.leaflet.microphone")
    /// recordings are the recordings the microphone is on for, or is
    /// asking to be, by session: one stopping still hands its own sink
    /// what its engine held while the next one starts, and a late Stop or
    /// Close of it leaves the next one alone. Touched only on queue.
    private var recordings: [Int: Recording] = [:]

    /// Recording is one recording's engine and sink; touched only on queue.
    final class Recording: @unchecked Sendable {
        let rate: Int
        let sink: MobileAudioSink
        var engine: AVAudioEngine?
        /// resampler turns what the engine's tap hears into the core's PCM.
        var resampler: Resampler?
        var paused = false
        /// stopped says the recording stopped: nothing turns the engine
        /// back on, and the tap is gone.
        var stopped = false
        /// observers hear calls taking the microphone, and the engine's
        /// input changing under it.
        var observers: [any NSObjectProtocol] = []

        init(rate: Int, sink: MobileAudioSink) {
            self.rate = rate
            self.sink = sink
        }
    }

    func start(_ session: Int, rate: Int, sink: MobileAudioSink?) throws {
        guard let sink else { return }
        let rec = Recording(rate: rate, sink: sink)
        queue.sync { recordings[session] = rec }
        AVAudioApplication.requestRecordPermission { granted in
            self.queue.async {
                // Stopped or closed meanwhile, as a recording ended while it
                // asked: nothing turns its engine on.
                guard self.recordings[session] === rec, !rec.stopped else { return }
                guard granted else {
                    rec.sink.failed(LI.microphoneDenied)
                    return
                }
                do {
                    try self.run(session, rec)
                    rec.sink.started()
                } catch {
                    self.release(session)
                    rec.sink.failed(Self.why(error))
                }
            }
        }
    }

    /// run turns the microphone on for rec; on queue. One recording at a
    /// time has the input: an earlier one's engine, still on as its Stop
    /// has yet to come, stops, after the audio it handed on.
    private func run(_ session: Int, _ rec: Recording) throws {
        for (other, earlier) in recordings where other != session {
            earlier.stopped = true
            endTap(other, earlier)
            earlier.engine?.stop()
        }
        let audio = AVAudioSession.sharedInstance()
        try audio.setCategory(
            .playAndRecord, mode: .default, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try audio.setActive(true)
        let engine = AVAudioEngine()
        try tap(engine, session: session, rec)
        engine.prepare()
        do {
            try engine.start()
        } catch {
            // An engine that won't start, as from the background or in a
            // call, lets its tap go, which holds the engine: kept, neither
            // would ever be freed, one for each try.
            engine.inputNode.removeTap(onBus: 0)
            rec.resampler = nil
            throw error
        }
        rec.engine = engine
        guard rec.observers.isEmpty else { return }
        let center = NotificationCenter.default
        // A call, Siri or another app's audio takes the microphone: the
        // recording pauses, saying so, as it hears nothing meanwhile, and
        // goes on once it is given back. One never given back, as iOS may
        // leave it, stays paused, to resume or stop.
        rec.observers.append(
            center.addObserver(
                forName: AVAudioSession.interruptionNotification, object: audio, queue: nil
            ) { [weak self] note in
                guard let mic = self,
                    let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                    let kind = AVAudioSession.InterruptionType(rawValue: raw)
                else { return }
                let options = AVAudioSession.InterruptionOptions(
                    rawValue: note.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt ?? 0)
                mic.queue.async { [weak mic] in
                    mic?.interrupted(
                        session, ended: kind == .ended, resume: options.contains(.shouldResume))
                }
            })
        // The media services restarting take every engine with them: the
        // recording's starts anew.
        rec.observers.append(
            center.addObserver(
                forName: AVAudioSession.mediaServicesWereResetNotification, object: audio,
                queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.rebuild(session) }
            })
        // Back in front, an engine something stopped without saying, as an
        // interruption whose end never came, goes on, or says why not.
        rec.observers.append(
            center.addObserver(
                forName: UIApplication.didBecomeActiveNotification, object: nil, queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.wake(session) }
            })
        // AirPods or a headset coming or going change the input, its rate
        // and channels with it: the engine stops, and the tap, made for the
        // input before, must be made again for the audio to go on.
        rec.observers.append(
            center.addObserver(
                forName: .AVAudioEngineConfigurationChange, object: nil, queue: nil
            ) { [weak self] note in
                guard let mic = self, let changed = note.object as? AVAudioEngine else { return }
                mic.queue.async { [weak mic] in mic?.restart(session, changed, rewire: true) }
            })
    }

    /// interrupted pauses recording session as something takes the
    /// microphone, and resumes it as that ends, when iOS says to; on queue.
    private func interrupted(_ session: Int, ended: Bool, resume: Bool) {
        guard let rec = recordings[session], !rec.stopped else { return }
        if !ended {
            rec.sink.stalled(LI.microphoneTaken)
        } else if resume {
            rec.sink.recovered()
        }
    }

    /// rebuild makes recording session a new engine, as the media services
    /// restarted, and starts it unless the recording is paused, which
    /// Resume then starts; on queue.
    private func rebuild(_ session: Int) {
        guard let rec = recordings[session], !rec.stopped else { return }
        endTap(session, rec)
        rec.engine = nil
        guard !rec.paused else { return }
        do {
            try run(session, rec)
        } catch {
            rec.sink.stalled(Self.why(error))
        }
    }

    /// wake turns recording session's engine back on when it is recording
    /// and something stopped it, as the app comes to the front; on queue.
    private func wake(_ session: Int) {
        guard let rec = recordings[session], !rec.stopped, !rec.paused else { return }
        guard let engine = rec.engine else { return rebuild(session) }
        // An engine left without its tap, by an input that changed to one
        // with no audio, would run and hear nothing.
        guard tapped(session, rec, engine) else {
            rec.sink.stalled(LI.microphoneTaken)
            return
        }
        if !engine.isRunning { turnOn(rec, engine) }
    }

    /// tap hands recording session, rec, what engine's input hears,
    /// converted from the input's format as it is now; on queue.
    private func tap(_ engine: AVAudioEngine, session: Int, _ rec: Recording) throws {
        let input = engine.inputNode
        let from = input.outputFormat(forBus: 0)
        guard let resampler = Resampler(from: from, rate: rec.rate) else {
            throw CocoaError(.featureUnsupported)
        }
        rec.resampler = resampler
        input.installTap(onBus: 0, bufferSize: 4096, format: from) { [weak self] buffer, _ in
            // Converted here, as the engine may use the buffer again.
            resampler.convert(buffer) { pcm in
                self?.queue.async { [weak self] in self?.hand(pcm, session: session, from: engine) }
            }
        }
    }

    /// endTap takes recording session's tap away, rec's, and hands on what
    /// its converter held back of the stream, after a buffer the tap was
    /// converting as it went; on queue.
    private func endTap(_ session: Int, _ rec: Recording) {
        rec.engine?.inputNode.removeTap(onBus: 0)
        guard let resampler = rec.resampler, let engine = rec.engine else { return }
        rec.resampler = nil
        // The buffer being converted is handed on as finish waits for it:
        // its audio goes on the queue before the end.
        guard let end = resampler.finish() else { return }
        queue.async { [weak self] in self?.hand(end, session: session, from: engine) }
    }

    /// hand gives recording session the audio engine heard, as hands
    /// says; on queue.
    private func hand(_ pcm: Data, session: Int, from engine: AVAudioEngine) {
        guard let rec = recordings[session], Self.hands(rec, from: engine) else { return }
        rec.sink.chunk(pcm)
    }

    /// hands says whether audio engine heard goes to rec's sink: while the
    /// engine is rec's and it is not paused, after Stop too, until Close.
    /// Stop takes the tap away, but a buffer being converted as it does is
    /// heard before it, and the core takes it as the recording's last.
    static func hands(_ rec: Recording, from engine: AVAudioEngine) -> Bool {
        rec.engine === engine && !rec.paused
    }

    /// restart turns engine back on after something stopped it, while it
    /// is recording session's and not paused, with its tap made again for
    /// an input that changed (rewire); on queue. A paused one is rewired
    /// too, and Resume starts it. One that won't start says why, and the
    /// core pauses the recording.
    private func restart(_ session: Int, _ engine: AVAudioEngine, rewire: Bool) {
        guard let rec = recordings[session], rec.engine === engine, !rec.stopped else { return }
        if rewire {
            endTap(session, rec)
            guard tapped(session, rec, engine) else {
                // An input with no audio, as it changes: the next change
                // brings one, most often. Where none comes, as with the
                // input taken for good, the recording would count on over
                // nothing: it is looked at again in a moment.
                queue.asyncAfter(deadline: .now() + Self.rewireWait) { [weak self] in
                    self?.rewire(session, engine)
                }
                return
            }
        }
        guard !rec.paused, !engine.isRunning else { return }
        turnOn(rec, engine)
    }

    /// rewireWait is how long an input that came without audio is given
    /// to change again before its recording says the microphone is gone.
    private static let rewireWait: TimeInterval = 2

    /// tapped makes sure rec, recording session, has its tap on engine,
    /// making it for the input as it is now where it has none, and reports
    /// whether it has; on queue.
    private func tapped(_ session: Int, _ rec: Recording, _ engine: AVAudioEngine) -> Bool {
        if rec.resampler != nil { return true }
        do {
            try tap(engine, session: session, rec)
            return true
        } catch {
            return false
        }
    }

    /// rewire gives recording session its tap on engine once more, where
    /// a change of input left it none and no change since made it one,
    /// and starts it; with still no audio to tap, the microphone is gone,
    /// which it says, and the core pauses the recording; on queue.
    private func rewire(_ session: Int, _ engine: AVAudioEngine) {
        guard let rec = recordings[session], rec.engine === engine, !rec.stopped,
            rec.resampler == nil
        else { return }
        guard tapped(session, rec, engine) else {
            rec.sink.stalled(LI.microphoneTaken)
            return
        }
        if !rec.paused, !engine.isRunning { turnOn(rec, engine) }
    }

    /// turnOn starts engine again, the audio session first, as a call may
    /// have ended it; one that won't start says why to rec's sink; on
    /// queue.
    private func turnOn(_ rec: Recording, _ engine: AVAudioEngine) {
        do {
            try AVAudioSession.sharedInstance().setActive(true)
            engine.prepare()
            try engine.start()
        } catch {
            rec.sink.stalled(Self.why(error))
        }
    }

    /// why says why the microphone could not start. A call holds it, and
    /// iOS lets no app record meanwhile: its audio session is refused as
    /// of too low a priority, or fails to activate.
    private static func why(_ error: any Error) -> String {
        let inCall = CXCallObserver().calls.contains { !$0.hasEnded }
        let code = (error as NSError).code
        if inCall || code == AVAudioSession.ErrorCode.insufficientPriority.rawValue {
            return LI.microphoneInCall
        }
        // iOS lets no app start recording again from the background.
        if code == AVAudioSession.ErrorCode.cannotStartRecording.rawValue {
            return LI.microphoneInBackground
        }
        return error.localizedDescription
    }

    /// end is what converter held back of the stream it converted to
    /// format, as the stream ends.
    static func end(_ converter: AVAudioConverter, to format: AVAudioFormat) -> Data? {
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 4096) else {
            return nil
        }
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            status.pointee = .endOfStream
            return nil
        }
        guard error == nil, out.frameLength > 0, let samples = out.int16ChannelData else {
            return nil
        }
        return Data(bytes: samples[0], count: Int(out.frameLength) * 2)
    }

    /// convert turns what the engine heard into the core's PCM.
    static func convert(
        _ buffer: AVAudioPCMBuffer, with converter: AVAudioConverter, to format: AVAudioFormat
    ) -> Data? {
        let ratio = format.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 64
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else {
            return nil
        }
        var given = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if given {
                status.pointee = .noDataNow
                return nil
            }
            given = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, out.frameLength > 0, let samples = out.int16ChannelData else {
            return nil
        }
        return Data(bytes: samples[0], count: Int(out.frameLength) * 2)
    }

    /// pause stops handing on what recording session hears (hands). Its
    /// engine stays on: iOS lets no app start recording again from the
    /// background, where Resume from the Live Activity would find the
    /// microphone gone, and the app, its audio stopped, put away.
    func pause(_ session: Int) {
        queue.async {
            guard let rec = self.recordings[session] else { return }
            rec.paused = true
        }
    }

    func resume(_ session: Int) {
        queue.async {
            guard let rec = self.recordings[session], !rec.stopped else { return }
            rec.paused = false
            if let engine = rec.engine {
                // As wake: with no tap, it would go on hearing nothing.
                guard self.tapped(session, rec, engine) else {
                    rec.sink.stalled(LI.microphoneTaken)
                    return
                }
                if !engine.isRunning { self.turnOn(rec, engine) }
            } else {
                self.rebuild(session)
            }
        }
    }

    /// stop hands on all the engine heard before it, then turns it off:
    /// the audio on its way, a buffer the tap was converting, and what the
    /// converter held back, before it returns, as the core takes the
    /// recording's last audio until then.
    func stop(_ session: Int) {
        queue.sync {
            guard let rec = recordings[session] else { return }
            rec.stopped = true
            endTap(session, rec)
            rec.engine?.stop()
        }
        // The end, endTap put on the queue, handed on.
        queue.sync {}
    }

    func close(_ session: Int) {
        queue.sync { release(session) }
    }

    /// release lets recording session's microphone go, and the audio
    /// session too unless another recording has it on; on queue.
    private func release(_ session: Int) {
        guard let rec = recordings.removeValue(forKey: session) else { return }
        for o in rec.observers { NotificationCenter.default.removeObserver(o) }
        rec.observers = []
        rec.engine?.inputNode.removeTap(onBus: 0)
        _ = rec.resampler?.finish()
        rec.resampler = nil
        rec.engine?.stop()
        rec.engine = nil
        guard !recordings.values.contains(where: { $0.engine != nil }) else { return }
        try? AVAudioSession.sharedInstance().setActive(
            false, options: .notifyOthersOnDeactivation)
    }
}

/// Resampler turns what the engine hears into the core's PCM as it
/// streams, from the tap's thread: the converter holds back a little of
/// each buffer for the next, which finish gives as the stream ends. One
/// call at a time, so finish waits for a buffer being converted, and what
/// it hands on goes first.
final class Resampler: @unchecked Sendable {
    private let lock = NSLock()
    private let converter: AVAudioConverter
    private let format: AVAudioFormat
    private var ended = false

    init?(from: AVAudioFormat, rate: Int) {
        guard from.sampleRate > 0, from.channelCount > 0,
            let to = AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: Double(rate), channels: 1,
                interleaved: true),
            let converter = AVAudioConverter(from: from, to: to)
        else { return nil }
        self.converter = converter
        format = to
    }

    /// convert hands buffer on, as the core's PCM, until the stream ends.
    func convert(_ buffer: AVAudioPCMBuffer, hand: (Data) -> Void) {
        lock.withLock {
            guard !ended, let pcm = Microphone.convert(buffer, with: converter, to: format)
            else { return }
            hand(pcm)
        }
    }

    /// finish ends the stream and returns what the converter held back,
    /// once a buffer being converted is handed on.
    func finish() -> Data? {
        lock.withLock {
            guard !ended else { return nil }
            ended = true
            return Microphone.end(converter, to: format)
        }
    }
}
