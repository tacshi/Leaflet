package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

// request is a call the server heard: its method and path, and its body.
type request struct {
	method, path string
	body         map[string]any
}

// accountServer is a server that keeps the calls it hears and answers
// them with answer, or refuses them all while down.
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

// calls returns the calls heard, and forgets them.
func (s *accountServer) calls() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.heard
	s.heard = nil
	return out
}

// signedInCore is a Core signed in as u1 to the server s.
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

// The name is kept here without the spaces around it, and none, or the
// same, changes nothing; then it goes to the account and its profile, and
// what the server refused says so.
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

// The devices list this one and those signed out from another; signing
// one out marks it so on the server.
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

	s.answer = "[]"
	if err := c.SignOutDevice("d3"); err != nil {
		t.Fatal(err)
	}
	if calls := s.calls(); len(calls) != 1 || calls[0].body["id"] != "d3" || calls[0].body["user_id"] != "u1" || calls[0].body["revoked_at"] == nil {
		t.Errorf("sent %+v", calls)
	}
	s.down = true
	if err := c.SignOutDevice("d3"); err == nil || !strings.HasPrefix(err.Error(), locales.SettingsDeviceSignOutFailed("")) {
		t.Errorf("refused: %v", err)
	}
}

// Deleting the account asks the server to, and says why it could not.
func TestDeleteAccount(t *testing.T) {
	s := &accountServer{answer: "{}"}
	c := signedInCore(t, s)
	if err := c.DeleteAccount(); err != nil {
		t.Fatal(err)
	}
	if calls := s.calls(); len(calls) != 1 || calls[0].path != "/functions/v1/account" || calls[0].body["action"] != "delete" {
		t.Errorf("sent %+v", calls)
	}
	s.down = true
	if err := c.DeleteAccount(); err == nil || !strings.Contains(err.Error(), "down for maintenance") {
		t.Errorf("refused: %v", err)
	}
}
