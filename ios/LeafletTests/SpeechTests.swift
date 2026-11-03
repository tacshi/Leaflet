import Foundation
import Testing

@testable import LeafletSpeech

/// SpeechTests cover the device's own transcription as the core reads
/// it: what it answers, which the Mac's speech helper answers alike, and
/// a language the device can't transcribe, which the core names from its
/// code in the user's language.
struct SpeechTests {
    /// A transcript, and why one could not be made, read as the core reads
    /// them (recording.OnDeviceAnswer): a failure the core can say has its
    /// code, and its language's.
    @Test func answersAreTheCoresToRead() throws {
        let text = { (answer: Data) in String(decoding: answer, as: UTF8.self) }
        #expect(
            text(Answer.done([SpeechSegment(startMs: 0, endMs: 1500, text: "Hello")]))
                == #"{"done":true,"segments":[{"end_ms":1500,"start_ms":0,"text":"Hello"}]}"#)
        #expect(text(Answer.words("so far")) == #"{"text":"so far"}"#)
        let language = SpeechFailure(
            message: "unsupported language: xx", code: "language", language: "xx")
        #expect(
            text(Answer.failed(language))
                == #"{"code":"language","error":"unsupported language: xx","language":"xx"}"#)
        #expect(
            text(Answer.failed(SpeechFailure.stuck))
                == #"{"code":"stuck","error":"the transcription stopped going on"}"#)
        let other = text(Answer.failed(CocoaError(.fileReadNoSuchFile)))
        let failure = try #require(
            try JSONSerialization.jsonObject(with: Data(other.utf8)) as? [String: String])
        #expect(failure["code"] == nil && failure["error"]?.isEmpty == false)
    }

    /// A language the device can't transcribe fails with its code, for the
    /// core to name it, as on the Mac; one it can is its locale.
    @Test func aLanguageTheDeviceCantTranscribeHasItsCode() async throws {
        do {
            _ = try await Transcription.locale(for: "xx")
            Issue.record("a language no device knows was taken")
        } catch let failure as SpeechFailure {
            #expect(failure.code == "language" && failure.language == "xx")
        }
        if let english = try? await Transcription.locale(for: "en") {
            #expect(english.language.languageCode?.identifier == "en")
        }
    }
}
