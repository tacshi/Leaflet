package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// TestMain has an entry that failed go again on the next push, as the
// tests that push again expect; TestFailedEntriesWait has it wait.
func TestMain(m *testing.M) {
	retryBase = 0
	os.Exit(m.Run())
}

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

// putSynced stores c as a page's content that came from the server, the
// base it agrees on, over what the page has.
func putSynced(t *testing.T, st *store.Store, pageID string, c *doc.Content) {
	t.Helper()
	row, _ := st.ContentRow(pageID)
	if err := st.PutSyncedContentOver(pageID, row.Content, c, c.Encode(), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
}

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

// askedPages are the pages a request names by page_id, as page_id=eq.x
// or page_id=in.("x","y").
func askedPages(r *http.Request) []string {
	v := r.URL.Query().Get("page_id")
	if id, ok := strings.CutPrefix(v, "eq."); ok {
		return []string{id}
	}
	list, ok := strings.CutPrefix(v, "in.(")
	if !ok {
		return nil
	}
	var out []string
	for _, id := range strings.Split(strings.TrimSuffix(list, ")"), ",") {
		out = append(out, strings.Trim(id, `"`))
	}
	return out
}

// contentRows answers a request for pages' content with row for each page
// it names, as the server does, with the page's ID.
func contentRows(r *http.Request, row func(id string) map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, id := range askedPages(r) {
		if m := row(id); m != nil {
			m["page_id"] = id
			out = append(out, m)
		}
	}
	return out
}

// fullPass is a service on a server a whole pass runs against: content
// answers for page_content, and every other table has no rows and takes
// what is sent. requests are those it had, as "GET /rest/v1/comments".
func fullPass(t *testing.T, st *store.Store, content http.Handler) (*Service, *[]string) {
	t.Helper()
	// The space is adopted, as after the first pass.
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var requests []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if content != nil && r.URL.Path == "/rest/v1/page_content" {
			content.ServeHTTP(w, r)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(hs.Close)
	return New(st, remote.SignedInForTest(hs.URL, st, "u1")), &requests
}

// onlyContent is srv for the reads of a page's content and its saves; the
// pull's list of changed content is empty.
func onlyContent(srv *contentServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && !strings.Contains(r.URL.Query().Get("select"), "revision") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		srv.ServeHTTP(w, r)
	})
}

// pendingOf is the queued entry of a row, or nil.
func pendingOf(t *testing.T, st *store.Store, entity, id string) *store.OutboxEntry {
	t.Helper()
	entries, err := st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Entity == entity && e.EntityID == id {
			return &e
		}
	}
	return nil
}

// dequeueAll empties the outbox, as after a push the server took whole.
func dequeueAll(t *testing.T, st *store.Store) {
	t.Helper()
	entries, err := st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
}

// refusing is a server that refuses every insert and upsert, as row-level
// security does a row the user may not change, changes nothing on a
// PATCH, and shows rows by table for a GET. It records the requests.
type refusing struct {
	mu       gosync.Mutex
	rows     map[string]map[string]any
	patch    func(table string, r *http.Request) []map[string]any
	requests []string
}

func (s *refusing) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	table := strings.TrimPrefix(r.URL.Path, "/rest/v1/")
	out := []map[string]any{}
	switch r.Method {
	case "POST":
		if strings.HasPrefix(r.URL.Path, "/rest/v1/") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"42501","message":"new row violates row-level security policy"}`))
			return
		}
	case "PATCH":
		if s.patch != nil {
			out = s.patch(table, r)
		}
	case "GET":
		if row := s.rows[table]; row != nil {
			out = append(out, row)
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// oldFileCopy makes on st a copy of a stored file as versions before
// copies were made only from what is on the device did: its upload reads
// the original's stored contents later (from_path), and such entries
// still queued go up so. It returns the copy.
func oldFileCopy(t *testing.T, st *store.Store, pageID string, orig store.File) store.File {
	t.Helper()
	now := time.Now()
	c := store.File{ID: doc.NewRowID(), SpaceID: st.Session().SpaceID, PageID: pageID, Name: orig.Name, MimeType: orig.MimeType,
		CreatedAt: now, UpdatedAt: now}
	c.StoragePath = c.SpaceID + "/" + c.ID + "/" + c.Name
	if err := st.PutFile(c); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("files", c.ID, store.FileToRemote(c), "new file", pageID); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("storage", "files/"+c.ID, map[string]any{"bucket": "files", "path": c.StoragePath, "local_path": "",
		"content_type": orig.MimeType, "file_id": c.ID, "from_path": orig.StoragePath}, "file waiting to upload", pageID); err != nil {
		t.Fatal(err)
	}
	return c
}

// The passes, the app and uploads read the iCloud folder from goroutines
// of their own while the user picks another.
func TestICloudFolderMovesWhileRead(t *testing.T) {
	st := openStore(t)
	svc := New(st, remote.New(remote.Settings{}, st))
	svc.SetICloud(true)
	var wg gosync.WaitGroup
	wg.Go(func() {
		for range 200 {
			_ = svc.CanSync()
			_ = svc.CanFetch()
			_ = svc.joinedICloud()
		}
	})
	for i := range 200 {
		svc.Hold(func() { svc.SetICloudRoot(filepath.Join(t.TempDir(), strconv.Itoa(i))) })
	}
	wg.Wait()
}
