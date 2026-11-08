package recording

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings says which providers are set up, by name, or that none is.
func TestSummary(t *testing.T) {
	for _, c := range []struct {
		stt  store.SttSettings
		keys Keys
		want string
	}{
		{store.SttSettings{}, Keys{}, locales.RecSttNotSetUp()},
		{store.SttSettings{BaseURL: "  "}, Keys{}, locales.RecSttNotSetUp()},
		{store.SttSettings{}, Keys{SonioxOnServer: true, MossHere: true}, "Soniox, MOSS"},
		{store.SttSettings{BaseURL: "http://localhost:8000/v1"}, Keys{OnDevice: true}, locales.RecSttOnDevice() + ", " + locales.CommonEndpoint()},
	} {
		if got := Summary(c.stt, c.keys); got != c.want {
			t.Errorf("%+v, %+v: %q, want %q", c.stt, c.keys, got, c.want)
		}
	}
}

// Signed in, Soniox's key counts as on the server once this device saved
// it there, or chose Soniox; signed out, only the one here counts.
func TestKeysOf(t *testing.T) {
	soniox := store.SttSettings{Provider: "soniox"}
	saved := store.SttSettings{Provider: Moss, SonioxOnServer: true}
	if k := KeysOf(soniox, false, false, true, false); !k.SonioxOnServer {
		t.Error("Soniox chosen, signed in: no key on the server")
	}
	if k := KeysOf(saved, false, true, true, false); !k.SonioxOnServer || !k.MossHere {
		t.Errorf("saved on the server: %+v", k)
	}
	if k := KeysOf(saved, false, false, false, true); k.SonioxOnServer || !k.OnDevice {
		t.Errorf("signed out: %+v", k)
	}
	if k := KeysOf(store.SttSettings{Provider: Moss}, false, false, true, false); k.SonioxOnServer {
		t.Error("never saved on the server, nor chosen: a key there")
	}
}

// The list has the providers set up in its order, the others to add, but
// never the device's engine where it has none; Soniox's key on the server
// alone can't be removed from here.
func TestSpeechProviders(t *testing.T) {
	stt := store.SttSettings{Provider: Moss, BaseURL: "http://localhost:8000/v1", Model: "whisper", SonioxModel: "stt-rt-v5"}
	set, addable := SpeechProviders(stt, Keys{SonioxOnServer: true, MossHere: true})
	var ids []string
	for _, p := range set {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, " ") != "soniox moss openai-compatible" {
		t.Errorf("set up %v", ids)
	}
	if set[0].Name != "Soniox · stt-rt-v5" || set[0].Removable || set[0].Place != locales.RecSttSonioxOnServer() {
		t.Errorf("Soniox on the server %+v", set[0])
	}
	if set[2].Name != locales.CommonEndpoint()+" · whisper" || !set[2].Removable {
		t.Errorf("endpoint %+v", set[2])
	}
	if len(addable) != 0 {
		t.Errorf("addable %+v", addable)
	}
	set, addable = SpeechProviders(store.SttSettings{}, Keys{OnDevice: true, SonioxHere: true})
	if len(set) != 2 || set[0].ID != OnDevice || set[0].Removable || !set[1].Removable {
		t.Errorf("set up %+v", set)
	}
	if len(addable) != 2 || addable[0].ID != Moss || addable[1].ID != Endpoint {
		t.Errorf("addable %+v", addable)
	}
}

// Naming a provider not set up, the settings take the first that is; one
// set up stays, and none set up changes nothing.
func TestPreferSetUp(t *testing.T) {
	stt := store.SttSettings{Provider: "soniox"}
	if !PreferSetUp(&stt, Keys{MossHere: true}) || stt.Provider != Moss {
		t.Errorf("took %q", stt.Provider)
	}
	if PreferSetUp(&stt, Keys{MossHere: true, OnDevice: true}) || stt.Provider != Moss {
		t.Errorf("one set up changed to %q", stt.Provider)
	}
	stt = store.SttSettings{Provider: Moss}
	if PreferSetUp(&stt, Keys{}) || stt.Provider != Moss {
		t.Errorf("none set up changed to %q", stt.Provider)
	}
}

// Removing a provider takes its key from this device, or an endpoint's
// address and key, and the settings take another set up.
func TestRemoveProvider(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	for _, set := range []func() error{
		func() error { return SetMossKey("mk") }, func() error { return SetLocalSonioxKey("sk") },
		func() error { return SetEndpointKey("ek") },
	} {
		if err := set(); err != nil {
			t.Fatal(err)
		}
	}
	stt := store.SttSettings{Provider: Moss, BaseURL: "http://localhost:8000/v1", Model: "whisper", ProviderID: "p"}
	keys, err := RemoveProvider(Moss, &stt, Keys{MossHere: true, SonioxHere: true})
	if err != nil || MossKey() != "" || keys.MossHere || stt.Provider != "soniox" {
		t.Errorf("MOSS: %v, key %q, %+v, now %q", err, MossKey(), keys, stt.Provider)
	}
	if _, err := RemoveProvider(Endpoint, &stt, keys); err != nil || stt.BaseURL != "" || stt.Model != "" || stt.ProviderID != "" || EndpointKey() != "" {
		t.Errorf("endpoint: %v, %+v, key %q", err, stt, EndpointKey())
	}
	keys, err = RemoveProvider("soniox", &stt, keys)
	if err != nil || LocalSonioxKey() != "" || keys.SonioxHere {
		t.Errorf("Soniox: %v, key %q", err, LocalSonioxKey())
	}
	// A key already gone is no failure.
	if _, err := RemoveProvider(Moss, &stt, keys); err != nil {
		t.Errorf("again: %v", err)
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

// There is no default: a transcription takes the provider picked for it,
// kept as its page's; unpicked, the one last picked on its page, else
// anywhere, else the first set up.
func TestPickingAProvider(t *testing.T) {
	r, _, _ := newTestController(t)
	keys := Keys{SonioxHere: true, MossHere: true}
	stt := store.SttSettings{Provider: Moss}
	if got := r.Choice("p1", stt, keys); got != Moss {
		t.Errorf("unpicked: %q", got)
	}
	out, last := r.For("p1", "soniox", stt, keys)
	if out.Provider != "soniox" || !last {
		t.Errorf("picked: %q, last %v", out.Provider, last)
	}
	if got := r.Choice("p1", stt, keys); got != "soniox" {
		t.Errorf("the page's pick: %q", got)
	}
	if got := r.Choice("p2", stt, keys); got != Moss {
		t.Errorf("another page: %q", got)
	}
	if out, last := r.For("p2", "", stt, keys); out.Provider != Moss || last {
		t.Errorf("unpicked on another page: %q, last %v", out.Provider, last)
	}
	// A pick whose key went since gives way to one set up.
	if got := r.Choice("p1", stt, Keys{MossHere: true}); got != Moss {
		t.Errorf("Soniox gone: %q", got)
	}
	if got := r.Choice("p3", store.SttSettings{Provider: Endpoint}, Keys{OnDevice: true}); got != OnDevice {
		t.Errorf("none picked set up: %q", got)
	}
	if got := Choices(stt, keys); len(got) != 2 || got[0].ID != "soniox" || got[1].ID != Moss {
		t.Errorf("choices %+v", got)
	}
}
