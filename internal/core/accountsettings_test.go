package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	leafletsync "leaflet/internal/sync"
	"leaflet/locales"
)

type request struct {
	method, path string
	body         map[string]any
}

type accountServer struct {
	mu     sync.Mutex
	heard  []request
	answer string
	down   bool
}

func (s *accountServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heard = append(s.heard, request{r.Method, r.URL.Path, body})
	if s.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"down for maintenance"}`))
		return
	}
	_, _ = w.Write([]byte(s.answer))
}

func (s *accountServer) calls() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.heard
	s.heard = nil
	return out
}

func signedInCore(t *testing.T, s *accountServer) *Core {
	t.Helper()
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	return &Core{Store: st, Remote: remote.SignedInForTest(hs.URL, st, "u1")}
}

func TestDisplayName(t *testing.T) {
	s := &accountServer{answer: "[]"}
	c := signedInCore(t, s)
	if changed, err := c.SetDisplayName("  Ada  "); !changed || err != nil {
		t.Fatalf("changed %v, %v", changed, err)
	}
	for _, name := range []string{"   ", "Ada"} {
		if changed, err := c.SetDisplayName(name); changed || err != nil {
			t.Errorf("%q changed %v, %v", name, changed, err)
		}
	}
	if got := c.Store.Session().DisplayName; got != "Ada" {
		t.Errorf("name %q", got)
	}
	if err := c.SendDisplayName(); err != nil {
		t.Fatal(err)
	}
	calls := s.calls()
	if len(calls) != 2 || calls[0].path != "/auth/v1/user" || calls[1].path != "/rest/v1/profiles" ||
		calls[1].body["id"] != "u1" || calls[1].body["display_name"] != "Ada" {
		t.Errorf("sent %+v", calls)
	}
	s.down = true
	if err := c.SendDisplayName(); err == nil || !strings.HasPrefix(err.Error(), locales.SettingsNameServerFailed("")) {
		t.Errorf("refused: %v", err)
	}
}

func TestDevices(t *testing.T) {
	s := &accountServer{}
	c := signedInCore(t, s)
	me := c.Store.DeviceID()
	s.answer = `[{"id":"` + me + `","name":"This Mac","last_active_at":"2026-11-02T10:00:00Z"},` +
		`{"id":"d2","name":"Old iPad","last_active_at":"2026-11-01T10:00:00Z","revoked_at":"2026-11-01T11:00:00Z"}]`
	ds, err := c.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || !ds[0].This || ds[0].Revoked || ds[1].This || !ds[1].Revoked || ds[1].Name != "Old iPad" ||
		!ds[0].LastActive.Equal(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("devices %+v", ds)
	}
	if calls := s.calls(); len(calls) != 1 || calls[0].path != "/rest/v1/devices" {
		t.Errorf("asked %+v", calls)
	}

	s.answer = ""
	if err := c.SignOutDevice("d3"); err != nil {
		t.Fatal(err)
	}
	if calls := s.calls(); len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/rest/v1/rpc/sign_out_device" ||
		len(calls[0].body) != 1 || calls[0].body["p_device_id"] != "d3" {
		t.Errorf("sent %+v", calls)
	}
	s.down = true
	if err := c.SignOutDevice("d3"); err == nil || !strings.HasPrefix(err.Error(), locales.SettingsDeviceSignOutFailed("")) {
		t.Errorf("refused: %v", err)
	}
}

func TestDeleteAccount(t *testing.T) {
	keys := memSecrets{}
	keychain.Use(keys)
	t.Cleanup(func() { keychain.Use(nil) })
	s := &accountServer{answer: "{}", down: true}
	c := signedInCore(t, s)
	c.Hub = agents.NewHub(c.Store, c.Remote, nil)
	if err := c.Store.PutProvider(store.Provider{ID: "here", Preset: "openai", AuthKind: "api_key"}); err != nil {
		t.Fatal(err)
	}
	if err := agents.SetLocalKey("here", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := recording.SetEndpointKey("https://api.example.com/v1", "sk-2"); err != nil {
		t.Fatal(err)
	}
	if err := recording.SetLocalSonioxKey("sk-3"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAccount(); err == nil || !strings.Contains(err.Error(), "down for maintenance") {
		t.Errorf("refused: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("the keys went with an account that stayed: %v", keys)
	}
	s.down = false
	if err := c.DeleteAccount(); err != nil {
		t.Fatal(err)
	}
	calls := s.calls()
	if last := calls[len(calls)-1]; last.path != "/functions/v1/account" || last.body["action"] != "delete" {
		t.Errorf("sent %+v", calls)
	}
	if len(keys) != 0 {
		t.Errorf("the keychain kept %v", keys)
	}
}

type memSecrets map[string][]byte

func (m memSecrets) Get(name string) ([]byte, error) {
	if v, ok := m[name]; ok {
		return v, nil
	}
	return nil, keychain.ErrNotFound
}

func (m memSecrets) Set(name string, value []byte) error {
	m[name] = value
	return nil
}

func (m memSecrets) Delete(name string) error {
	delete(m, name)
	return nil
}

func TestLeavingRevokesThisDevicesRow(t *testing.T) {
	for _, down := range []bool{false, true} {
		s := &accountServer{answer: "null", down: down}
		c := signedInCore(t, s)
		id := c.Store.DeviceID()
		c.Sync = leafletsync.New(c.Store, c.Remote)
		if _, err := c.Leave(c.Store.ForgetAccount, false); err != nil {
			t.Fatal(err)
		}
		heard := s.calls()
		if len(heard) == 0 || heard[0].path != "/rest/v1/rpc/sign_out_device" || heard[0].body["p_device_id"] != id {
			t.Errorf("down %v: heard %+v", down, heard)
		}
		if c.Remote.SignedIn() {
			t.Errorf("down %v: still signed in", down)
		}
	}
}

func TestLeavingWithNoDeviceAsksNothing(t *testing.T) {
	s := &accountServer{answer: "null"}
	c := signedInCore(t, s)
	c.Sync = leafletsync.New(c.Store, c.Remote)
	if _, err := c.Leave(c.Store.ForgetAccount, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.calls() {
		if r.path == "/rest/v1/rpc/sign_out_device" {
			t.Errorf("asked %+v", r)
		}
	}
}
