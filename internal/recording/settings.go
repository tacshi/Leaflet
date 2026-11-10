package recording

import (
	"errors"
	"strings"
	"time"

	"leaflet/internal/store"
	"leaflet/locales"
)

func Summary(stt store.SttSettings, k Keys) string {
	var names []string
	for _, p := range Choices(stt, k) {
		names = append(names, ProviderName(p.ID))
	}
	if len(names) == 0 {
		return locales.RecSttNotSetUp()
	}
	return strings.Join(names, ", ")
}

func KeysOf(sonioxHere, mossHere, onDevice bool) Keys {
	return Keys{SonioxHere: sonioxHere, MossHere: mossHere, OnDevice: onDevice}
}

func PreferOnDevice(stt *store.SttSettings) bool {
	if stt.Provider != "openai-compatible" || strings.TrimSpace(stt.BaseURL) != "" {
		return false
	}
	stt.Provider = OnDevice
	return true
}

const MicTest = 4 * time.Second

func (r *Controller) TestMicrophone(stt store.SttSettings, online bool, done func(heard string)) error {
	return r.Test(stt, online, MicTest, func(text string, err error) { done(heard(text, err)) })
}

func heard(text string, err error) string {
	if err != nil {
		return err.Error()
	}
	return locales.RecSttHeard(text)
}

func SaveSonioxKey(key string) error {
	if err := SetLocalSonioxKey(key); err != nil {
		return errors.New(locales.RecSttKeyNotSaved(err.Error()))
	}
	return nil
}
