import AVFoundation
import CallKit
import LeafletCore
import UIKit

final class Microphone: NSObject, MobileMicrophoneProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "app.capyvibe.leaflet.microphone")
    private var recordings: [Int: Recording] = [:]

    final class Recording: @unchecked Sendable {
        let rate: Int
        let sink: MobileAudioSink
        var engine: AVAudioEngine?
        var resampler: Resampler?
        var paused = false
        var stopped = false
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
            engine.inputNode.removeTap(onBus: 0)
            rec.resampler = nil
            throw error
        }
        rec.engine = engine
        guard rec.observers.isEmpty else { return }
        let center = NotificationCenter.default
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
        rec.observers.append(
            center.addObserver(
                forName: AVAudioSession.mediaServicesWereResetNotification, object: audio,
                queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.rebuild(session) }
            })
        rec.observers.append(
            center.addObserver(
                forName: UIApplication.didBecomeActiveNotification, object: nil, queue: nil
            ) { [weak self] _ in
                guard let mic = self else { return }
                mic.queue.async { [weak mic] in mic?.wake(session) }
            })
        rec.observers.append(
            center.addObserver(
                forName: .AVAudioEngineConfigurationChange, object: nil, queue: nil
            ) { [weak self] note in
                guard let mic = self, let changed = note.object as? AVAudioEngine else { return }
                mic.queue.async { [weak mic] in mic?.restart(session, changed, rewire: true) }
            })
    }

    private func interrupted(_ session: Int, ended: Bool, resume: Bool) {
        guard let rec = recordings[session], !rec.stopped else { return }
        if !ended {
            rec.sink.stalled(LI.microphoneTaken)
        } else if resume {
            rec.sink.recovered()
        }
    }

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

    private func wake(_ session: Int) {
        guard let rec = recordings[session], !rec.stopped, !rec.paused else { return }
        guard let engine = rec.engine else { return rebuild(session) }
        guard tapped(session, rec, engine) else {
            rec.sink.stalled(LI.microphoneTaken)
            return
        }
        if !engine.isRunning { turnOn(rec, engine) }
    }

    private func tap(_ engine: AVAudioEngine, session: Int, _ rec: Recording) throws {
        let input = engine.inputNode
        let from = input.outputFormat(forBus: 0)
        guard let resampler = Resampler(from: from, rate: rec.rate) else {
            throw CocoaError(.featureUnsupported)
        }
        rec.resampler = resampler
        input.installTap(onBus: 0, bufferSize: 4096, format: from) { [weak self] buffer, _ in
            resampler.convert(buffer) { pcm in
                self?.queue.async { [weak self] in self?.hand(pcm, session: session, from: engine) }
            }
        }
    }

    private func endTap(_ session: Int, _ rec: Recording) {
        rec.engine?.inputNode.removeTap(onBus: 0)
        guard let resampler = rec.resampler, let engine = rec.engine else { return }
        rec.resampler = nil
        guard let end = resampler.finish() else { return }
        queue.async { [weak self] in self?.hand(end, session: session, from: engine) }
    }

    private func hand(_ pcm: Data, session: Int, from engine: AVAudioEngine) {
        guard let rec = recordings[session], Self.hands(rec, from: engine) else { return }
        rec.sink.chunk(pcm)
    }

    static func hands(_ rec: Recording, from engine: AVAudioEngine) -> Bool {
        rec.engine === engine && !rec.paused
    }

    private func restart(_ session: Int, _ engine: AVAudioEngine, rewire: Bool) {
        guard let rec = recordings[session], rec.engine === engine, !rec.stopped else { return }
        if rewire {
            endTap(session, rec)
            guard tapped(session, rec, engine) else {
                queue.asyncAfter(deadline: .now() + Self.rewireWait) { [weak self] in
                    self?.rewire(session, engine)
                }
                return
            }
        }
        guard !rec.paused, !engine.isRunning else { return }
        turnOn(rec, engine)
    }

    private static let rewireWait: TimeInterval = 2

    private func tapped(_ session: Int, _ rec: Recording, _ engine: AVAudioEngine) -> Bool {
        if rec.resampler != nil { return true }
        do {
            try tap(engine, session: session, rec)
            return true
        } catch {
            return false
        }
    }

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

    private func turnOn(_ rec: Recording, _ engine: AVAudioEngine) {
        do {
            try AVAudioSession.sharedInstance().setActive(true)
            engine.prepare()
            try engine.start()
        } catch {
            rec.sink.stalled(Self.why(error))
        }
    }

    private static func why(_ error: any Error) -> String {
        let inCall = CXCallObserver().calls.contains { !$0.hasEnded }
        let code = (error as NSError).code
        if inCall || code == AVAudioSession.ErrorCode.insufficientPriority.rawValue {
            return LI.microphoneInCall
        }
        if code == AVAudioSession.ErrorCode.cannotStartRecording.rawValue {
            return LI.microphoneInBackground
        }
        return error.localizedDescription
    }

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

    func stop(_ session: Int) {
        queue.sync {
            guard let rec = recordings[session] else { return }
            rec.stopped = true
            endTap(session, rec)
            rec.engine?.stop()
        }
        queue.sync {}
    }

    func close(_ session: Int) {
        queue.sync { release(session) }
    }

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

    func convert(_ buffer: AVAudioPCMBuffer, hand: (Data) -> Void) {
        lock.withLock {
            guard !ended, let pcm = Microphone.convert(buffer, with: converter, to: format)
            else { return }
            hand(pcm)
        }
    }

    func finish() -> Data? {
        lock.withLock {
            guard !ended else { return nil }
            ended = true
            return Microphone.end(converter, to: format)
        }
    }
}
