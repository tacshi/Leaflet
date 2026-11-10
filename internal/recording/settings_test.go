package recording

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestSummary(t *testing.T) {
	for _, c := range []struct {
		stt  store.SttSettings
		keys Keys
		want string
	}{
		{store.SttSettings{}, Keys{}, locales.RecSttNotSetUp()},
		{store.SttSettings{BaseURL: "  "}, Keys{}, locales.RecSttNotSetUp()},
		{store.SttSettings{}, Keys{SonioxHere: true, MossHere: true}, "Soniox, MOSS"},
		{store.SttSettings{BaseURL: "http://localhost:8000/v1"}, Keys{OnDevice: true}, locales.RecSttOnDevice() + ", " + locales.CommonEndpoint()},
	} {
		if got := Summary(c.stt, c.keys); got != c.want {
			t.Errorf("%+v, %+v: %q, want %q", c.stt, c.keys, got, c.want)
		}
	}
}

func TestKeysOf(t *testing.T) {
	var stt store.SttSettings
	old := `{"provider":"soniox","soniox_on_server":true,"provider_id":"p1","base_url":"https://api.example.com/v1"}`
	if err := json.Unmarshal([]byte(old), &stt); err != nil || stt.Provider != "soniox" || stt.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("read %+v, %v", stt, err)
	}
	if k := KeysOf(false, true, true); SetUp("soniox", stt, k) || !k.MossHere || !k.OnDevice {
		t.Errorf("no key here: %+v", k)
	}
	if k := KeysOf(true, false, false); !SetUp("soniox", stt, k) || k.MossHere || k.OnDevice {
		t.Errorf("a key here: %+v", k)
	}
}

func TestSpeechProviders(t *testing.T) {
	stt := store.SttSettings{Provider: Moss, BaseURL: "http://localhost:8000/v1", Model: "whisper", SonioxModel: "stt-rt-v6"}
	set, addable := SpeechProviders(stt, Keys{SonioxHere: true, MossHere: true})
	var ids []string
	for _, p := range set {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, " ") != "soniox moss openai-compatible" {
		t.Errorf("set up %v", ids)
	}
	if set[0].Name != "Soniox · stt-rt-v6" || !set[0].Removable || set[0].Place != locales.RecSttSonioxOnDevice() {
		t.Errorf("Soniox %+v", set[0])
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

func TestRemoveProvider(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	for _, set := range []func() error{
		func() error { return SetMossKey("mk") }, func() error { return SetLocalSonioxKey("sk") },
		func() error { return SetEndpointKey("http://localhost:8000/v1", "ek") },
	} {
		if err := set(); err != nil {
			t.Fatal(err)
		}
	}
	stt := store.SttSettings{Provider: Moss, BaseURL: "http://localhost:8000/v1", Model: "whisper"}
	keys, err := RemoveProvider(Moss, &stt, Keys{MossHere: true, SonioxHere: true})
	if err != nil || MossKey() != "" || keys.MossHere || stt.Provider != "soniox" {
		t.Errorf("MOSS: %v, key %q, %+v, now %q", err, MossKey(), keys, stt.Provider)
	}
	if _, err := RemoveProvider(Endpoint, &stt, keys); err != nil || stt.BaseURL != "" || stt.Model != "" || LoadEndpointKey() != (KeptKey{}) {
		t.Errorf("endpoint: %v, %+v, key %+v", err, stt, LoadEndpointKey())
	}
	keys, err = RemoveProvider("soniox", &stt, keys)
	if err != nil || LocalSonioxKey() != "" || keys.SonioxHere {
		t.Errorf("Soniox: %v, key %q", err, LocalSonioxKey())
	}
	if _, err := RemoveProvider(Moss, &stt, keys); err != nil {
		t.Errorf("again: %v", err)
	}
}

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

func TestHeard(t *testing.T) {
	if got := heard("Hello", nil); got != locales.RecSttHeard("Hello") {
		t.Errorf("heard %q", got)
	}
	if got := heard("", errors.New("no engine")); got != "no engine" {
		t.Errorf("failed %q", got)
	}
}

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

func TestSaveSonioxKey(t *testing.T) {
	keychain.Use(memKeys{})
	t.Cleanup(func() { keychain.Use(nil) })
	if err := SaveSonioxKey("sk-here"); err != nil || LocalSonioxKey() != "sk-here" {
		t.Errorf("saved: %v, kept %q", err, LocalSonioxKey())
	}
	if err := Usable(store.SttSettings{Provider: "soniox"}, true); err != nil {
		t.Errorf("not usable with its key here: %v", err)
	}
}

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
