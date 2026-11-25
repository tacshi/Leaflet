package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	if err := b.st.EmptyBin(store.WipeNow); err != nil {
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
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
		t.Fatal(err)
	}
	// Put there by another of the owner's devices, whose row this one never pulled.
	const elsewhere = "space/other/photo.png"
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
		case r.URL.Path == "/rest/v1/purged_objects" && r.URL.Query().Get("page_id") == "eq."+p.ID:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"bucket_id": "files", "name": f.StoragePath}, {"bucket_id": "files", "name": elsewhere}})
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
	// While the purge has not gone up yet, the page keeps what was on it, here and where it is stored.
	if err := svc.push(context.Background()); err == nil {
		t.Fatal("expected first push to fail on 500")
	}
	if len(removed) != 0 {
		t.Fatalf("removed before page purge went up: %v", removed)
	}
	if _, err := st.File(f.ID); err != nil {
		t.Errorf("the file went before the server took the purge: %v", err)
	}
	if _, err := st.Recording("rec1"); err != nil {
		t.Errorf("the recording went before the server took the purge: %v", err)
	}
	svc.retryAt = map[outboxKey]time.Time{}
	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if patched["purged_at"] == nil || patched["deleted_at"] == nil || patched["title"] != "" {
		t.Errorf("page went up as %v", patched)
	}
	wantFiles := []string{f.StoragePath, elsewhere}
	slices.Sort(wantFiles)
	slices.Sort(removed["files"])
	if !slices.Equal(removed["files"], wantFiles) || strings.Join(removed["recordings"], ",") != "u1/rec1.wav,u1/rec2.wav" {
		t.Errorf("removed %v", removed)
	}
	if _, err := st.File(f.ID); !store.IsNotFound(err) {
		t.Errorf("the file stayed once the server took the purge: %v", err)
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d waiting", n)
	}
}

