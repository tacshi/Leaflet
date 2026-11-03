package recording

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Speech to text, as the desktop and the iOS app have it: where
// transcription runs, the device's own engine by default, the test of the
// microphone, and what goes to the server: the Soniox key and the
// settings.

// EngineStatus says where transcription runs, or that it is not set up:
// the device's engine on one without it (onDevice), Soniox without a key
// here or on the server (hasSonioxKey), or an endpoint with no address.
func EngineStatus(stt store.SttSettings, hasSonioxKey, onDevice bool) string {
	if stt.Provider == OnDevice && !onDevice ||
		stt.Provider == "soniox" && !hasSonioxKey ||
		stt.Provider == "openai-compatible" && strings.TrimSpace(stt.BaseURL) == "" {
		return locales.RecSttNotSetUp()
	}
	return EngineLabel(stt)
}

// PreferOnDevice turns speech settings that name no engine, an endpoint
// with no address, to the device's own, once the device has one: such a
// device transcribes with it unless another engine is set up. It reports
// whether they changed, to be saved.
func PreferOnDevice(stt *store.SttSettings) bool {
	if stt.Provider != "openai-compatible" || strings.TrimSpace(stt.BaseURL) != "" {
		return false
	}
	stt.Provider = OnDevice
	return true
}

// MicTest is how long Settings' test of the microphone records.
const MicTest = 4 * time.Second

// TestMicrophone records MicTest of audio into no page and transcribes it
// with stt, for Settings to try the microphone and the engine. done runs
// on the main thread with what it heard, or why it heard nothing, in
// words for the user.
func (r *Controller) TestMicrophone(stt store.SttSettings, online bool, done func(heard string)) error {
	return r.Test(stt, online, MicTest, func(text string, err error) { done(heard(text, err)) })
}

// heard says what a test of the microphone heard, or why it heard nothing.
func heard(text string, err error) string {
	if err != nil {
		return err.Error()
	}
	return locales.RecSttHeard(text)
}

// SaveSonioxKey keeps a Soniox key on the server, signed in, which then
// transcribes with it for every device, or else in this device's
// keychain; onServer says which. What failed it says in words for the
// user.
func (r *Controller) SaveSonioxKey(key string) (onServer bool, err error) {
	if !r.be.SignedIn() {
		if err := SetLocalSonioxKey(key); err != nil {
			return false, errors.New(locales.RecSttKeyNotSaved(err.Error()))
		}
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := r.be.Invoke(ctx, "stt", map[string]any{"action": "save_key", "provider": "soniox", "api_key": key}); err != nil {
		return true, errors.New(locales.RecSttKeyNotSaved(err.Error()))
	}
	return true, nil
}

// mirrorWait is how long a change of the speech settings waits for the
// next before it goes to the server: each key typed in a field changes
// them.
const mirrorWait = 500 * time.Millisecond

// MirrorSettings sends the speech settings to the server, signed in, which
// transcribes with them for every device. The device's own engine is this
// device's alone: the server's settings stay those the other devices
// chose. Only the last change goes, once the changes pause, and the sends
// go one at a time, so the server never ends with older ones. Any
// goroutine may call it.
func (r *Controller) MirrorSettings(stt store.SttSettings) {
	be := r.be
	if !be.SignedIn() || stt.Provider == OnDevice {
		return
	}
	n := r.sttLatest.Add(1)
	time.AfterFunc(mirrorWait, func() {
		defer guard.Recover()
		if r.sttLatest.Load() != n {
			return
		}
		r.sttSent.Lock()
		defer r.sttSent.Unlock()
		if r.sttLatest.Load() != n {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var provider any
		if stt.ProviderID != "" {
			provider = stt.ProviderID
		}
		row := map[string]any{"user_id": be.UserID(), "provider": stt.Provider, "language": stt.Language,
			"live_transcript": stt.LiveTranscript, "speaker_labels": stt.SpeakerLabels,
			"base_url": stt.BaseURL, "model": stt.Model, "provider_id": provider}
		if _, err := be.Upsert(ctx, "stt_settings", row, "user_id"); err != nil {
			log.Printf("stt settings: %v", err)
		}
	})
}
