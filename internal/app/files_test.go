package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
)

// A file uploaded from another device downloads from Storage the first
// time it is needed here, and stays.
func TestFileFromAnotherDeviceDownloads(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-0000-0000-0000000000dd"
	var asked string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte("hello"))
	}))
	defer hs.Close()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.PutRemoteFile(map[string]any{"id": id, "space_id": st.Session().SpaceID, "name": "notes.txt",
		"storage_path": "space/" + id + "/notes.txt", "mime_type": "text/plain", "size_bytes": 5.0, "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInForTest(hs.URL, st, "u1")
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	f, ok := a.fileByID(id)
	if !ok {
		t.Fatal("no file")
	}
	done := make(chan struct{})
	if path := a.localFile(f, func() { close(done) }); path != "" {
		t.Fatalf("on this device already at %s", path)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the download never finished")
	}
	if asked != "/storage/v1/object/authenticated/files/space/"+id+"/notes.txt" {
		t.Errorf("asked for %s", asked)
	}
	f, _ = a.fileByID(id)
	path := a.localFile(f, func() { t.Error("downloaded twice") })
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello" { // #nosec G304 -- the file the test just downloaded.
		t.Errorf("%s has %q, %v", path, data, err)
	}
}

// A picture whose download failed shows as failed, not loading, and is
// downloaded again once imageRetry has passed.
func TestPictureDownloadFails(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-0000-0000-0000000000de"
	var asked atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hs.Close()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.PutRemoteFile(map[string]any{"id": id, "space_id": st.Session().SpaceID, "name": "photo.png",
		"storage_path": "space/" + id + "/photo.png", "mime_type": "image/png", "size_bytes": 5.0, "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInForTest(hs.URL, st, "u1")
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	h := &host{a: a}
	n := &doc.Node{ID: "img", Type: "image", Data: map[string]any{"file_id": id}}
	if pic, _, failed := h.Image(n); pic != nil || failed {
		t.Fatalf("before the download: %v, failed %v", pic, failed)
	}
	select {
	case fn := <-posted:
		fn() // the download's end, on the main thread
	case <-time.After(5 * time.Second):
		t.Fatal("the download never ended")
	}
	if _, _, failed := h.Image(n); !failed {
		t.Fatal("a failed download still shows as loading")
	}
	if asked.Load() != 1 {
		t.Fatalf("asked %d times", asked.Load())
	}
	a.downloadFails[id] = time.Now().Add(-imageRetry)
	h.Image(n)
	<-posted
	if asked.Load() != 2 {
		t.Errorf("not tried again after the wait: asked %d times", asked.Load())
	}
}

// Files are copied in before what adds them to a page runs on the main
// thread, each in its turn.
func TestAddFilesOffTheMainThread(t *testing.T) {
	a := templatesApp(t)
	var posted []func()
	a.post = func(fn func()) { posted = append(posted, fn) }
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a.txt", "b.txt"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	var got []string
	a.addFiles(append(paths, filepath.Join(dir, "missing.txt")), "", "", func(f store.File) { got = append(got, f.Name) })
	if files, _ := a.st.Files(); len(files) != 2 || len(got) != 0 {
		t.Fatalf("%d files stored, %v added before the main thread ran", len(files), got)
	}
	for _, fn := range posted {
		fn()
	}
	if fmt.Sprint(got) != "[a.txt b.txt]" {
		t.Errorf("added %v", got)
	}
}
