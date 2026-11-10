package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

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
	if path, fetching := a.localFile(f, func(bool) { close(done) }); path != "" || !fetching {
		t.Fatalf("on this device already at %q, downloading %v", path, fetching)
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
	path, _ := a.localFile(f, func(bool) { t.Error("downloaded twice") })
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello" { // #nosec G304
		t.Errorf("%s has %q, %v", path, data, err)
	}
}

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
		fn()
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

func TestUpdatesWaitForTheWindow(t *testing.T) {
	a := templatesApp(t)
	a.starting = true
	ran := false
	a.update(func() { ran = true })
	if ran || len(a.early) != 1 {
		t.Errorf("ran %v, %d waiting", ran, len(a.early))
	}
}

func TestPreviewWaitsForTheDownloadUnderway(t *testing.T) {
	for _, fails := range []bool{false, true} {
		st, err := store.OpenMemory(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, err := st.LoadSession(nil); err != nil {
			t.Fatal(err)
		}
		const id = "00000000-0000-0000-0000-0000000000df"
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if fails {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("hello"))
		}))
		t.Cleanup(hs.Close)
		at := time.Now().UTC().Format(time.RFC3339Nano)
		if err := st.PutRemoteFile(map[string]any{"id": id, "space_id": st.Session().SpaceID, "name": "notes.txt",
			"storage_path": "space/" + id + "/notes.txt", "mime_type": "text/plain", "size_bytes": 5.0, "created_at": at, "updated_at": at}); err != nil {
			t.Fatal(err)
		}
		be := remote.SignedInForTest(hs.URL, st, "u1")
		a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
		a.reloadAll()
		posted := make(chan func(), 8)
		a.post = func(fn func()) { posted <- fn }
		f, _ := a.fileByID(id)
		a.previewFile(f)
		a.previewFile(f)
		p := a.preview
		for p.loading {
			select {
			case fn := <-posted:
				fn()
			case <-time.After(5 * time.Second):
				t.Fatalf("failing %v: the preview is still loading", fails)
			}
		}
		if p.failed != fails {
			t.Errorf("failing %v: the preview failed %v", fails, p.failed)
		}
		if !fails && p.code == nil {
			t.Errorf("the file downloaded shows as %+v", p)
		}
	}
}

func addedFile(t *testing.T, a *App, pageID, name string) store.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(path, "", pageID, "")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestUploadedBlocksKeepNoLocalPath(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	a.reloadAll()
	a.openPage(p.ID)
	a.placeUpload(p.ID, "", addedFile(t, a, p.ID, "photo.png"))
	a.placeUpload(p.ID, "", addedFile(t, a, p.ID, "notes.pdf"))
	old := addedFile(t, a, p.ID, "old.png")
	a.page.editor.InsertBlocks(
		&doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{"url": "file://" + old.LocalPath, "alt": "Old", "file_id": old.ID}},
		&doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": `file://C:\Users\me\Leaflet\files\1\win.pdf`, "file_id": "f1"}},
		&doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{"url": "https://example.com/web.png", "alt": "Web"}},
	)
	a.replaceWith(t, p.ID, "photo.png", addedFile(t, a, p.ID, "better.png"))
	if !a.savePage() {
		t.Fatal("not saved")
	}
	a.reloadAll()
	c := a.st.Content(p.ID)
	kept := 0
	c.Walk(func(n, _ *doc.Node) {
		f, ok := a.fileByID(n.Str("file_id"))
		if !ok || f.ID == old.ID {
			return
		}
		kept++
		if url := n.Str("url"); !strings.HasPrefix(url, "file://") || strings.Contains(url, a.st.Dir) || filepath.IsAbs(strings.TrimPrefix(url, "file://")) {
			t.Errorf("the block of %s holds %q", f.Name, url)
		}
	})
	if kept != 2 {
		t.Fatalf("%d blocks of uploaded files", kept)
	}
	md := doc.PageMarkdown(p.Title, c, a.markdownOptions())
	if strings.Contains(md, a.st.Dir) || strings.Contains(md, "file:") || strings.Contains(md, "Users") {
		t.Errorf("Markdown tells where files are on this device:\n%s", md)
	}
	for _, want := range []string{"![better.png](better.png)", "[notes.pdf](notes.pdf)", "![Old](old.png)", "[win.pdf](win.pdf)", "![Web](https://example.com/web.png)"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %s:\n%s", want, md)
		}
	}
}

