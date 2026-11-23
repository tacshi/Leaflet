package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func TestICloudCarriesTheBin(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plans", &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}})
	a.edit(p.ID, "b1", "secret")
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	onPage, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if b.text(p.ID, "b1") != "secret" {
		t.Fatal("B has not the page")
	}
	b.edit(p.ID, "b1", "secret too")
	syncAll(a, b)

	if _, err := a.st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	items, err := b.st.Bin()
	if err != nil || len(items) != 2 {
		t.Fatalf("B's bin %+v, %v", items, err)
	}
	blob, _ := icloud.FileBlobPath(f.ID, f.Name)
	if !a.svc.folder().Exists(blob) {
		t.Fatal("a file in the bin lost its contents")
	}
	if err := b.st.RestoreFile(f.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if got, err := a.st.File(f.ID); err != nil || !got.DeletedAt.IsZero() {
		t.Fatalf("A has %+v after B restored the file, %v", got, err)
	}

	if err := a.st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := b.st.EmptyBin(); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	for _, d := range []*cloudDevice{a, b} {
		if !d.st.IsPurged(p.ID) {
			t.Errorf("page not purged on %s", d.st.DeviceID())
		}
		if got := d.st.Content(p.ID); got != nil {
			t.Errorf("content kept on %s", d.st.DeviceID())
		}
		if items, _ := d.st.Bin(); len(items) != 0 {
			t.Errorf("bin of %s: %+v", d.st.DeviceID(), items)
		}
		rel, _ := icloud.PagePath(d.st.DeviceID(), p.ID)
		if d.svc.folder().Exists(rel) {
			t.Errorf("the page file of %s stayed", d.st.DeviceID())
		}
		if got, err := d.st.File(f.ID); err != nil || got.PurgedAt.IsZero() {
			t.Errorf("file on %s: %+v, %v", d.st.DeviceID(), got, err)
		}
	}
	for _, x := range []store.File{f, onPage} {
		blob, _ := icloud.FileBlobPath(x.ID, x.Name)
		if a.svc.folder().Exists(blob) {
			t.Errorf("the blob of %s stayed", x.ID)
		}
	}
}

func TestPurgedPageGoesUpAndTakesItsObjects(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecording(store.Recording{ID: "rec1", PageID: p.ID, BlockID: "b", AudioPath: "u1/rec1.wav", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecording(store.Recording{ID: "rec2", PageID: p.ID, BlockID: "b2", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var patched map[string]any
	removed := map[string][]string{}
	failPage := true
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch:
			if failPage {
				failPage = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&patched)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": p.ID}})
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/"):
			var body struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			bucket := strings.TrimPrefix(r.URL.Path, "/storage/v1/object/")
			removed[bucket] = append(removed[bucket], body.Prefixes...)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	// While the page purge has not gone up yet, its files and recordings must not be removed.
	if err := svc.push(context.Background()); err == nil {
		t.Fatal("expected first push to fail on 500")
	}
	if len(removed) != 0 {
		t.Fatalf("removed before page purge went up: %v", removed)
	}
	svc.retryAt = map[outboxKey]time.Time{}
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patched["purged_at"] == nil || patched["deleted_at"] == nil || patched["title"] != "" {
		t.Errorf("page went up as %v", patched)
	}
	if strings.Join(removed["files"], ",") != f.StoragePath || strings.Join(removed["recordings"], ",") != "u1/rec1.wav,u1/rec2.wav" {
		t.Errorf("removed %v", removed)
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d waiting", n)
	}
}

func TestTheBinEmptiesOnlyWithWhatOtherDevicesDid(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plans")
	if _, err := a.st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := b.st.RestorePages([]string{p.ID}); err != nil {
		t.Fatal(err)
	}
	b.sync()
	later := time.Now().Add(store.BinKept + time.Hour)
	told := 0
	a.svc.Pulled = func() {
		told++
		if _, err := a.st.PurgeOld(later); err != nil {
			t.Error(err)
		}
	}
	syncAll(a, b)
	if told == 0 {
		t.Fatal("no pull was told")
	}
	for _, d := range []*cloudDevice{a, b} {
		if got, err := d.st.Page(p.ID); err != nil || !got.DeletedAt.IsZero() || d.st.IsPurged(p.ID) {
			t.Errorf("on %s, the page restored on B is %+v, %v", d.st.DeviceID(), got, err)
		}
	}
}

func TestAPullFromTheServerIsToldOnceItIsIn(t *testing.T) {
	st := openStore(t)
	const id = "33333333-3333-4333-8333-333333333333"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []map[string]any{}
		if r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodGet {
			rows = append(rows, map[string]any{"id": id, "space_id": st.Session().SpaceID, "title": "Restored", "sort_key": "V",
				"created_at": at, "updated_at": at})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	told := 0
	svc.Pulled = func() {
		told++
		if p, err := st.Page(id); err != nil || p.Title != "Restored" {
			t.Errorf("told before the pull was in: %+v, %v", p, err)
		}
	}
	if err := svc.pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Errorf("told %d times", told)
	}
}

func TestAPurgeLeavesNothingOfThePageInICloudDrive(t *testing.T) {
	root := cloudRoot(t)
	a, b, quiet := newCloudDevice(t, root), newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Hidden plans", &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}})
	a.edit(p.ID, "b1", "hidden text")
	if _, err := a.st.AddComment(p.ID, "b1", "hidden note", "", "", "user", "A"); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b, quiet)
	if _, err := b.st.AddComment(p.ID, "b1", "hidden reply", "", "", "user", "B"); err != nil {
		t.Fatal(err)
	}
	quiet.edit(p.ID, "b1", "hidden edit")
	if _, err := quiet.st.AddComment(p.ID, "b1", "hidden aside", "", "", "user", "Q"); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b, quiet)
	if _, err := a.st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := a.st.EmptyBin(); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	holding := func(under string) []string {
		t.Helper()
		var found []string
		err := filepath.WalkDir(filepath.Join(root, under), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path) // #nosec G304
			if err == nil && strings.Contains(strings.ToLower(string(raw)), "hidden") {
				found = append(found, strings.TrimPrefix(path, root))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	qid := quiet.st.DeviceID()
	if found := holding("devices/" + qid + "/log"); len(found) == 0 {
		t.Fatal("the log of a device still active was changed by another")
	}
	if found := holding("devices/" + qid + "/pages"); len(found) != 0 {
		t.Errorf("the version of the page of a device that had not heard stays: %v", found)
	}
	// It has not been seen since: the others take what was on the page out of its log.
	rel, _ := icloud.DevicePath(qid)
	var d deviceFile
	if err := a.svc.folder().ReadJSON(rel, &d); err != nil {
		t.Fatal(err)
	}
	d.LastActiveAt = time.Now().Add(-quietFor - time.Hour)
	if err := a.svc.folder().WriteJSON(rel, d); err != nil {
		t.Fatal(err)
	}
	a.svc.redactSoon.Store(true)
	a.sync()
	if found := holding(""); len(found) != 0 {
		t.Errorf("what was on the page stays in %v", found)
	}
	for _, dev := range []*cloudDevice{a, b} {
		if !dev.st.IsPurged(p.ID) {
			t.Errorf("not purged on %s", dev.st.DeviceID())
		}
	}
	// The quiet device comes back, and reads its log as it left it, but for the page.
	syncAll(quiet, a, b)
	if !quiet.st.IsPurged(p.ID) || quiet.st.Content(p.ID) != nil {
		t.Error("the device that came back keeps the page")
	}
	if found := holding(""); len(found) != 0 {
		t.Errorf("what was on the page came back in %v", found)
	}
}

func TestAFilePutInTheBinWaitsForAServerWithTheBin(t *testing.T) {
	st := openStore(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := st.Pending()
	for _, e := range pending {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	withBin := false
	var patched map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/rest/v1/files" && r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["kept"]; ok && !withBin {
				// A server without the bin would empty the row of a deleted file: it knows no such column.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"PGRST204","message":"Could not find the 'kept' column of 'files' in the schema cache"}`))
				return
			}
			patched = body
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": f.ID}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := st.PendingCount(); n != 1 || patched != nil {
		t.Fatalf("a server without the bin took a file put in the bin: %d waiting, sent %v", n, patched)
	}
	mu.Lock()
	withBin = true
	mu.Unlock()
	svc.retryAt = map[outboxKey]time.Time{}
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patched["kept"] != true || patched["deleted_at"] == nil {
		t.Errorf("went up as %v", patched)
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d waiting", n)
	}
}

func TestAPurgeRemovesARecordingSomeoneElseMadeByItsListedPath(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	// Made by the person it is shared with: its audio_path has not come here yet.
	if err := st.PutRecording(store.Recording{ID: "33333333-3333-4333-8333-333333333334", PageID: p.ID, BlockID: "b", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(); err != nil {
		t.Fatal(err)
	}
	const theirs = "u2/33333333-3333-4333-8333-333333333334.wav"
	var mu gosync.Mutex
	var removed []string
	var asked string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": p.ID}})
			return
		case r.URL.Path == "/rest/v1/purged_objects":
			asked = r.URL.Query().Get("name")
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": theirs}})
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/recordings"):
			var body struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			removed = append(removed, body.Prefixes...)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked != "like.*/33333333-3333-4333-8333-333333333334.*" {
		t.Errorf("asked for %q", asked)
	}
	if strings.Join(removed, ",") != theirs {
		t.Errorf("removed %v, want %s", removed, theirs)
	}
}
