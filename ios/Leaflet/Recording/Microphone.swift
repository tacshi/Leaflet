import AVFoundation
import CallKit
import LeafletCore

/// Microphone is the device's microphone as the core's recordings hear
/// it: the audio engine's input, turned into 16-bit mono PCM at the rate
/// the core asks for. The core calls it from its own threads; it does its
/// work on a queue of its own, which also hands the core the audio, in
/// order.
final class Microphone: NSObject, MobileMicrophoneProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "app.capyvibe.leaflet.microphone")
    // Touched only on queue.
    private var engine: AVAudioEngine?
    private var sink: MobileAudioSink?
    private var paused = false
    /// stopped says the recording's audio is all handed on: nothing turns
    /// the engine back on.
    private var stopped = false
    /// session counts the recordings started, so a permission answered
    /// late starts none of an earlier one's.
    private var session = 0
    /// rate is the rate the core asked for.
    private var rate = 16000
    /// observers hear calls taking the microphone, and the engine's
    /// input changing under it.
    private var observers: [any NSObjectProtocol] = []

    func start(_ rate: Int, sink: MobileAudioSink?) throws {
        let session = queue.sync {
            self.session += 1
            self.sink = sink
            paused = false
            stopped = false
            return self.session
        }
        AVAudioApplication.requestRecordPermission { granted in
            self.queue.async {
                guard session == self.session, let sink = self.sink else { return }
                guard granted else {
                    sink.failed(LI.microphoneDenied)
                    return
                }
                do {
                    try self.run(rate: rate)
                    sink.started()
                } catch {
                    self.release()
                    sink.failed(Self.why(error))
                }
            }
        }
    }

    /// run turns the microphone on; on queue.
    private func run(rate: Int) throws {
        let audio = AVAudioSession.sharedInstance()
        try audio.setCategory(
            .playAndRecord, mode: .default, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try audio.setActive(true)
        self.rate = rate
        let engine = AVAudioEngine()
        try tap(engine)
        engine.prepare()
        try engine.start()
        self.engine = engine
        let center = NotificationCenter.default
        // A call or Siri takes the microphone; it comes back after.
        observers.append(
            center.addObserver(
                forName: AVAudioSession.interruptionNotification, object: audio, queue: nil
            ) { [weak self] note in
                let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt
                guard raw == AVAudioSession.InterruptionType.ended.rawValue, let mic = self else {
                    return
                }
                mic.queue.async { [weak mic] in mic?.restart(engine, rewire: false) }
            })
        // AirPods or a headset coming or going change the input, its rate
        // and channels with it: the engine stops, and the tap, made for the
        // input before, must be made again for the audio to go on.
        observers.append(
            center.addObserver(
                forName: .AVAudioEngineConfigurationChange, object: engine, queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.restart(engine, rewire: true) }
            })
    }

    /// tap hands the core what engine's input hears, converted from the
    /// input's format as it is now; on queue.
    private func tap(_ engine: AVAudioEngine) throws {
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
            mic.queue.async { [weak mic] in
                guard let self = mic, !self.paused, self.engine === engine else { return }
                self.sink?.chunk(pcm)
            }
        }
    }

    /// restart turns engine back on after something stopped it, while it
    /// is the recording's and not paused, with its tap made again for an
    /// input that changed (rewire); on queue. A paused one is rewired too,
    /// and Resume starts it.
    private func restart(_ engine: AVAudioEngine, rewire: Bool) {
        guard self.engine === engine, !stopped else { return }
        if rewire {
            engine.inputNode.removeTap(onBus: 0)
            do {
                try tap(engine)
            } catch {
                // An input with no audio, as it changes: the next change
                // brings one.
                return
            }
        }
        guard !paused, !engine.isRunning else { return }
        try? AVAudioSession.sharedInstance().setActive(true)
        engine.prepare()
        try? engine.start()
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

    func pause() {
        queue.async {
            self.paused = true
            self.engine?.pause()
        }
    }

    func resume() {
        queue.async {
            self.paused = false
            try? self.engine?.start()
        }
    }

    /// stop hands on what the engine heard before it, then turns it off:
    /// the queue runs the audio already on its way first.
    func stop() {
        queue.sync {
            stopped = true
            engine?.inputNode.removeTap(onBus: 0)
            engine?.stop()
        }
    }

    func close() {
        queue.sync {
            session += 1
            release()
        }
    }

    /// release lets the microphone go; on queue.
    private func release() {
        for o in observers { NotificationCenter.default.removeObserver(o) }
        observers = []
        engine?.inputNode.removeTap(onBus: 0)
        engine?.stop()
        engine = nil
        sink = nil
        try? AVAudioSession.sharedInstance().setActive(
            false, options: .notifyOthersOnDeactivation)
    }
}