func (a *App) replaceWith(t *testing.T, pageID, alt string, f store.File) {
	t.Helper()
	var id string
	a.page.editor.Content.Walk(func(n, _ *doc.Node) {
		if n.Type == "image" && n.Str("alt") == alt {
			id = n.ID
		}
	})
	if id == "" {
		t.Fatalf("no picture %s", alt)
	}
	a.placeUpload(pageID, id, f)
}

func TestUploadEndsAfterLeavingThePage(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	other, _ := a.st.CreatePage(store.NewPageOptions{Title: "Other"})
	a.reloadAll()
	a.openPage(p.ID)
	f := addedFile(t, a, p.ID, "notes.pdf")
	a.openPage(other.ID)
	a.placeUpload(p.ID, "", f)
	found := false
	a.st.Content(p.ID).Walk(func(n, _ *doc.Node) { found = found || n.Type == "file" && n.Str("file_id") == f.ID })
	if !found || len(a.toasts) != 1 || a.toasts[0].text != locales.HostAddedToPage("notes.pdf", "Trip") {
		t.Errorf("in its page %v, said %+v", found, a.toasts)
	}
	if a.page.id != other.ID || a.page.editor.Content.Find(f.ID) != nil {
		t.Error("the page open was changed")
	}

	a.toasts = nil
	gone := addedFile(t, a, "no-such-page", "late.pdf")
	a.reloadAll()
	a.placeUpload("no-such-page", "", gone)
	a.reloadAll()
	if _, ok := a.fileByID(gone.ID); ok || len(a.toasts) != 1 || a.toasts[0].text != locales.HostFileNotAdded("late.pdf") {
		t.Errorf("a file with no page is kept %v, said %+v", ok, a.toasts)
	}
}

func TestFileThatCannotBeDownloaded(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-0000-0000-0000000000e0"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hs.Close()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.PutRemoteFile(map[string]any{"id": id, "space_id": st.Session().SpaceID, "name": "photo.png",
		"storage_path": "space/" + id + "/photo.png", "mime_type": "image/png", "size_bytes": 5.0, "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	out := remote.New(remote.Config(), st)
	a := &App{st: st, backend: out, sync: syncsvc.New(st, out), session: st.Session()}
	a.reloadAll()
	f, _ := a.fileByID(id)
	a.openFile(f)
	if len(a.toasts) != 1 || a.toasts[0].text != locales.HostNotOnDevice("photo.png") {
		t.Errorf("signed out, said %+v", a.toasts)
	}
	if _, _, failed := (&host{a: a}).Image(&doc.Node{ID: "img", Type: "image", Data: map[string]any{"file_id": id}}); !failed {
		t.Error("a picture that cannot be downloaded shows as loading")
	}

	in := remote.SignedInForTest(hs.URL, st, "u1")
	a = &App{st: st, backend: in, sync: syncsvc.New(st, in), session: st.Session()}
	a.reloadAll()
	posted := make(chan func(), 8)
	a.post = func(fn func()) { posted <- fn }
	a.openFile(f)
	if len(a.toasts) != 1 || a.toasts[0].text != locales.HostDownloading("photo.png") {
		t.Fatalf("signed in, said %+v", a.toasts)
	}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("the download never ended")
	}
	if len(a.toasts) != 2 || a.toasts[1].text != locales.HostDownloadFailed("photo.png") {
		t.Errorf("after the download failed, said %+v", a.toasts)
	}
}
