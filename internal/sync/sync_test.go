package sync

import (
	"context"
	"encoding/json"
	"errors"
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
	want := `(updated_at.gt."` + at + `",and(updated_at.eq."` + at + `",id.gt."p1"))`
	if queries[1] != want {
		t.Errorf("second pull asked for\n%s\nwant\n%s", queries[1], want)
	}
	if strings.Join(stored, ",") != "p1,p2,p3" {
		t.Errorf("stored %v", stored)
	}
	if v, _ := st.Value("sync.cursor.pages"); v != at+"|p3" {
		t.Errorf("cursor %q", v)
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
