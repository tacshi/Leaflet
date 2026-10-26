package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello" {
		t.Errorf("%s has %q, %v", path, data, err)
	}
}
