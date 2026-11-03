package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// Signing in uploads the audio of recordings made before, once they are
// done, and none whose audio is gone.
func TestSignInUploadsEarlierRecordings(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Standup"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	recording := func(name, status string, keep bool) store.Recording {
		path := dir + "/" + name + ".wav"
		if keep {
			if err := os.WriteFile(path, []byte("RIFF"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		r, err := st.CreateRecording(p.ID, name, "", "", path, "")
		if err != nil {
			t.Fatal(err)
		}
		r, err = st.UpdateRecording(r.ID, func(r *store.Recording) { r.Status = status })
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	done := recording("done", "ready", true)
	recording("live", "recording", true)
	recording("gone", "ready", false)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "00000000-0000-0000-0000-0000000000aa", "name": "My space"}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.resolveSpace(context.Background()); err != nil {
		t.Fatal(err)
	}
	var uploads []string
	entries, _ := st.Pending()
	for _, e := range entries {
		if e.Entity == "storage" {
			uploads = append(uploads, e.EntityID)
		}
	}
	if strings.Join(uploads, ",") != "recordings/"+done.ID {
		t.Errorf("uploads %v", uploads)
	}
}

// Signing in to an account whose library other devices made leaves this
// device's starter pages out of it; signing in to one with no pages yet
// keeps them as its own, though the server made its space at sign-up,
// unless another device signing in at the same time claimed it first. A
// space another device made meanwhile is joined. The device's other pages
// go up either way.
func TestSignInSettlesStarterPages(t *testing.T) {
	const remoteID = "00000000-0000-0000-0000-0000000000aa"
	for _, c := range []struct {
		name string
		// made says the server has the space from the start, else it has
		// none until the insert, which another device's beats; claim is
		// what the server answers this device's claim, "" when it must
		// not be asked.
		made, pages, kept bool
		claim             string
	}{
		{"library with pages", true, true, false, ""},
		{"new account", true, false, true, "true"},
		{"new account another device claimed", true, false, false, "false"},
		{"space made meanwhile", false, true, false, ""},
	} {
		st := openStore(t)
		guide, err := st.CreatePage(store.NewPageOptions{Title: "Welcome"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MarkStarterPages([]string{guide.ID}); err != nil {
			t.Fatal(err)
		}
		mine, err := st.CreatePage(store.NewPageOptions{Title: "Mine"})
		if err != nil {
			t.Fatal(err)
		}
		made := c.made
		var claimed map[string]any
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/rest/v1/rpc/claim_starter_pages":
				_ = json.NewDecoder(r.Body).Decode(&claimed)
				_, _ = io.WriteString(w, c.claim)
			case r.URL.Path == "/rest/v1/spaces" && r.Method == http.MethodPost:
				made = true
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"23505","message":"duplicate key value violates unique constraint \"spaces_one_per_owner\""}`))
			case r.URL.Path == "/rest/v1/spaces" && made:
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": remoteID, "name": "My space"}})
			case r.URL.Path == "/rest/v1/pages" && c.pages && r.URL.Query().Get("space_id") == "eq."+remoteID:
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "00000000-0000-0000-0000-0000000000bb"}})
			default:
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
		}))
		svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
		if err := svc.resolveSpace(context.Background()); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		hs.Close()
		if _, err := st.Page(guide.ID); store.IsNotFound(err) == c.kept {
			t.Errorf("%s: the starter page went %v", c.name, store.IsNotFound(err))
		}
		if !st.HasPending("pages", mine.ID) || st.Session().SpaceID != remoteID {
			t.Errorf("%s: the device's own page does not go up", c.name)
		}
		if want := c.claim != ""; (claimed != nil) != want ||
			want && (claimed["p_space_id"] != remoteID || claimed["p_device"] != st.DeviceID()) {
			t.Errorf("%s: claimed %v", c.name, claimed)
		}
	}
}

// A device whose starter pages are settled, as when it joined a library
// before, claims no new library for them: the device that has some does.
func TestSignInWithoutStarterPagesClaimsNothing(t *testing.T) {
	st := openStore(t)
	claimed := false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/v1/rpc/claim_starter_pages":
			claimed = true
			_, _ = io.WriteString(w, "true")
		case "/rest/v1/spaces":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "00000000-0000-0000-0000-0000000000aa", "name": "My space"}})
		default:
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.resolveSpace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("a device with no starter pages claimed the library")
	}
}

// Signing in adopts the device's space only once everything in it is
// queued: a pass that could not queue it all leaves the space to adopt
// again, and what was not queued is queued before the next pass. What
// is made meanwhile goes into the account's space.
func TestSignInAdoptsOnceAllIsQueued(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	// The disk refuses the folder's entry, as one that is full would.
	if _, err := st.DB().Exec(`CREATE TRIGGER refuse BEFORE INSERT ON outbox WHEN NEW.entity = 'folders'
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	const remoteID = "00000000-0000-0000-0000-0000000000aa"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": remoteID, "name": "My space"}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	if err := svc.resolveSpace(ctx); !errors.Is(err, errNotRequeued) {
		t.Fatalf("resolveSpace: %v", err)
	}
	if _, ok := st.Value("space.u1"); ok {
		t.Error("the space counts as adopted with the folder not queued")
	}
	if !st.HasPending("pages", p.ID) || st.HasPending("folders", f.ID) {
		t.Errorf("page queued %v, folder queued %v", st.HasPending("pages", p.ID), st.HasPending("folders", f.ID))
	}
	if sess := st.Session(); sess.SpaceID != remoteID || !sess.SignedIn {
		t.Errorf("session %+v", sess)
	}
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := st.AddFile(src, "", "", "text/plain")
	if err != nil || added.SpaceID != remoteID {
		t.Errorf("file added meanwhile in %q: %v", added.SpaceID, err)
	}

	if _, err := st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if err := svc.retryMoves(ctx); err != nil {
		t.Fatal(err)
	}
	if !st.HasPending("folders", f.ID) {
		t.Error("the folder is not queued before the next pass")
	}
	if v, _ := st.Value(requeueKey); v != "" {
		t.Errorf("%s is %q after all went", requeueKey, v)
	}
	if err := svc.resolveSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Value("space.u1"); v != remoteID {
		t.Errorf("space.u1 is %q", v)
	}
}

// Sending everything again sends a deleted file's row, so its deletion
// still reaches the other devices, but none of its contents, and a deleted
// folder's.
func TestRequeueSendsDeletedFilesAndFolders(t *testing.T) {
	st := openStore(t)
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	folder, err := st.CreateFolder("Old")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFolder(folder); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, remote.New(remote.Settings{}, st))
	if err := svc.requeueAll(context.Background(), st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var queued []string
	entries, _ = st.Pending()
	for _, e := range entries {
		if e.EntityID == f.ID || strings.HasSuffix(e.EntityID, "/"+f.ID) {
			queued = append(queued, e.Entity)
		}
	}
	if strings.Join(queued, ",") != "files" {
		t.Errorf("queued %v for the deleted file", queued)
	}
	if pendingOf(t, st, "folders", folder.ID) == nil {
		t.Error("the deleted folder is not queued")
	}
}
