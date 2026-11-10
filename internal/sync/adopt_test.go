package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

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

func TestSignInSettlesStarterPages(t *testing.T) {
	const remoteID = "00000000-0000-0000-0000-0000000000aa"
	for _, c := range []struct {
		name              string
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

func TestSignInAgainAfterICloudSendsAllAgain(t *testing.T) {
	d := newCloudDevice(t, cloudRoot(t))
	space := d.st.Session().SpaceID
	for k, v := range map[string]string{"account": "u1", "space.u1": space} {
		if err := d.st.SetValue(k, v); err != nil {
			t.Fatal(err)
		}
	}
	p := d.page("Plan", para("a", "one"))
	d.sync()
	if d.st.HasPending("pages", p.ID) || d.st.HasPending("page_content", p.ID) {
		t.Fatal("the page did not go through iCloud Drive")
	}
	if _, ok := d.st.Value("space.u1"); ok {
		t.Error("the account's space still counts as adopted")
	}

	d.svc.SetICloud(false)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": space, "name": "My space"}})
	}))
	defer hs.Close()
	svc := New(d.st, remote.SignedInForTest(hs.URL, d.st, "u1"))
	if err := svc.resolveSpace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.st.HasPending("pages", p.ID) || !d.st.HasPending("page_content", p.ID) {
		t.Error("the page is not sent to the server again")
	}
	if v, _ := d.st.Value("space.u1"); v != space {
		t.Errorf("space.u1 is %q", v)
	}
}

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

func TestRetryMovesKeepsWhatItCannotRead(t *testing.T) {
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
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, remote.New(remote.Settings{}, st))
	if err := svc.setIDs(movesKey, map[string]bool{"files:" + f.ID: true, "recordings:gone": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE files RENAME TO files_away`); err != nil {
		t.Fatal(err)
	}
	if err := svc.retryMoves(context.Background()); err == nil {
		t.Error("the retry read no files, and said nothing")
	}
	if got := idsOf(t, svc, movesKey); !maps.Equal(got, map[string]bool{"files:" + f.ID: true}) {
		t.Errorf("waiting for %v", got)
	}

	if _, err := st.DB().Exec(`ALTER TABLE files_away RENAME TO files`); err != nil {
		t.Fatal(err)
	}
	if err := svc.retryMoves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !st.HasPending("files", f.ID) {
		t.Error("the deleted file is not queued")
	}
	if got := idsOf(t, svc, movesKey); len(got) != 0 {
		t.Errorf("still waiting for %v", got)
	}
}
