package recording

import (
	"encoding/json"
	"errors"
	"path"
	"strings"

	"leaflet/internal/store"
	"leaflet/locales"
)

func OnDeviceReads(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".aac", ".aif", ".aiff", ".caf", ".flac", ".m4a", ".mp3", ".mp4", ".wav":
		return true
	}
	return false
}

type OnDeviceAnswer struct {
	Text     *string         `json:"text"`
	Done     bool            `json:"done"`
	Segments []store.Segment `json:"segments"`
	Error    string          `json:"error"`
	Code     string          `json:"code"`
	Language string          `json:"language"`
}

func (a OnDeviceAnswer) Err(language string) error {
	switch a.Code {
	case "language":
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
