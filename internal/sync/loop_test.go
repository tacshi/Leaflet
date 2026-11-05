package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func TestOpeningAPageFetchesItsContent(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "from another device")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	srv := &contentServer{content: theirs.Encode(), revision: 3}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	got := make(chan string, 1)
	svc.OnPageContent = func(id string) { got <- id }
	svc.SetOpen(p.ID, true)
	select {
	case id := <-got:
		if id != p.ID {
			t.Errorf("content of %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the opened page's content never came")
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "from another device" {
		t.Errorf("stored %v", c)
	}
}

// An opened page has its time to come once the pass in progress is done,
// not from when it was opened.
func TestOpeningAPageWaitsForThePassFirst(t *testing.T) {
	old := openWait
	openWait = 200 * time.Millisecond
	t.Cleanup(func() { openWait = old })
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "from another device")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	srv := &contentServer{content: theirs.Encode(), revision: 3}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	got := make(chan string, 1)
	svc.OnPageContent = func(id string) { got <- id }
	svc.running.Lock()
	svc.SetOpen(p.ID, true)
	time.Sleep(2 * openWait)
	svc.running.Unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the opened page's content never came")
	}
}

// An open page takes what changed on the server, as another device's agent
// wrote it, from the check between the passes: it asks for the page's
// revision, and its content only once that moved.
func TestTheOpenPageTakesWhatChangedOnTheServer(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	mine := &doc.Content{Children: []*doc.Node{para("a", "as it was")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, mine)
	dequeueAll(t, st)
	srv := &contentServer{content: mine.Encode(), revision: 1}
	var mu gosync.Mutex
	var selects []string
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		selects = append(selects, r.URL.Query().Get("select"))
		mu.Unlock()
		srv.ServeHTTP(w, r)
	})))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	got := make(chan string, 4)
	svc.OnPageContent = func(id string) { got <- id }
	svc.open[p.ID] = true

	svc.watchOpen(t.Context())
	if !slices.Equal(selects, []string{savedCols}) || len(got) != 0 {
		t.Errorf("unchanged, the check asked for %q and told the app of %d changes", selects, len(got))
	}

	theirs := &doc.Content{Children: []*doc.Node{para("a", "as an agent wrote it")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	srv.mu.Lock()
	srv.content, srv.revision = theirs.Encode(), 2
	srv.mu.Unlock()
	svc.watchOpen(t.Context())
	select {
	case id := <-got:
		if id != p.ID {
			t.Errorf("content of %s", id)
		}
	default:
		t.Fatal("the open page was not told of what changed")
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "as an agent wrote it" {
		t.Errorf("stored %v", c)
	}
}

// A page whose content on the server can't be read is passed over, and
// the other open pages are checked all the same.
func TestTheOpenPagesAreCheckedPastOneThatCannotBeRead(t *testing.T) {
	st := openStore(t)
	var ids []string
	for range 2 {
		here := &doc.Content{Children: []*doc.Node{para("a", "here")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
		p, err := st.CreatePage(store.NewPageOptions{Title: "Page", Content: here})
		if err != nil {
			t.Fatal(err)
		}
		putSynced(t, st, p.ID, here)
		ids = append(ids, p.ID)
	}
	dequeueAll(t, st)
	slices.Sort(ids)
	// The first checked is the one that can't be read.
	const unreadable = `{"document": {"type": "page", "id": "root", "children": [{"id": "x", "type": "paragraph", "data": ["of a newer kind"]}]}, "meta": {"v": 1, "blocks": {"x": 5}}}`
	theirs := &doc.Content{Children: []*doc.Node{para("a", "there")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentRows(r, func(id string) map[string]any {
			content := theirs.Encode()
			if id == ids[0] {
				content = unreadable
			}
			return map[string]any{"content": json.RawMessage(content), "revision": 2, "updated_at": at}
		}))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for _, id := range ids {
		svc.open[id] = true
	}
	svc.watchOpen(t.Context())
	if c := st.Content(ids[0]); c == nil || textOf(c, "a") != "here" {
		t.Errorf("the page that can't be read has %v", c)
	}
	if c := st.Content(ids[1]); c == nil || textOf(c, "a") != "there" {
		t.Errorf("the other page has %v", c)
	}
}

// The check asks the server nothing while the window is off screen, while
// a pass or an opened page's refresh runs, which brings the page already,
// while the server asks for a wait, or while sync goes through iCloud
// Drive, whose changes start a pass as they come.
func TestTheOpenPagesWait(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("[]"))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.open[p.ID] = true
	shown := false
	svc.Shown = func() bool { return shown }
	svc.watchOpen(t.Context())
	shown = true
	svc.running.Lock()
	svc.watchOpen(t.Context())
	svc.running.Unlock()
	svc.busyUntil = time.Now().Add(time.Minute)
	svc.watchOpen(t.Context())
	svc.busyUntil = time.Time{}
	svc.cloudOn.Store(true)
	svc.watchOpen(t.Context())
	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests while the check should wait", n)
	}
	svc.cloudOn.Store(false)
	svc.watchOpen(t.Context())
	if requests.Load() == 0 {
		t.Error("the check asked nothing once it could")
	}
}

// The loop checks the open pages between its passes: what changed on the
// server comes though every pass fails.
func TestTheLoopChecksTheOpenPages(t *testing.T) {
	old := watchEvery
	watchEvery = 20 * time.Millisecond
	t.Cleanup(func() { watchEvery = old })
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	mine := &doc.Content{Children: []*doc.Node{para("a", "as it was")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, mine)
	dequeueAll(t, st)
	theirs := &doc.Content{Children: []*doc.Node{para("a", "from another device")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	srv := &contentServer{content: theirs.Encode(), revision: 2}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the check's reads of the page are answered.
		if r.Method == "GET" && r.URL.Path == "/rest/v1/page_content" && r.URL.Query().Get("page_id") == "eq."+p.ID {
			srv.ServeHTTP(w, r)
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	got := make(chan string, 4)
	svc.OnPageContent = func(id string) { got <- id }
	svc.mu.Lock()
	svc.open[p.ID] = true
	svc.mu.Unlock()
	svc.Start()
	defer svc.Stop()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("what changed on the server never came to the open page")
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "from another device" {
		t.Errorf("stored %v", c)
	}
}