// TestARefusedPurgeKeepsThePage has the server refuse a deletion for good of a page another device restored since the
// deletion this one saw: the page comes back here as the server has it, with what was on it, and nothing of it is
// removed where it is stored.
func TestARefusedPurgeKeepsThePage(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	c := doc.Empty()
	if err := c.Insert(doc.Position{At: "end"}, &doc.Node{ID: "b1", Type: "paragraph", Data: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRecording(store.Recording{ID: "rec1", PageID: p.ID, BlockID: "b1", AudioPath: "u1/rec1.wav", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
		t.Fatal(err)
	}
	restored := store.PageToRemote(p)
	restored["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	var removed []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"42501","message":"page_restored"}`)
			return
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{restored})
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/"):
			removed = append(removed, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Page(p.ID)
	if err != nil || st.IsPurged(p.ID) || !got.DeletedAt.IsZero() || got.Title != "Plans" {
		t.Fatalf("the page is %+v, %v", got, err)
	}
	if content := st.Content(p.ID); content == nil || content.Find("b1") == nil {
		t.Errorf("what was on the page went: %v", content)
	}
	if _, err := st.Recording("rec1"); err != nil {
		t.Errorf("the recording went: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v of a page back in use", removed)
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
		if _, err := a.st.PurgeOld(later, store.WipeNow); err != nil {
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
	if err := a.st.EmptyBin(store.WipeNow); err != nil {
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
	// It has not been seen since: a day on, once it has gone quiet, the others take what was on the page out of its
	// log, as a device that came back in between would have compacted its own.
	a.sync()
	if found := holding("devices/" + qid + "/log"); len(found) == 0 {
		t.Fatal("the log of a device still active was changed by another")
	}
	was := cloudNow
	cloudNow = func() time.Time { return time.Now().Add(redactAgain + time.Hour) }
	t.Cleanup(func() { cloudNow = was })
	rel, _ := icloud.DevicePath(qid)
	var d deviceFile
	if err := a.svc.folder().ReadJSON(rel, &d); err != nil {
		t.Fatal(err)
	}
	d.LastActiveAt = cloudNow().Add(-quietFor - time.Hour)
	if err := a.svc.folder().WriteJSON(rel, d); err != nil {
		t.Fatal(err)
	}
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
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
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
		case r.URL.Path == "/rest/v1/purged_objects" && r.URL.Query().Get("page_id") != "":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
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
	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if asked != "like.*/33333333-3333-4333-8333-333333333334.*" {
		t.Errorf("asked for %q", asked)
	}
	if strings.Join(removed, ",") != theirs {
		t.Errorf("removed %v, want %s", removed, theirs)
	}
}

// sentFile is a file added here that went up, its row as the server has it pulled back at the time.
func sentFile(t *testing.T, st *store.Store, at time.Time) store.File {
	t.Helper()
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Queued()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	row := store.FileToRemote(f)
	row["updated_at"] = at.UTC().Format(time.RFC3339Nano)
	if err := st.PutRemoteFile(row); err != nil {
		t.Fatal(err)
	}
	return f
}

func binRefs(t *testing.T, st *store.Store) []store.BinRef {
	t.Helper()
	items, err := st.Bin()
	if err != nil {
		t.Fatal(err)
	}
	var refs []store.BinRef
	for _, it := range items {
		refs = append(refs, it.BinRef)
	}
	return refs
}

// TestAFilePurgeGoesUpWithWhatItSaw deletes a file of the bin for good: the deletion goes up with the deletion this
// device saw, and with the server's time of the row as the server took the file's trashing, for the server to refuse
// it if the file changed there since. Its copy here goes once the server has the deletion.
func TestAFilePurgeGoesUpWithWhatItSaw(t *testing.T) {
	st := openStore(t)
	f := sentFile(t, st, time.Now().Add(-time.Hour))
	if err := st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	trashed := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	var mu gosync.Mutex
	var patches []map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/rest/v1/files" && r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patches = append(patches, body)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": f.ID, "updated_at": trashed.Format(time.RFC3339Nano)}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(store.WipeOnceTaken, binRefs(t, st)...); err != nil {
		t.Fatal(err)
	}
	deleted, _ := st.File(f.ID)
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Fatalf("the copy went before the deletion went up: %v", err)
	}
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 {
		t.Fatalf("sent %v", patches)
	}
	purge := patches[1]
	if purge["purged_at"] == nil || store.TimeOrZero(purge["deleted_at"]).UnixMilli() != deleted.DeletedAt.UnixMilli() ||
		!store.TimeOrZero(purge[seenKey]).Equal(trashed) || purge["name"] != "" {
		t.Errorf("the deletion for good went up as %v", purge)
	}
	if _, err := os.Stat(f.LocalPath); !os.IsNotExist(err) {
		t.Errorf("the copy stayed once the server took the deletion: %v", err)
	}
}

// TestARefusedFilePurgeKeepsItsStoredObject has the server refuse a file's deletion for good, as another device put it
// in the bin again since: this device takes the server's row, the file in the bin with its contents, and removes
// nothing of it where it is stored.
func TestARefusedFilePurgeKeepsItsStoredObject(t *testing.T) {
	st := openStore(t)
	f := sentFile(t, st, time.Now().Add(-time.Hour))
	if err := st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(store.WipeOnceTaken, binRefs(t, st)...); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	there := store.FileToRemote(f)
	there["deleted_at"], there["kept"], there["updated_at"] = later, true, later
	var mu gosync.Mutex
	var removed []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/files" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"42501","message":"file_restored"}`)
			return
		case r.URL.Path == "/rest/v1/files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{there})
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/") && r.Method == http.MethodDelete:
			body, _ := io.ReadAll(r.Body)
			removed = append(removed, string(body))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.File(f.ID)
	if err != nil || !got.PurgedAt.IsZero() || !got.Kept || got.Name != "notes.txt" {
		t.Fatalf("the file is %+v, %v", got, err)
	}
	if _, err := os.Stat(f.LocalPath); err != nil {
		t.Errorf("the file back in the bin lost its copy: %v", err)
	}
	if len(removed) > 0 {
		t.Errorf("removed the object of a file the server keeps in the bin: %v", removed)
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d waiting", n)
	}
}

// TestAPagePurgeGoesUpWithTheServersTimeOfThePage deletes a page, whose deletion the server takes, and then deletes it
// for good before pulling it back: the deletion for good goes with the server's time of the page as it took the
// deletion, not as this device last pulled it.
func TestAPagePurgeGoesUpWithTheServersTimeOfThePage(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Queued()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	row := store.PageToRemote(p)
	row["updated_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := st.PutRemotePage(row); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	took := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	var mu gosync.Mutex
	var patches []map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patches = append(patches, body)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": p.ID, "updated_at": took.Format(time.RFC3339Nano)}})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 || patches[1]["purged_at"] == nil || !store.TimeOrZero(patches[1][seenKey]).Equal(took) {
		t.Errorf("sent %v", patches)
	}
}

// TestWhatCameWhileAPurgeWaitedComesWithThePageBack pulls, while a deletion for good waits to go up, a comment and a
// file another device put on the page, which this device passes by; the server refuses the deletion, of a page
// restored since: the page comes back with them, and its content is read again.
func TestWhatCameWhileAPurgeWaitedComesWithThePageBack(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	comment := map[string]any{"id": "c1", "page_id": p.ID, "block_id": "b1", "author_id": "u2", "author_kind": "user",
		"body": "a note", "created_at": at, "updated_at": at}
	fileID := doc.NewRowID()
	file := map[string]any{"id": fileID, "space_id": p.SpaceID, "page_id": p.ID, "name": "notes.txt",
		"storage_path": p.SpaceID + "/" + fileID + "/notes.txt", "created_at": at, "updated_at": at}
	restored := store.PageToRemote(p)
	restored["updated_at"] = at
	var mu gosync.Mutex
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		var rows []map[string]any
		switch {
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"42501","message":"page_restored"}`)
			return
		case r.URL.Path == "/rest/v1/pages" && q.Get("id") == "eq."+p.ID:
			rows = []map[string]any{restored}
		case r.URL.Path == "/rest/v1/comments" && r.Method == http.MethodGet:
			rows = []map[string]any{comment}
		case r.URL.Path == "/rest/v1/files" && strings.HasPrefix(q.Get("page_id"), "in."):
			rows = []map[string]any{file}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	if err := svc.pullComments(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Comment("c1"); err == nil {
		t.Fatal("a comment on a page deleted for good here was kept")
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if st.IsPurged(p.ID) {
		t.Fatal("the page did not come back")
	}
	if _, err := st.Comment("c1"); err != nil {
		t.Errorf("the comment put on the page meanwhile did not come: %v", err)
	}
	if _, err := st.File(fileID); err != nil {
		t.Errorf("the file put on the page meanwhile did not come: %v", err)
	}
	if waiting, _ := svc.waiting(); !waiting[p.ID] {
		t.Errorf("the page's content is not read again: %v", waiting)
	}
	if trees, _ := svc.ids(treesKey); len(trees) != 0 {
		t.Errorf("trees still to pull: %v", trees)
	}
}

// TestAPurgeReadsEveryObjectTheServerLists lists more objects of a page deleted for good than a read returns: each
// read goes on after the last, and every one is removed.
func TestAPurgeReadsEveryObjectTheServerLists(t *testing.T) {
	was := treeRows
	treeRows = 2
	t.Cleanup(func() { treeRows = was })
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plans"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EmptyBin(store.WipeOnceTaken); err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	for i := range 5 {
		listed = append(listed, map[string]any{"bucket_id": "files", "name": fmt.Sprintf("space/f%d/photo.png", i)})
	}
	var mu gosync.Mutex
	var asked []url.Values
	var removed []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/pages" && r.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": p.ID}})
			return
		case r.URL.Path == "/rest/v1/purged_objects":
			from := len(asked) * treeRows
			asked = append(asked, r.URL.Query())
			_ = json.NewEncoder(w).Encode(listed[min(from, len(listed)):min(from+treeRows, len(listed))])
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/") && r.Method == http.MethodDelete:
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
	for range 2 {
		if err := svc.push(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 3 || asked[0].Get("order") != "bucket_id,name" || !strings.Contains(asked[2].Get("or"), "space/f3/photo.png") {
		t.Errorf("asked %v", asked)
	}
	slices.Sort(removed)
	if len(removed) != 5 || removed[0] != "space/f0/photo.png" || removed[4] != "space/f4/photo.png" {
		t.Errorf("removed %v", removed)
	}
}

// TestAnEntryOfNoKindThisLeafletKnowsIsDropped has nothing go up for an entry of the outbox of a kind nothing here
// queues, as a Leaflet from later might have.
func TestAnEntryOfNoKindThisLeafletKnowsIsDropped(t *testing.T) {
	st := openStore(t)
	entries, _ := st.Queued()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.Queue("widgets", "w1", map[string]any{"id": "w1"}, "", ""); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 || st.PendingCount() != 0 {
		t.Errorf("asked %v, %d waiting", asked, st.PendingCount())
	}
}

// TestARemovalWaitsForTheListToBeRead removes the audio of a recording whose path never came here, which the server
// lists by its id: while the list cannot be read, nothing is removed, and the removal waits.
func TestARemovalWaitsForTheListToBeRead(t *testing.T) {
	st := openStore(t)
	entries, _ := st.Queued()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	const rec = "44444444-4444-4444-8444-444444444444"
	if err := st.Queue("storage_delete", "recordings/"+rec, map[string]any{"bucket": "recordings", "path": "", "recording_id": rec}, "", ""); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	down := true
	var removed []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/purged_objects" && down:
			w.WriteHeader(http.StatusInternalServerError)
			return
		case r.URL.Path == "/rest/v1/purged_objects":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "u2/" + rec + ".wav"}})
			return
		case strings.HasPrefix(r.URL.Path, "/storage/v1/object/") && r.Method == http.MethodDelete:
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
	if err := svc.push(context.Background()); err == nil {
		t.Fatal("a removal went on past a list it could not read")
	}
	if len(removed) != 0 || st.PendingCount() != 1 {
		t.Fatalf("removed %v, %d waiting", removed, st.PendingCount())
	}
	mu.Lock()
	down = false
	mu.Unlock()
	svc.retryAt = map[outboxKey]time.Time{}
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"u2/" + rec + ".wav"}) || st.PendingCount() != 0 {
		t.Errorf("removed %v, %d waiting", removed, st.PendingCount())
	}
}

// TestAWholeFileRowPurgedGoesWithWhatItSaw deletes for good a file whose whole row waits to go up again: it goes as a
// change of every column first, with what this device saw of the row, which an upsert's update would not have.
func TestAWholeFileRowPurgedGoesWithWhatItSaw(t *testing.T) {
	st := openStore(t)
	at := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli()).UTC()
	f := sentFile(t, st, at)
	if err := st.TrashFile(f.ID); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Queued()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.RequeueFile(f.ID, f.SpaceID); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(store.WipeOnceTaken, binRefs(t, st)...); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var patched map[string]any
	var upserts int
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/rest/v1/files" && r.Method == http.MethodPatch:
			_ = json.NewDecoder(r.Body).Decode(&patched)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": f.ID}})
			return
		case r.URL.Path == "/rest/v1/files" && r.Method == http.MethodPost:
			upserts++
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if upserts != 0 || patched["purged_at"] == nil || !store.TimeOrZero(patched[seenKey]).Equal(at) {
		t.Errorf("went up as %v, %d upserts", patched, upserts)
	}
}
