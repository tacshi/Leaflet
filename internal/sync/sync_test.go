package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
