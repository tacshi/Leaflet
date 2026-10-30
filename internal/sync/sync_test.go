package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func para(id, text string) *doc.Node {
	n := &doc.Node{ID: id, Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: text}})
	return n
}

func textOf(c *doc.Content, id string) string { return c.Find(id).Delta().Plain() }

// contentServer is page_content on a fake PostgREST: one row, saved only
// over the revision a writer read.
type contentServer struct {
	mu       gosync.Mutex
	content  string
	revision int64
	updates  int
	// between runs before the first update lands, standing in for another
	// device that saved after this one read.
	between func(s *contentServer)
}

func (s *contentServer) row() map[string]any {
	return map[string]any{"content": json.RawMessage(s.content), "revision": s.revision,
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
}

func (s *contentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	switch r.Method {
	case "GET":
		out = append(out, s.row())
	case "PATCH":
		if s.between != nil {
			s.between(s)
			s.between = nil
		}
		s.updates++
		if r.URL.Query().Get("revision") == "eq."+jsonNumber(s.revision) {
			var body struct {
				Content json.RawMessage `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.content, s.revision = string(body.Content), s.revision+1
			out = append(out, s.row())
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func jsonNumber(n int64) string { raw, _ := json.Marshal(n); return string(raw) }

func TestSaveMergesWithASaveInBetween(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}

	srv := &contentServer{content: base.Encode(), revision: 1, between: func(s *contentServer) {
		theirs := base.Copy()
		theirs.Find("b").SetDelta(doc.Delta{{Text: "two, edited there"}})
		theirs.Blocks["b"] = time.Now().UnixMilli() + 1000
		s.content, s.revision = theirs.Encode(), s.revision+1
	}}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	done, err := svc.pushContent(context.Background(), p.ID)
	if err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	saved := doc.MustDecode(srv.content)
	if textOf(saved, "a") != "one, edited here" || textOf(saved, "b") != "two, edited there" {
		t.Errorf("server has %q / %q", textOf(saved, "a"), textOf(saved, "b"))
	}
	if srv.updates != 2 {
		t.Errorf("%d updates, want a refused one and a retry", srv.updates)
	}
	row, _ := st.ContentRow(p.ID)
	if row.RemoteRevision != srv.revision {
		t.Errorf("device has revision %d, server %d", row.RemoteRevision, srv.revision)
	}
	if got := textOf(doc.MustDecode(row.Content), "b"); got != "two, edited there" {
		t.Errorf("device has %q", got)
	}
}

// typeWhileStoring saves an edit to block b once, between a merge and its
// store, standing in for the user typing then.
func typeWhileStoring(t *testing.T, st *store.Store, pageID string) *bool {
	t.Helper()
	typed := false
	was := beforeStore
	beforeStore = func(id string) {
		if typed || id != pageID {
			return
		}
		typed = true
		now := st.Content(id)
		now.Find("b").SetDelta(doc.Delta{{Text: "two, typed meanwhile"}})
		if _, _, err := st.SaveContent(id, now); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeStore = was })
	return &typed
}

func TestRefreshKeepsASaveMadeWhileMerging(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	theirs := base.Copy()
	theirs.Children = append(theirs.Children, para("c", "three, added there"))
	theirs.Blocks["c"] = time.Now().UnixMilli()
	srv := &contentServer{content: theirs.Encode(), revision: 2}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })
	typed := typeWhileStoring(t, st, p.ID)

	if _, err := svc.RefreshContent(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if !*typed {
		t.Fatal("nothing was typed during the merge")
	}
	got := st.Content(p.ID)
	if textOf(got, "a") != "one, edited here" || textOf(got, "b") != "two, typed meanwhile" || got.Find("c") == nil {
		t.Errorf("device has %q / %q / %v", textOf(got, "a"), textOf(got, "b"), got.Find("c") != nil)
	}
	row, _ := st.ContentRow(p.ID)
	if row.RemoteRevision != 2 {
		t.Errorf("device has revision %d, want 2", row.RemoteRevision)
	}
}

func TestPushKeepsASaveMadeWhileStoring(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	srv := &contentServer{content: base.Encode(), revision: 1}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	typed := typeWhileStoring(t, st, p.ID)

	done, err := svc.pushContent(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !*typed {
		t.Fatal("nothing was typed while the push stored")
	}
	if done {
		t.Error("the push is done, though the typing still has to go")
	}
	got := st.Content(p.ID)
	if textOf(got, "a") != "one, edited here" || textOf(got, "b") != "two, typed meanwhile" {
		t.Errorf("device has %q / %q", textOf(got, "a"), textOf(got, "b"))
	}
	if !st.HasPending("page_content", p.ID) {
		t.Error("the typing is no longer queued")
	}
}

// A block changed on the server and typed here while the push was out
// is a conflict: the side the merge does not keep stays to recover.
func TestPushKeepsAConflictTypedWhileStoring(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	theirs := base.Copy()
	theirs.Find("b").SetDelta(doc.Delta{{Text: "two, edited there"}})
	theirs.Blocks["b"] = 2000
	srv := &contentServer{content: theirs.Encode(), revision: 2}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	typed := typeWhileStoring(t, st, p.ID)

	if _, err := svc.pushContent(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if !*typed {
		t.Fatal("nothing was typed while the push stored")
	}
	conflicts, err := st.Conflicts()
	if err != nil {
		t.Fatal(err)
	}
	var sides []string
	for _, c := range conflicts {
		if c.BlockID == "b" && c.LocalNode != nil && c.RemoteNode != nil {
			sides = append(sides, c.LocalNode.Delta().Plain()+" / "+c.RemoteNode.Delta().Plain())
		}
	}
	if len(sides) != 1 || sides[0] != "two, typed meanwhile / two, edited there" {
		t.Errorf("conflicts on b: %q", sides)
	}
}

func TestPullResumesAtARowThatFailedToStore(t *testing.T) {
	st := openStore(t)
	const at = "2026-10-26T09:00:00.123456+00:00"
	var queries []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("or"))
		rows := []map[string]any{{"id": "p1", "updated_at": at}, {"id": "p2", "updated_at": at}, {"id": "p3", "updated_at": at}}
		if len(queries) > 1 {
			rows = rows[1:]
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	var stored []string
	failOnce := true
	apply := func(r map[string]any) error {
		id := store.S(r, "id")
		if id == "p2" && failOnce {
			failOnce = false
			return errors.New("disk full")
		}
		stored = append(stored, id)
		return nil
	}
	ctx := context.Background()
	if err := svc.pullTable(ctx, "pages", "*", apply); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Value("sync.cursor.pages"); v != at+"|p1" {
		t.Fatalf("cursor %q, want it to stop before p2", v)
	}
	if err := svc.pullTable(ctx, "pages", "*", apply); err != nil {
		t.Fatal(err)
	}
	// From the overlap behind the cursor, where p1 is passed over.
	if queries[1] != "" {
		t.Errorf("second pull asked for %s", queries[1])
	}
	if strings.Join(stored, ",") != "p1,p2,p3" {
		t.Errorf("stored %v", stored)
	}
	if v, _ := st.Value("sync.cursor.pages"); v != at+"|p3" {
		t.Errorf("cursor %q", v)
	}
}

// A row whose change commits after rows stamped later, which the pull has
// passed, is found by the next pull, which looks again behind its cursor.
// The rows it has already are not stored again.
func TestPullFindsARowThatCommittedLate(t *testing.T) {
	st := openStore(t)
	t2 := time.Date(2026, 10, 26, 9, 0, 5, 123456000, time.UTC)
	t1 := t2.Add(-3 * time.Second)
	row := func(id string, at time.Time) map[string]any {
		return map[string]any{"id": id, "space_id": st.Session().SpaceID, "title": id, "sort_key": "V",
			"created_at": at.Format(time.RFC3339Nano), "updated_at": at.Format(time.RFC3339Nano)}
	}
	var filters []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters = append(filters, r.URL.Query().Get("updated_at"))
		rows := []map[string]any{row("p1", t2)}
		if len(filters) > 1 {
			// p0 committed late, stamped before p1.
			rows = []map[string]any{row("p0", t1), row("p1", t2)}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	stored := map[string]int{}
	apply := func(r map[string]any) error {
		stored[store.S(r, "id")]++
		return st.PutRemotePage(r)
	}
	ctx := context.Background()
	for range 3 {
		if err := svc.pullTable(ctx, "pages", "*", apply); err != nil {
			t.Fatal(err)
		}
	}
	if want := "gt." + t2.Add(-overlap).Format(time.RFC3339Nano); filters[1] != want {
		t.Errorf("second pull asked for %s, want %s", filters[1], want)
	}
	if _, err := st.Page("p0"); err != nil {
		t.Fatalf("the late row never came: %v", err)
	}
	if stored["p0"] != 1 || stored["p1"] != 1 {
		t.Errorf("stored %v, each once", stored)
	}
	if v, _ := st.Value("sync.cursor.pages"); v != t2.Format(time.RFC3339Nano)+"|p1" {
		t.Errorf("cursor %q", v)
	}
}

// An overlap fuller than a page of rows reads on past it.
func TestPullReadsPastAFullOverlap(t *testing.T) {
	st := openStore(t)
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		rows := []map[string]any{{"id": "a", "updated_at": at}, {"id": "b", "updated_at": at}}
		if len(asked) > 1 {
			rows = []map[string]any{{"id": "c", "updated_at": at}}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	rows, err := svc.changed(context.Background(), "pages", "id", "*", cursor{at, "b"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(asked) != 2 || !strings.Contains(asked[1], "or=") {
		t.Errorf("rows %v, asked %v", rows, asked)
	}
}

// Content the device cannot read now stays queued; only a page that is
// gone takes its content off the queue.
func TestPushKeepsContentItCannotRead(t *testing.T) {
	st := openStore(t)
	svc := New(st, remote.SignedInForTest("http://127.0.0.1:1", st, "u1"))
	if done, err := svc.pushContent(context.Background(), "gone"); !done || err != nil {
		t.Errorf("gone page: done %v, %v", done, err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if done, err := svc.pushContent(context.Background(), p.ID); done || err == nil {
		t.Errorf("unreadable page: done %v, %v", done, err)
	}
	if _, published, err := svc.publishContent(st.DeviceID(), p.ID); published != nil || err == nil {
		t.Errorf("unreadable page published %v, %v", published, err)
	}
}

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
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })
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

// Content can come before its page's row: it is fetched once the row has
// come, though the content pull has moved past it.
func TestContentWaitsForItsPage(t *testing.T) {
	st := openStore(t)
	const id = "00000000-0000-0000-0000-0000000000aa"
	theirs := &doc.Content{Children: []*doc.Node{para("a", "written first")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	lists := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if strings.Contains(r.URL.Query().Get("select"), "content") {
			out = append(out, map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at})
		} else if lists++; lists == 1 {
			out = append(out, map[string]any{"page_id": id, "updated_at": at})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })

	ctx := context.Background()
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if st.Content(id) != nil {
		t.Fatal("content stored without its page")
	}
	if err := st.PutRemotePage(map[string]any{"id": id, "space_id": st.Session().SpaceID, "title": "Late", "sort_key": "V",
		"created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if c := st.Content(id); c == nil || textOf(c, "a") != "written first" {
		t.Errorf("stored %v", c)
	}
	if len(svc.waiting()) != 0 {
		t.Errorf("still waiting for %v", svc.waiting())
	}
}

// A page shared with the user arrives with the pages under it and their
// comments, though none of them changed since the pulls last looked.
func TestPullBringsAPageSharedWithTheUser(t *testing.T) {
	st := openStore(t)
	const (
		root  = "00000000-0000-0000-0000-0000000000e1"
		child = "00000000-0000-0000-0000-0000000000e2"
		other = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	page := func(id, parent string) map[string]any {
		r := map[string]any{"id": id, "space_id": other, "title": id, "sort_key": "V", "created_at": old, "updated_at": old}
		if parent != "" {
			r["parent_id"] = parent
		}
		return r
	}
	var grantFilter string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out := []map[string]any{}
		switch strings.TrimPrefix(r.URL.Path, "/rest/v1/") {
		case "page_permissions":
			grantFilter = q.Get("user_id")
			if q.Get("updated_at") == "gt.1970-01-01T00:00:00Z" {
				out = append(out, map[string]any{"page_id": root, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
			}
		case "pages":
			switch {
			case q.Get("id") == "eq."+root:
				out = append(out, page(root, ""))
			case q.Get("parent_id") == `in.("`+root+`")`:
				out = append(out, page(child, root))
			}
		case "comments":
			if strings.Contains(q.Get("page_id"), child) {
				out = append(out, map[string]any{"id": "c1", "page_id": child, "block_id": "b1", "author_id": "u2", "body": "Look here",
					"created_at": old, "updated_at": old})
			}
		case "profiles":
			out = append(out, map[string]any{"id": "u2", "display_name": "Sam"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.pullShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	if grantFilter != "eq.u1" {
		t.Errorf("grants filtered by %q", grantFilter)
	}
	for _, id := range []string{root, child} {
		if _, err := st.Page(id); err != nil {
			t.Errorf("page %s: %v", id, err)
		}
	}
	threads, _ := st.Threads(child)
	if len(threads) != 1 || threads[0].Root.AuthorName != "Sam" {
		t.Errorf("threads %+v", threads)
	}
	if w := svc.waiting(); !w[root] || !w[child] {
		t.Errorf("content not asked for: %v", w)
	}
	// The next pull has nothing new.
	if err := svc.pullShared(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A page shared with the user waits for them as an invite: it is pulled
// only once accepted, which moves the share on so the next pull brings
// it. Declining one takes it off the list.
func TestPullBringsASharedPageOnceAccepted(t *testing.T) {
	st := openStore(t)
	const (
		root  = "00000000-0000-0000-0000-0000000000e1"
		other = "00000000-0000-0000-0000-0000000000e3"
		space = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	var mu gosync.Mutex
	type share struct {
		at       string
		accepted bool
	}
	shares := map[string]*share{root: {at: old}, other: {at: old}}
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		out := []map[string]any{}
		switch path := strings.TrimPrefix(r.URL.Path, "/rest/v1/"); path {
		case "page_permissions":
			asked = append(asked, q.Get("accepted_at"))
			after, _ := time.Parse(time.RFC3339Nano, strings.TrimPrefix(q.Get("updated_at"), "gt."))
			for _, id := range []string{root, other} {
				sh := shares[id]
				if sh == nil || !sh.accepted && q.Get("accepted_at") == "not.is.null" {
					continue
				}
				if at, _ := time.Parse(time.RFC3339Nano, sh.at); at.After(after) {
					out = append(out, map[string]any{"page_id": id, "updated_at": sh.at})
				}
			}
		case "pages":
			if q.Get("id") == "eq."+root {
				out = append(out, map[string]any{"id": root, "space_id": space, "title": "Roadmap", "sort_key": "V", "created_at": old, "updated_at": old})
			}
		case "rpc/page_invites":
			for _, id := range []string{root, other} {
				if sh := shares[id]; sh != nil && !sh.accepted {
					out = append(out, map[string]any{"page_id": id, "title": "Page " + id[len(id)-2:], "role": "editor",
						"inviter_name": "Sam", "created_at": old})
				}
			}
		case "rpc/accept_page_invite", "rpc/decline_page_invite":
			var body struct {
				PageID string `json:"p_page_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if path == "rpc/decline_page_invite" {
				delete(shares, body.PageID)
			} else if sh := shares[body.PageID]; sh != nil {
				sh.accepted, sh.at = true, time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	changes := 0
	svc.OnInvites = func() { changes++ }
	ctx := context.Background()
	// An earlier pull went past the time the page was shared.
	if err := st.SetValue("sync.cursor.page_permissions", "2026-06-01T00:00:00Z|"); err != nil {
		t.Fatal(err)
	}

	if err := svc.pullShared(ctx); err != nil {
		t.Fatal(err)
	}
	svc.readInvites(ctx)
	if len(asked) != 1 || asked[0] != "not.is.null" {
		t.Errorf("shares asked for with accepted_at %q", asked)
	}
	if _, err := st.Page(root); err == nil {
		t.Fatal("a page not accepted yet was pulled")
	}
	invites := svc.Invites()
	if len(invites) != 2 || invites[0].Inviter != "Sam" || invites[0].Role != "editor" || changes != 1 {
		t.Fatalf("invites %+v, %d changes", invites, changes)
	}
	// Read again, the same list is no change.
	svc.readInvites(ctx)
	if changes != 1 {
		t.Errorf("%d changes for the same invites", changes)
	}

	if err := svc.AcceptInvite(ctx, root); err != nil {
		t.Fatal(err)
	}
	if invites := svc.Invites(); len(invites) != 1 || invites[0].PageID != other {
		t.Errorf("invites after accepting %+v", invites)
	}
	if err := svc.pullShared(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := st.Page(root); err != nil || p.Title != "Roadmap" {
		t.Fatalf("the accepted page: %+v, %v", p, err)
	}
	if w := svc.waiting(); !w[root] {
		t.Errorf("content not asked for: %v", w)
	}

	if err := svc.DeclineInvite(ctx, other); err != nil {
		t.Fatal(err)
	}
	svc.readInvites(ctx)
	if invites := svc.Invites(); len(invites) != 0 {
		t.Errorf("invites after declining %+v", invites)
	}
	if _, err := st.Page(other); err == nil {
		t.Error("a declined page was pulled")
	}

	// Signed out, there are none.
	shares[other] = &share{at: old}
	svc.readInvites(ctx)
	svc.ClearInvites()
	if invites := svc.Invites(); len(invites) != 0 {
		t.Errorf("invites after signing out %+v", invites)
	}
}

// A shared page pulled again keeps a rename made here that has not gone
// up yet.
func TestPullSharedKeepsChangesWaiting(t *testing.T) {
	st := openStore(t)
	const (
		root  = "00000000-0000-0000-0000-0000000000e1"
		other = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	server := map[string]any{"id": root, "space_id": other, "title": "Old title", "sort_key": "V", "created_at": old, "updated_at": old}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		switch strings.TrimPrefix(r.URL.Path, "/rest/v1/") {
		case "page_permissions":
			out = append(out, map[string]any{"page_id": root, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "pages":
			if r.URL.Query().Get("id") == "eq."+root {
				out = append(out, server)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := st.PutRemotePage(server); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(root, "New title"); err != nil {
		t.Fatal(err)
	}
	if err := svc.pullShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Page(root); p.Title != "New title" {
		t.Errorf("title %q", p.Title)
	}
}

// Resolving someone else's comment updates only what may change, and a
// comment the server does not have yet goes in whole.
func TestCommentsPushAsUpdates(t *testing.T) {
	st := openStore(t)
	var requests []string
	var patched map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Query().Get("id"))
		out := []map[string]any{}
		if r.Method == "PATCH" {
			_ = json.NewDecoder(r.Body).Decode(&patched)
			if r.URL.Query().Get("id") == "eq.theirs" {
				out = append(out, map[string]any{"id": "theirs"})
			}
		} else {
			out = append(out, map[string]any{"id": "new"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	done, err := svc.pushComment(ctx, "u1", map[string]any{"id": "theirs", "author_id": "u2", "body": "Look", "resolved": true, "deleted_at": nil})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if len(patched) != 1 || patched["resolved"] != true {
		t.Errorf("sent %v", patched)
	}
	done, err = svc.pushComment(ctx, "u1", map[string]any{"id": "new", "body": "Mine", "resolved": false})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if strings.Join(requests, ",") != "PATCH eq.theirs,PATCH eq.new,POST " {
		t.Errorf("requests %v", requests)
	}
	// A resolve made here, of a comment deleted elsewhere, sends only the
	// resolve, not this device's deleted_at.
	patched = nil
	done, err = svc.pushComment(ctx, "u1", map[string]any{"id": "theirs", "author_id": "u1", "body": "Look", "resolved": true,
		"deleted_at": nil, store.ChangedKey: []any{"resolved", "updated_at"}})
	if err != nil || !done {
		t.Fatalf("done %v, %v", done, err)
	}
	if len(patched) != 1 || patched["resolved"] != true {
		t.Errorf("sent %v", patched)
	}
}

// A page changed here sends only what changed, so an offline rename does
// not undo a move or a deletion made on another device meanwhile; one the
// server does not have goes up whole.
func TestPagePushSendsOnlyItsChanges(t *testing.T) {
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	clear := func() {
		entries, _ := st.Pending()
		for _, e := range entries {
			_ = st.Dequeue(e)
		}
	}
	clear() // the server has the page now
	if err := st.RenamePage(p.ID, "Plan for Q3"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(p.ID, "Plan for Q4"); err != nil {
		t.Fatal(err)
	}

	onServer := true
	var methods []string
	var sent map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		sent = nil
		_ = json.NewDecoder(r.Body).Decode(&sent)
		out := []map[string]any{}
		if r.Method != "PATCH" || onServer {
			out = append(out, map[string]any{"id": p.ID})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()

	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "PATCH" || len(sent) != 1 || sent["title"] != "Plan for Q4" {
		t.Errorf("requests %v, sent %v", methods, sent)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d still queued", st.PendingCount())
	}

	// Nothing changed: nothing queued.
	if err := st.RenamePage(p.ID, "Plan for Q4"); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 {
		t.Errorf("an unchanged page queued")
	}

	onServer, methods = false, nil
	if err := st.MovePage(p.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePage(p.ID, "Plan"); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "PATCH,POST" || sent["space_id"] == nil || sent[store.ChangedKey] != nil {
		t.Errorf("requests %v, sent %v", methods, sent)
	}
}

// Signing in uploads the audio of recordings made before, once they are
// done, and none whose audio is gone.
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

// Signing in adopts the device's space only once everything in it is
// queued: a pass that could not queue it all leaves the space to adopt
// again, and what was not queued is queued before the next pass. What
// is made meanwhile goes into the account's space.
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
	// The disk refuses the folder's entry, as one that is full would.
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

// A deleted file's object leaves Storage once the server has the row that
// says so, and not before: while the row cannot go up, the removal waits.
// An object the server does not have counts as removed.
func TestDeletedFileLeavesStorageAfterItsRow(t *testing.T) {
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
	var mu gosync.Mutex
	var requests []string
	var removed []string
	refuseRow, missing := true, false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/rest/v1/files" && refuseRow:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "refused"})
			return
		case r.URL.Path == "/storage/v1/object/files":
			var body struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			removed = append(removed, body.Prefixes...)
			if missing {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })

	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || st.PendingCount() != 2 {
		t.Fatalf("removed %v before the row went up; %d waiting", removed, st.PendingCount())
	}
	refuseRow = false
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != f.StoragePath || st.PendingCount() != 0 {
		t.Errorf("removed %v, %d waiting", removed, st.PendingCount())
	}
	if requests[len(requests)-1] != "DELETE /storage/v1/object/files" || requests[len(requests)-2] != "POST /rest/v1/files" {
		t.Errorf("requests %v", requests)
	}

	// Gone from the server already.
	g, err := st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFile(g); err != nil {
		t.Fatal(err)
	}
	missing = true
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	if st.PendingCount() != 0 {
		t.Errorf("%d waiting after a removal of what was not there", st.PendingCount())
	}
}

// Sending everything again sends a deleted file's row, so its deletion
// still reaches the other devices, but none of its contents.
func TestRequeueSendsDeletedFiles(t *testing.T) {
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
	if err := svc.requeueAll(st.Session().SpaceID); err != nil {
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
}

// Content here that cannot be read is neither sent over the server's nor
// merged with it as an empty page: the server keeps its copy, and the
// change waits.
func TestUnreadableContentIsNotSynced(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	const broken = `{"children": [`
	if _, err := st.DB().Exec(`UPDATE page_contents SET content = ? WHERE page_id = ?`, broken, p.ID); err != nil {
		t.Fatal(err)
	}
	srv := &contentServer{content: base.Encode(), revision: 1}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if done, err := svc.pushContent(context.Background(), p.ID); done || !errors.Is(err, errUnreadable) {
		t.Errorf("push: done %v, %v", done, err)
	}
	if srv.updates != 0 || srv.content != base.Encode() {
		t.Errorf("server saved over: %d updates, %s", srv.updates, srv.content)
	}

	// The server moves on: the change waiting here is not merged either.
	theirs := base.Copy()
	theirs.Children = append(theirs.Children, para("b", "two, added there"))
	theirs.Blocks["b"] = time.Now().UnixMilli()
	srv.content, srv.revision = theirs.Encode(), 2
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })
	if _, err := svc.RefreshContent(context.Background(), p.ID); !errors.Is(err, errUnreadable) {
		t.Errorf("refresh: %v", err)
	}
	if row, _ := st.ContentRow(p.ID); row.Content != broken || !st.HasPending("page_content", p.ID) {
		t.Errorf("content %q, pending %v", row.Content, st.HasPending("page_content", p.ID))
	}
}

// Content on the server that this device cannot read, as a newer version
// of Leaflet may write, is neither merged as an empty page nor saved
// over: the server keeps its copy, the device keeps its own, the change
// waits, and the other pages' content still comes.
func TestServerContentItCannotReadIsNotSynced(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	const unreadable = `{"document": {"type": "page", "id": "root", "children": [{"id": "x", "type": "paragraph", "data": ["of a newer kind"]}]}, "meta": {"v": 1, "blocks": {"x": 5}}}`
	srv := &contentServer{content: unreadable, revision: 2}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })
	ctx := context.Background()

	if done, err := svc.pushContent(ctx, p.ID); done || !errors.Is(err, errServerUnreadable) {
		t.Errorf("push: done %v, %v", done, err)
	}
	if srv.updates != 0 || srv.content != unreadable {
		t.Errorf("server saved over: %d updates, %s", srv.updates, srv.content)
	}
	if _, err := svc.RefreshContent(ctx, p.ID); !errors.Is(err, errServerUnreadable) {
		t.Errorf("refresh: %v", err)
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "one, edited here" || textOf(c, "b") != "two" || !st.HasPending("page_content", p.ID) {
		t.Errorf("content %v, pending %v", c, st.HasPending("page_content", p.ID))
	}

	// A pull leaves the page waiting for content it can read, and goes on.
	at := time.Now().UTC().Format(time.RFC3339Nano)
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	theirs := &doc.Content{Children: []*doc.Node{para("c", "from another device")}, Blocks: map[string]int64{"c": 1000}, Deleted: map[string]int64{}}
	hs2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		switch {
		case !strings.Contains(r.URL.Query().Get("select"), "content"):
			out = append(out, map[string]any{"page_id": p.ID, "updated_at": at}, map[string]any{"page_id": other.ID, "updated_at": at})
		case r.URL.Query().Get("page_id") == "eq."+p.ID:
			out = append(out, map[string]any{"content": json.RawMessage(unreadable), "revision": 2, "updated_at": at})
		default:
			out = append(out, map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs2.Close()
	svc = New(st, remote.SignedInForTest(hs2.URL, st, "u1"))
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if c := st.Content(p.ID); c == nil || textOf(c, "a") != "one, edited here" {
		t.Errorf("content %v", c)
	}
	if c := st.Content(other.ID); c == nil || textOf(c, "c") != "from another device" {
		t.Errorf("the other page has %v", c)
	}
	if !svc.waiting()[p.ID] {
		t.Errorf("waiting for %v", svc.waiting())
	}
}

// Sync loads the members of a page shared with the user, so an agent
// outside the app may edit it and the pages inside it as the share says,
// opened in the app or not. A share accepted before sync loaded them gets
// them on the next pull.
func TestPullLoadsSharedPagesMembers(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	const (
		root    = "00000000-0000-0000-0000-0000000000e1"
		child   = "00000000-0000-0000-0000-0000000000e2"
		earlier = "00000000-0000-0000-0000-0000000000e3"
		other   = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	page := func(id, parent string) map[string]any {
		r := map[string]any{"id": id, "space_id": other, "title": id, "sort_key": "V", "created_at": old, "updated_at": old}
		if parent != "" {
			r["parent_id"] = parent
		}
		return r
	}
	roles := map[string]string{root: "editor", earlier: "viewer"}
	var mu gosync.Mutex
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var out any = []map[string]any{}
		switch strings.TrimPrefix(r.URL.Path, "/rest/v1/") {
		case "page_permissions":
			if q.Get("updated_at") == "gt.1970-01-01T00:00:00Z" {
				out = []map[string]any{{"page_id": root, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}}
			}
		case "pages":
			switch {
			case q.Get("id") == "eq."+root:
				out = []map[string]any{page(root, "")}
			case q.Get("parent_id") == `in.("`+root+`")`:
				out = []map[string]any{page(child, root)}
			}
		case "rpc/page_members":
			var args struct {
				PageID string `json:"p_page_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&args)
			mu.Lock()
			asked = append(asked, args.PageID)
			mu.Unlock()
			out = []map[string]any{
				{"user_id": "u2", "role": "owner", "accepted": true},
				{"user_id": "u1", "role": roles[args.PageID], "accepted": true},
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.pullShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root, child} {
		if p, err := st.Page(id); err != nil || !st.CanEdit(p) {
			t.Errorf("shared to edit, %s: can edit %v, %v", id, st.CanEdit(p), err)
		}
	}

	if err := st.PutRemotePage(page(earlier, "")); err != nil {
		t.Fatal(err)
	}
	asked = nil
	svc.pullMembers(context.Background())
	if strings.Join(asked, ",") != earlier {
		t.Errorf("members asked of %v, want only %s", asked, earlier)
	}
	if p, _ := st.Page(earlier); st.CanEdit(p) || len(st.MembersOrNil(earlier)) != 2 {
		t.Errorf("shared to view: can edit %v, members %+v", st.CanEdit(p), st.MembersOrNil(earlier))
	}
}

// A shared page's tree comes whole however many rows it has: the server
// sends a page of them at a time, and leaves out the rest without saying.
func TestPullSharedReadsEveryPageOfRows(t *testing.T) {
	st := openStore(t)
	treeIDs, treeRows = 1, 2
	t.Cleanup(func() { treeIDs, treeRows = 100, 500 })
	const (
		root  = "00000000-0000-0000-0000-0000000000e1"
		kid1  = "00000000-0000-0000-0000-0000000000e2"
		kid2  = "00000000-0000-0000-0000-0000000000e3"
		space = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	page := func(id, parent string) map[string]any {
		r := map[string]any{"id": id, "space_id": space, "title": id, "sort_key": "V", "created_at": old, "updated_at": old}
		if parent != "" {
			r["parent_id"] = parent
		}
		return r
	}
	tables := map[string][]map[string]any{
		"pages": {page(root, ""), page(kid1, root), page(kid2, root)},
	}
	for i, pageID := range []string{kid1, kid1, kid1, kid1, kid1, kid2, root} {
		tables["comments"] = append(tables["comments"], map[string]any{"id": "c" + string(rune('1'+i)), "page_id": pageID,
			"block_id": "b1", "author_id": "u2", "body": "Look here", "created_at": old, "updated_at": old})
	}
	// in reports whether a row's col is one a filter names.
	in := func(filter, v string) bool {
		list, ok := strings.CutPrefix(filter, "in.(")
		return ok && strings.Contains(strings.TrimSuffix(list, ")"), `"`+v+`"`)
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out := []map[string]any{}
		table := strings.TrimPrefix(r.URL.Path, "/rest/v1/")
		switch table {
		case "page_permissions":
			if q.Get("updated_at") == "gt.1970-01-01T00:00:00Z" {
				out = append(out, map[string]any{"page_id": root, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
			}
		case "pages", "comments", "files", "recordings":
			after, _ := strings.CutPrefix(q.Get("id"), "gt.")
			for _, row := range tables[table] {
				id := store.S(row, "id")
				switch {
				case q.Get("id") == "eq."+id,
					in(q.Get("parent_id"), store.S(row, "parent_id")) && id > after,
					in(q.Get("page_id"), store.S(row, "page_id")) && id > after:
					out = append(out, row)
				}
			}
			// The server sends at most two rows here, as it sends at most
			// 1,000.
			if len(out) > 2 {
				out = out[:2]
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.pullShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root, kid1, kid2} {
		if _, err := st.Page(id); err != nil {
			t.Errorf("page %s: %v", id, err)
		}
	}
	comments, _ := st.AllComments()
	if len(comments) != len(tables["comments"]) {
		t.Errorf("%d of %d comments", len(comments), len(tables["comments"]))
	}
}

// An upload stays queued while the recording cannot take its path here:
// else nothing would send the audio again, and the other devices could
// not play it.
func TestUploadWaitsForTheRecordingsPath(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Standup"})
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir() + "/a.wav"
	if err := os.WriteFile(local, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := st.CreateRecording(p.ID, "b1", "", "", local, "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		_ = st.Dequeue(e)
	}
	if err := st.QueueUpload(r); err != nil {
		t.Fatal(err)
	}
	// The disk refuses the recording's path, as a full one would.
	if _, err := st.DB().Exec(`CREATE TRIGGER refuse BEFORE UPDATE ON recordings
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	uploads := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.URL.Path == "/storage/v1/object/recordings/u1/"+r.ID+".wav" {
			uploads++
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })

	// A failure here ends the pass, as one that stores a page's content does.
	if err := svc.push(ctx); err == nil {
		t.Error("the pass went on")
	}
	if !st.HasPending("storage", "recordings/"+r.ID) {
		t.Fatal("the upload left the queue without the recording's path")
	}
	if _, err := st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if err := svc.push(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Recording(r.ID)
	if uploads != 2 || got.AudioPath != "u1/"+r.ID+".wav" || st.HasPending("storage", "recordings/"+r.ID) {
		t.Errorf("%d uploads, path %q, still queued %v", uploads, got.AudioPath, st.HasPending("storage", "recordings/"+r.ID))
	}
}

// A copied page's file and recording that this device has only stored go
// up as the copy's own, from the originals' stored copies; a copy whose
// original is gone has nothing to send.
func TestCopiesGoUpFromTheOriginals(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Meeting"})
	if err != nil {
		t.Fatal(err)
	}
	space := st.Session().SpaceID
	f := store.File{ID: "00000000-0000-0000-0000-0000000000f1", SpaceID: space, PageID: p.ID, Name: "plan.pdf",
		MimeType: "application/pdf", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.StoragePath = space + "/" + f.ID + "/plan.pdf"
	gone := f
	gone.ID, gone.Name, gone.StoragePath = "00000000-0000-0000-0000-0000000000f2", "gone.pdf", space+"/00000000-0000-0000-0000-0000000000f2/gone.pdf"
	for _, x := range []store.File{f, gone} {
		if err := st.PutFile(x); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.CreateRecording(p.ID, "b1", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err = st.UpdateRecording(r.ID, func(x *store.Recording) { x.Status, x.AudioPath = "ready", "u0/"+r.ID+".wav" }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "b1", Type: "recording", Data: map[string]any{"recording_id": r.ID}},
		{ID: "b2", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
		{ID: "b3", Type: "file", Data: map[string]any{"name": gone.Name, "file_id": gone.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	cp, err := st.CopyPage(p.ID, store.CopyOptions{Title: "Meeting copy"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := st.Content(cp.ID).Children
	recID, fileID, goneID := blocks[0].Str("recording_id"), blocks[1].Str("file_id"), blocks[2].Str("file_id")

	var mu gosync.Mutex
	uploaded := map[string]string{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch req.URL.Path {
		case "/storage/v1/object/authenticated/recordings/u0/" + r.ID + ".wav":
			_, _ = w.Write([]byte("RIFF"))
			return
		case "/storage/v1/object/authenticated/files/" + f.StoragePath:
			_, _ = w.Write([]byte("%PDF"))
			return
		case "/storage/v1/object/authenticated/files/" + gone.StoragePath:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
			return
		}
		if rest, ok := strings.CutPrefix(req.URL.Path, "/storage/v1/object/"); ok && req.Method == http.MethodPost {
			body, _ := io.ReadAll(req.Body)
			uploaded[rest] = string(body)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	was := online
	online = func() bool { return true }
	t.Cleanup(func() { online = was })

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	cf, _ := st.File(fileID)
	if uploaded["recordings/u1/"+recID+".wav"] != "RIFF" || uploaded["files/"+cf.StoragePath] != "%PDF" || len(uploaded) != 2 {
		t.Errorf("uploaded %v", uploaded)
	}
	if c, _ := st.Recording(recID); c.AudioPath != "u1/"+recID+".wav" {
		t.Errorf("the copy plays %q", c.AudioPath)
	}
	if data, err := os.ReadFile(cf.LocalPath); err != nil || string(data) != "%PDF" {
		t.Errorf("the copy here: %q, %v", data, err)
	}
	for _, id := range []string{"recordings/" + recID, "files/" + fileID, "files/" + goneID} {
		if st.HasPending("storage", id) {
			t.Errorf("%s is still queued", id)
		}
	}
}

// A share withdrawn takes the page from this device, with the pages under
// it and their changes waiting, once pullMembers loads its members again
// and is refused; the user's own pages stay. A page the server still
// shows, though refused, stays without the roles that let it be edited.
func TestWithdrawnShareLeavesTheDevice(t *testing.T) {
	const (
		root  = "00000000-0000-0000-0000-0000000000e1"
		child = "00000000-0000-0000-0000-0000000000e2"
		other = "00000000-0000-0000-0000-0000000000ff"
	)
	for _, shown := range []bool{false, true} {
		st := openStore(t)
		if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
			t.Fatal(err)
		}
		mine, err := st.CreatePage(store.NewPageOptions{Title: "Mine"})
		if err != nil {
			t.Fatal(err)
		}
		old := "2026-01-01T00:00:00Z"
		for _, r := range []map[string]any{
			{"id": root, "space_id": other, "title": "Root", "sort_key": "V", "created_at": old, "updated_at": old},
			{"id": child, "space_id": other, "parent_id": root, "title": "Child", "sort_key": "V", "created_at": old, "updated_at": old},
		} {
			if err := st.PutRemotePage(r); err != nil {
				t.Fatal(err)
			}
		}
		editor := []store.Member{{UserID: "u1", Role: "editor", Accepted: true}}
		for _, id := range []string{root, child} {
			if err := st.SetMembers(id, editor); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.AddComment(child, "b1", "waiting", "", "", "", ""); err != nil {
			t.Fatal(err)
		}
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/rpc/page_members"):
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "42501", "message": "forbidden"})
			case strings.HasSuffix(r.URL.Path, "/pages") && shown:
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": root}})
			default:
				_ = json.NewEncoder(w).Encode([]map[string]any{})
			}
		}))
		svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
		svc.pullMembers(context.Background())
		hs.Close()

		if _, err := st.Page(mine.ID); err != nil {
			t.Fatalf("shown %v: own page removed: %v", shown, err)
		}
		for _, id := range []string{root, child} {
			p, err := st.Page(id)
			switch {
			case !shown && err == nil:
				t.Errorf("%s stayed after the share went", id)
			case shown && err != nil:
				t.Errorf("%s removed though the server still shows it: %v", id, err)
			case shown && st.CanEdit(p):
				t.Errorf("%s still editable after the share went", id)
			}
		}
		entries, _ := st.Pending()
		left := 0
		for _, e := range entries {
			if e.PageID == child {
				left++
			}
		}
		if !shown && left != 0 || shown && left == 0 {
			t.Errorf("shown %v: %d changes of the shared pages waiting", shown, left)
		}
	}
}

// Content from the server that is not a page's, as null, is refused rather
// than taken for an empty page in place of the one here.
func TestRefreshRefusesContentThatIsNotAPage(t *testing.T) {
	st := openStore(t)
	base := doc.FromBlocks([]*doc.Node{para("a", "important text")})
	p, err := st.CreatePage(store.NewPageOptions{Content: base})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSyncedContent(p.ID, base, base.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Pending()
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, content := range []string{"null", `{}`} {
		srv := httptest.NewServer(&contentServer{content: content, revision: 2})
		s := New(st, remote.SignedInForTest(srv.URL, st, "user"))
		if _, err := s.refreshRemote(t.Context(), p.ID); err == nil {
			t.Errorf("%s: refresh took it", content)
		}
		srv.Close()
		if got := textOf(st.Content(p.ID), "a"); got != "important text" {
			t.Errorf("%s: page now %q", content, got)
		}
	}
}
