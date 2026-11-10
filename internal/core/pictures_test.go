package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/internal/webpage"
)

func TestDownloadPictures(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var paths []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte("png"))
	}))
	asked := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
	t.Cleanup(hs.Close)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	file := func(id, page string) {
		t.Helper()
		if err := st.PutRemoteFile(map[string]any{"id": id, "space_id": st.Session().SpaceID, "page_id": page, "name": "dawn.png",
			"storage_path": "space/" + id + "/dawn.png", "mime_type": "image/png", "size_bytes": 3.0, "created_at": at, "updated_at": at}); err != nil {
			t.Fatal(err)
		}
	}
	const mine, theirs = "00000000-0000-0000-0000-0000000000d1", "00000000-0000-0000-0000-0000000000d2"
	file(mine, "p1")
	file(theirs, "p2")
	picture := func(id, file string) *doc.Node {
		return &doc.Node{ID: id, Type: "image", Data: map[string]any{"url": "file:///elsewhere/dawn.png", "file_id": file}}
	}
	content := doc.FromBlocks([]*doc.Node{picture("a", mine), picture("b", mine), picture("c", theirs)})
	be := remote.SignedInForTest(hs.URL, st, "u1")
	c := &Core{Store: st, Sync: syncsvc.New(st, be)}
	if !c.DownloadPictures(context.Background(), content, "p1") {
		t.Fatal("no picture came")
	}
	if got := asked(); len(got) != 1 || got[0] != "/storage/v1/object/authenticated/files/space/"+mine+"/dawn.png" {
		t.Errorf("asked for %v", got)
	}
	files, _ := st.Files()
	page := webpage.HTML(content, webpage.Options{Store: st, Page: "p1", Files: files, Serve: webpage.Inline, Print: true})
	if strings.Count(page, `src="data:image/png;base64,cG5n"`) != 2 {
		t.Errorf("the pictures are not in the page:\n%s", page)
	}
	if c.DownloadPictures(context.Background(), content, "p1") || len(asked()) != 1 {
		t.Errorf("downloaded again: %v", asked())
	}
}
