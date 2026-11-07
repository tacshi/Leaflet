package recording

import (
	"encoding/json"
	"errors"
	"path"
	"strings"

	"leaflet/internal/store"
	"leaflet/locales"
)

// OnDeviceReads reports whether the device's engine can read an audio file
// of name, as Apple's audio files can: on any other it fails with nothing
// to say why.
func OnDeviceReads(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".aac", ".aif", ".aiff", ".caf", ".flac", ".m4a", ".mp3", ".mp4", ".wav":
		return true
	}
	return false
}

// OnDeviceAnswer is what a device's own speech engine says of a
// transcription (speech/, Leaflet's Swift package): a line of it each, as
// the Mac's helper writes them (internal/macspeech), or the last, as the
// iOS app's engine returns it (mobile.Transcriber). It is the words so
// far, the transcript once done, or why it could not transcribe, with a
// code that names the reason for the app to say it in the user's
// language.
type OnDeviceAnswer struct {
	Text     *string         `json:"text"`
	Done     bool            `json:"done"`
	Segments []store.Segment `json:"segments"`
	Error    string          `json:"error"`
	// Code is "language" when the device cannot transcribe the language,
	// which Language names by its code, and "stuck" when its
	// transcription stopped going on and was given up.
	Code     string `json:"code"`
	Language string `json:"language"`
}

// Err is why the engine could not transcribe language ("auto" for the
// device's own), in the user's language where its code names the reason.
func (a OnDeviceAnswer) Err(language string) error {
	switch a.Code {
	case "language":
		// "auto" is the device's own language, which the engine names.
		code := language
		if a.Language != "" {
			code = a.Language
		}
		name := code
		if _, ok := Languages[code]; ok {
			name = LanguageName(code)
		}
		return errors.New(locales.RecErrOnDeviceLanguage(name))
	case "stuck":
		return errors.New(locales.RecErrOnDeviceStuck())
	}
	return errors.New(a.Error)
}

// OnDeviceTranscript reads an engine's last answer of a transcription in
// language: its transcript, tidied, or why it failed.
func OnDeviceTranscript(raw, language string) ([]store.Segment, error) {
	var a OnDeviceAnswer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	switch {
	case a.Error != "":
		return nil, a.Err(language)
	case !a.Done:
		return nil, errors.New(locales.RecErrOnDeviceEnded())
	}
	return Tidy(a.Segments), nil
}
