package recording

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Transcription is not set up without the device's engine, a Soniox key
// or an endpoint's address; set up, the status says where it runs.
func TestEngineStatus(t *testing.T) {
	notSetUp := locales.RecSttNotSetUp()
	for _, c := range []struct {
		stt              store.SttSettings
		soniox, onDevice bool
		want             string
	}{
		{store.SttSettings{Provider: OnDevice}, true, false, notSetUp},
		{store.SttSettings{Provider: OnDevice}, false, true, locales.RecEngineOnDevice()},
		{store.SttSettings{Provider: "soniox"}, false, true, notSetUp},
		{store.SttSettings{Provider: "soniox"}, true, false, locales.RecEngineSonioxCloud()},
		{store.SttSettings{Provider: "openai-compatible", BaseURL: "  "}, true, true, notSetUp},
		{store.SttSettings{Provider: "openai-compatible", BaseURL: "http://localhost:8000/v1", Model: "whisper"}, false, false, locales.RecEngineLocal("whisper")},
	} {
		if got := EngineStatus(c.stt, c.soniox, c.onDevice); got != c.want {
			t.Errorf("%+v, soniox %v, on device %v: %q, want %q", c.stt, c.soniox, c.onDevice, got, c.want)
		}
	}
}

// Settings left at an endpoint with no address turn to the device's own
// engine; an engine set up stays.
func TestPreferOnDevice(t *testing.T) {
	stt := store.SttSettings{Provider: "openai-compatible", BaseURL: " "}
	if !PreferOnDevice(&stt) || stt.Provider != OnDevice {
		t.Errorf("an endpoint with no address stays %q", stt.Provider)
	}
	for _, stt := range []store.SttSettings{
		{Provider: "openai-compatible", BaseURL: "http://localhost:8000/v1"},
		{Provider: "soniox"},
		{Provider: OnDevice},
	} {
		was := stt.Provider
		if PreferOnDevice(&stt) || stt.Provider != was {
			t.Errorf("%s turned to %s", was, stt.Provider)
		}
	}
}

// A test of the microphone says what it heard, or why it heard nothing.
func TestHeard(t *testing.T) {
	if got := heard("Hello", nil); got != locales.RecSttHeard("Hello") {
		t.Errorf("heard %q", got)
	}
	if got := heard("", errors.New("no engine")); got != "no engine" {
		t.Errorf("failed %q", got)
	}
}

// mirrorServer keeps the base URLs of the speech settings it is sent, in
// order; hold, while open, holds the send of "https://a".
func mirrorServer(t *testing.T, hold chan struct{}) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var saved []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var row struct {
			BaseURL string `json:"base_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&row)
		if row.BaseURL == "https://a" && hold != nil {
			<-hold
		}
		mu.Lock()
		saved = append(saved, row.BaseURL)
		mu.Unlock()
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	return hs, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(saved)
	}
}

// sent waits for n sends to reach the server, and returns them.
func sent(t *testing.T, saved func() []string, n int) []string {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); len(saved()) < n; {
		if time.Now().After(end) {
			t.Fatalf("sent %v, want %d", saved(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return saved()
}

// Changes made as a field is typed in go to the server as one, once the
// typing pauses: the last.
func TestMirrorSettingsSendsTheLastOfAPause(t *testing.T) {
	hs, saved := mirrorServer(t, nil)
	r, st, _ := newTestController(t)
	r.be = remote.SignedInForTest(hs.URL, st, "u1")
	for _, url := range []string{"https://e", "https://ex", "https://exa"} {
		r.MirrorSettings(store.SttSettings{Provider: "openai-compatible", BaseURL: url})
	}
	time.Sleep(2 * mirrorWait)
	if got := sent(t, saved, 1); !slices.Equal(got, []string{"https://exa"}) {
		t.Errorf("sent %v", got)
	}
}

// The speech settings reach the server one at a time, the last last: sent
// at once, an earlier one could land after it.
func TestMirrorSettingsLastSentLast(t *testing.T) {
	hold := make(chan struct{})
	hs, saved := mirrorServer(t, hold)
	r, st, _ := newTestController(t)
	r.be = remote.SignedInForTest(hs.URL, st, "u1")
	r.MirrorSettings(store.SttSettings{Provider: "openai-compatible", BaseURL: "https://a"})
	// The first is held at the server as the second comes.
	time.Sleep(2 * mirrorWait)
	r.MirrorSettings(store.SttSettings{Provider: "openai-compatible", BaseURL: "https://ab"})
	time.Sleep(2 * mirrorWait)
	close(hold)
	if got := sent(t, saved, 2); !slices.Equal(got, []string{"https://a", "https://ab"}) {
		t.Errorf("the server ended with %v", got)
	}
}

// The device's own engine stays this device's, and signed out there is
// no server to send to.
func TestMirrorSettingsKeepsTheDevicesEngine(t *testing.T) {
	hs, saved := mirrorServer(t, nil)
	r, st, _ := newTestController(t)
	r.MirrorSettings(store.SttSettings{Provider: "soniox"})
	r.be = remote.SignedInForTest(hs.URL, st, "u1")
	r.MirrorSettings(store.SttSettings{Provider: OnDevice})
	time.Sleep(3 * mirrorWait)
	if got := saved(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// memKeys is a keychain in memory.
type memKeys map[string][]byte

func (k memKeys) Get(name string) ([]byte, error) {
	v, ok := k[name]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return v, nil
}

func (k memKeys) Set(name string, value []byte) error {
	k[name] = value
	return nil
}

func (k memKeys) Delete(name string) error {
	delete(k, name)
	return nil
}

// Signed in, the Soniox key goes to the server, for every device, and
// what it refused says so; signed out, it stays in this device's
// keychain.
func TestSaveSonioxKey(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	r, st, _ := newTestController(t)
	if onServer, err := r.SaveSonioxKey("sk-here"); onServer || err != nil || LocalSonioxKey() != "sk-here" {
		t.Errorf("signed out: on the server %v, %v, kept %q", onServer, err, LocalSonioxKey())
	}
	var mu sync.Mutex
	var sent []map[string]any
	down := false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, body)
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"down for maintenance"}`))
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(hs.Close)
	r.be = remote.SignedInForTest(hs.URL, st, "u1")
	if onServer, err := r.SaveSonioxKey("sk-cloud"); !onServer || err != nil {
		t.Fatalf("signed in: on the server %v, %v", onServer, err)
	}
	mu.Lock()
	if len(sent) != 1 || sent[0]["action"] != "save_key" || sent[0]["provider"] != "soniox" || sent[0]["api_key"] != "sk-cloud" {
		t.Errorf("sent %v", sent)
	}
	down = true
	mu.Unlock()
	if _, err := r.SaveSonioxKey("sk-cloud"); err == nil || err.Error() != locales.RecSttKeyNotSaved("down for maintenance") {
		t.Errorf("refused: %v", err)
	}
	if got := LocalSonioxKey(); got != "sk-here" {
		t.Errorf("the key here became %q", got)
	}
}
