import AVFoundation
import CallKit
import LeafletCore

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
    private final class Recording: @unchecked Sendable {
        let rate: Int
        let sink: MobileAudioSink
        var engine: AVAudioEngine?
        var paused = false
        /// stopped says the recording's audio is all handed on: nothing
        /// turns the engine back on.
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
            earlier.engine?.inputNode.removeTap(onBus: 0)
            earlier.engine?.stop()
        }
        let audio = AVAudioSession.sharedInstance()
        try audio.setCategory(
            .playAndRecord, mode: .default, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try audio.setActive(true)
        let engine = AVAudioEngine()
        try tap(engine, session: session, rate: rec.rate)
        engine.prepare()
        try engine.start()
        rec.engine = engine
        let center = NotificationCenter.default
        // A call or Siri takes the microphone; it comes back after.
        rec.observers.append(
            center.addObserver(
                forName: AVAudioSession.interruptionNotification, object: audio, queue: nil
            ) { [weak self] note in
                let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt
                guard raw == AVAudioSession.InterruptionType.ended.rawValue, let mic = self else {
                    return
                }
                mic.queue.async { [weak mic] in mic?.restart(session, engine, rewire: false) }
            })
        // AirPods or a headset coming or going change the input, its rate
        // and channels with it: the engine stops, and the tap, made for the
        // input before, must be made again for the audio to go on.
        rec.observers.append(
            center.addObserver(
                forName: .AVAudioEngineConfigurationChange, object: engine, queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.restart(session, engine, rewire: true) }
            })
    }

    /// tap hands recording session what engine's input hears, converted
    /// from the input's format as it is now; on queue.
    private func tap(_ engine: AVAudioEngine, session: Int, rate: Int) throws {
        let input = engine.inputNode
        let from = input.outputFormat(forBus: 0)
        guard from.sampleRate > 0, from.channelCount > 0,
            let to = AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: Double(rate), channels: 1,
                interleaved: true),
            let converter = AVAudioConverter(from: from, to: to)
        else {
            throw CocoaError(.featureUnsupported)
        }
        input.installTap(onBus: 0, bufferSize: 4096, format: from) { [weak self] buffer, _ in
            // Converted here, as the engine may use the buffer again.
            guard let mic = self, let pcm = Self.convert(buffer, with: converter, to: to) else {
                return
            }
            // What comes after Stop, the audio all handed on, is not the
            // recording's.
            mic.queue.async { [weak mic] in
                guard let rec = mic?.recordings[session], !rec.paused, !rec.stopped,
                    rec.engine === engine
                else { return }
                rec.sink.chunk(pcm)
            }
        }
    }

    /// restart turns engine back on after something stopped it, while it
    /// is recording session's and not paused, with its tap made again for
    /// an input that changed (rewire); on queue. A paused one is rewired
    /// too, and Resume starts it. One that won't start says why, and the
    /// core pauses the recording.
    private func restart(_ session: Int, _ engine: AVAudioEngine, rewire: Bool) {
        guard let rec = recordings[session], rec.engine === engine, !rec.stopped else { return }
        if rewire {
            engine.inputNode.removeTap(onBus: 0)
            do {
                try tap(engine, session: session, rate: rec.rate)
            } catch {
                // An input with no audio, as it changes: the next change
                // brings one.
                return
            }
        }
        guard !rec.paused, !engine.isRunning else { return }
        turnOn(rec, engine)
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
        let refused =
            (error as NSError).code == AVAudioSession.ErrorCode.insufficientPriority.rawValue
        return inCall || refused ? LI.microphoneInCall : error.localizedDescription
    }

    /// convert turns what the engine heard into the core's PCM.
    private static func convert(
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

    func pause(_ session: Int) {
        queue.async {
            guard let rec = self.recordings[session] else { return }
            rec.paused = true
            rec.engine?.pause()
        }
    }

    func resume(_ session: Int) {
        queue.async {
            guard let rec = self.recordings[session], !rec.stopped else { return }
            rec.paused = false
            if let engine = rec.engine, !engine.isRunning {
                self.turnOn(rec, engine)
            }
        }
    }

    /// stop hands on what the engine heard before it, then turns it off:
    /// the queue runs the audio already on its way first.
    func stop(_ session: Int) {
        queue.sync {
            guard let rec = recordings[session] else { return }
            rec.stopped = true
            rec.engine?.inputNode.removeTap(onBus: 0)
            rec.engine?.stop()
        }
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
        rec.engine?.stop()
        rec.engine = nil
        guard !recordings.values.contains(where: { $0.engine != nil }) else { return }
        try? AVAudioSession.sharedInstance().setActive(
            false, options: .notifyOthersOnDeactivation)
    }
}
