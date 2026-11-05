package sync

import (
	"encoding/json"
	"fmt"
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

// What changed on the server merges into what is being typed here, which
// still waits to go up.
func TestTheOpenPageMergesWhatChangedIntoWhatIsTyped(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, base)
	dequeueAll(t, st)
	mine := st.Content(p.ID)
	mine.Find("b").SetDelta(doc.Delta{{Text: "two, typed here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "one, as an agent wrote it"), para("b", "two")},
		Blocks: map[string]int64{"a": 2000, "b": 1000}, Deleted: map[string]int64{}}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", &contentServer{content: theirs.Encode(), revision: 2}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.open[p.ID] = true
	svc.watchOpen(t.Context())
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "one, as an agent wrote it" || textOf(c, "b") != "two, typed here" {
		t.Errorf("stored %v", c)
	}
	if !st.HasPending("page_content", p.ID) {
		t.Error("what was typed no longer waits to go up")
	}
}

// A page whose content on the server can't be read is passed over, as is
// one whose merge fails here on its own, as it kept changing while it
// merged, and the other open pages are checked all the same.
func TestTheOpenPagesAreCheckedPastOneThatFails(t *testing.T) {
	st := openStore(t)
	var ids []string
	for range 3 {
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
	// The first checked can't be read, and the second is typed in each
	// time before its merge is stored.
	old := beforeStore
	t.Cleanup(func() { beforeStore = old })
	typed := 0
	beforeStore = func(pageID string) {
		if pageID != ids[1] {
			return
		}
		typed++
		c := st.Content(pageID)
		c.Find("a").SetDelta(doc.Delta{{Text: fmt.Sprintf("typed %d", typed)}})
		if _, _, err := st.SaveContent(pageID, c); err != nil {
			t.Error(err)
		}
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "there")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentRows(r, func(id string) map[string]any {
			content := theirs.Encode()
			if id == ids[0] {
				content = newerKind
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
	if c := st.Content(ids[1]); c == nil || textOf(c, "a") != "typed 3" {
		t.Errorf("the page that kept changing has %v", c)
	}
	if c := st.Content(ids[2]); c == nil || textOf(c, "a") != "there" {
		t.Errorf("the other page has %v", c)
	}
	if !svc.watchAfter.IsZero() {
		t.Error("a page's own failure left the server alone")
	}
}

// The check asks the server nothing for an open page no one sees, while a
// pass or an opened page's refresh runs, which brings the page already,
// while the server asks for a wait or rests after a failed check, or while
// sync goes through iCloud Drive, whose changes start a pass as they come.
func TestTheOpenPagesWait(t *testing.T) {
	st := openStore(t)
	var pages []string
	for _, title := range []string{"Plan", "Notes"} {
		p, err := st.CreatePage(store.NewPageOptions{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p.ID)
	}
	var mu gosync.Mutex
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, askedPages(r)...)
		mu.Unlock()
		_, _ = w.Write([]byte("[]"))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for _, id := range pages {
		svc.open[id] = true
	}
	seen := map[string]bool{}
	svc.Watched = func(id string) bool { return seen[id] }
	svc.watchOpen(t.Context())
	seen[pages[0]] = true
	svc.running.Lock()
	svc.watchOpen(t.Context())
	svc.running.Unlock()
	svc.busyUntil = time.Now().Add(time.Minute)
	svc.watchOpen(t.Context())
	svc.busyUntil = time.Time{}
	svc.watchAfter = time.Now().Add(time.Minute)
	svc.watchOpen(t.Context())
	svc.watchAfter = time.Time{}
	svc.cloudOn.Store(true)
	svc.watchOpen(t.Context())
	if len(asked) != 0 {
		t.Fatalf("the check asked for %q while it should wait", asked)
	}
	svc.cloudOn.Store(false)
	svc.watchOpen(t.Context())
	if !slices.Equal(asked, []string{pages[0]}) {
		t.Errorf("the check asked for %q, not the page seen alone", asked)
	}
}

// A server that fails a check, as one out of order, is asked for no other
// page, and is left to the passes for a while.
func TestTheOpenPagesRestAfterTheServerFails(t *testing.T) {
	st := openStore(t)
	var requests atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for _, title := range []string{"Plan", "Notes"} {
		p, err := st.CreatePage(store.NewPageOptions{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		svc.open[p.ID] = true
	}
	svc.watchOpen(t.Context())
	if n := requests.Load(); n != 1 {
		t.Errorf("%d requests to a server that failed the first", n)
	}
	svc.watchOpen(t.Context())
	if n := requests.Load(); n != 1 {
		t.Errorf("asked again at once: %d requests", n)
	}
	if rest := time.Until(svc.watchAfter); rest <= 0 || rest > watchRest {
		t.Errorf("rests %v", rest)
	}
	svc.watchAfter = time.Time{}
	svc.watchOpen(t.Context())
	if n := requests.Load(); n != 2 {
		t.Errorf("rested, the check asked %d times", n)
	}
}

// The loop checks the open pages between its passes: what changed on the
// server comes though a pass failed before it.
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
	srv := &contentServer{content: mine.Encode(), revision: 1}
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
	failed := make(chan struct{}, 1)
	svc.OnState = func(running bool, err error) {
		if !running && err != nil {
			select {
			case failed <- struct{}{}:
			default:
			}
		}
	}
	got := make(chan string, 4)
	svc.OnPageContent = func(id string) { got <- id }
	svc.mu.Lock()
	svc.open[p.ID] = true
	svc.mu.Unlock()
	svc.Start()
	defer svc.Stop()
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass failed")
	}
	theirs := &doc.Content{Children: []*doc.Node{para("a", "from another device")}, Blocks: map[string]int64{"a": 2000}, Deleted: map[string]int64{}}
	srv.mu.Lock()
	srv.content, srv.revision = theirs.Encode(), 2
	srv.mu.Unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("what changed on the server never came to the open page")
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "from another device" {
		t.Errorf("stored %v", c)
	}
}
