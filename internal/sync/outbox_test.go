package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// A file larger than the server takes is refused for good: its upload
// waits the longest at once rather than going whole again on the next
// passes, its row waits with it, and the page's other changes still go.
func TestUploadTooLargeWaitsTheLongest(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "film.mov")
	if err := os.WriteFile(src, []byte("mov"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFile(src, "", p.ID, "video/quicktime")
	if err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	uploads := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(req.URL.Path, "/storage/v1/object/files/") {
			uploads++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"statusCode":"413","error":"Payload too large"}`)
			return
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
	if uploads != 1 {
		t.Errorf("the file went up %d times", uploads)
	}
	if !st.HasPending("storage", "files/"+f.ID) || !st.HasPending("files", f.ID) {
		t.Error("the file stopped waiting")
	}
	if st.HasPending("pages", p.ID) {
		t.Error("the page waits with the file")
	}
}

// An entry that failed waits before it goes again, longer each time, as a
// pass runs after every save; a change to it goes at once.
func TestFailedEntriesWait(t *testing.T) {
	retryBase = time.Minute
	t.Cleanup(func() { retryBase = 0 })
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &refusing{}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	sent := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		n := 0
		for _, r := range srv.requests {
			if r == "POST /rest/v1/pages" {
				n++
			}
		}
		return n
	}

	for range 2 {
		if err := svc.push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := sent(); n != 1 {
		t.Errorf("the page went %d times", n)
	}
	if e := pendingOf(t, st, "pages", p.ID); e == nil || e.Attempts != 1 {
		t.Errorf("queued %+v", e)
	}
	if err := st.RenamePage(p.ID, "My plan"); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if n := sent(); n != 2 {
		t.Errorf("the changed page went %d times", n)
	}
	if backoff(1) != time.Minute || backoff(3) != 4*time.Minute || backoff(40) != retryMax {
		t.Errorf("backoff %v, %v, %v", backoff(1), backoff(3), backoff(40))
	}
}
