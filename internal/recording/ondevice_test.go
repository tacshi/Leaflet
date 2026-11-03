package recording

import (
	"testing"

	"leaflet/locales"
)

// A device's engine answers with its transcript, tidied, or with a code
// that the app says why by, in the user's language, on the Mac and on iOS
// alike: a language it can't transcribe by its name, the device's own
// when it asked for that, and a transcription that stopped going on.
func TestOnDeviceAnswers(t *testing.T) {
	segs, err := OnDeviceTranscript(`{"done":true,"segments":[{"start_ms":0,"end_ms":900,"text":"Hello"},{"start_ms":900,"end_ms":1000,"text":"."}]}`, "en")
	if err != nil || len(segs) != 1 || segs[0].Text != "Hello." {
		t.Errorf("transcript %+v, %v", segs, err)
	}
	cases := []struct{ raw, language, want string }{
		{`{"error":"unsupported language: fr","code":"language","language":"fr"}`, "fr", locales.RecErrOnDeviceLanguage(LanguageName("fr"))},
		{`{"error":"unsupported language: de_CH","code":"language","language":"de"}`, "auto", locales.RecErrOnDeviceLanguage(LanguageName("de"))},
		{`{"error":"unsupported language: xx","code":"language"}`, "xx", locales.RecErrOnDeviceLanguage("xx")},
		{`{"error":"the transcription stopped going on","code":"stuck"}`, "en", locales.RecErrOnDeviceStuck()},
		{`{"error":"The file could not be read."}`, "en", "The file could not be read."},
		{`{"text":"so far"}`, "en", locales.RecErrOnDeviceEnded()},
	}
	for _, c := range cases {
		if _, err := OnDeviceTranscript(c.raw, c.language); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.raw, err, c.want)
		}
	}
}
