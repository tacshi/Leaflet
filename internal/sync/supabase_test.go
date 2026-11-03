package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// A device revoked in Account › Devices signs out, the app's session too,
// and forgets its ID: signing in again, it registers as a new device, not
// as the one revoked, which would sign it out again on every pass. What
// iCloud sync kept of the old ID goes too, so iCloud sync under the new
// one starts afresh, but for the folder's space, which what went up to it
// is fetched from.
func TestRevokedDeviceRegistersAnew(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"icloud.seq": "5", "icloud.space": "s", "icloud.log_from": "3", "icloud.device_at": "1"} {
		if err := st.SetValue(k, v); err != nil {
			t.Fatal(err)
		}
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"revoked_at": "2026-01-01T00:00:00Z"}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.ICloud = icloud.Folder{Root: t.TempDir()}
	was := st.DeviceID()
	if err := svc.heartbeat(context.Background()); !errors.Is(err, remote.ErrSignedOut) {
		t.Fatalf("heartbeat: %v", err)
	}
	if now := st.DeviceID(); now == was {
		t.Errorf("the device kept its revoked ID %s", was)
	}
	if st.Session().SignedIn {
		t.Error("the app still shows the account signed in")
	}
	if kept := st.ValuesWithPrefix("icloud."); len(kept) != 1 || kept["fetch_space"] != "s" {
		t.Errorf("iCloud sync kept %v of the old ID", kept)
	}
	if !svc.joinedICloud() {
		t.Error("what went up to iCloud Drive can no longer be fetched")
	}
}

// While the project is brought up to date, the changes wait to go up, and
// go once it is.
func TestChangesWaitForTheProjectUpdate(t *testing.T) {
	st := openStore(t)
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Plan"}); err != nil {
		t.Fatal(err)
	}
	// The space is adopted, as after the first pass.
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	sent := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/rest/v1/pages") {
			sent++
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	updating := true
	svc.Updating = func() bool { return updating }
	_ = svc.SyncNow()
	mu.Lock()
	if sent != 0 {
		t.Errorf("%d pages went up while the project was updated", sent)
	}
	mu.Unlock()
	updating = false
	_ = svc.SyncNow()
	mu.Lock()
	defer mu.Unlock()
	if sent == 0 {
		t.Error("the page never went up")
	}
}
