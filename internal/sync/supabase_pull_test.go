package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

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

// PulledAt has no time before a table's first pull, the time of the newest
// row after, as the server writes it, and none for a cursor it cannot read.
func TestPulledAt(t *testing.T) {
	st := openStore(t)
	if at, ok := PulledAt(st, "pages"); ok {
		t.Errorf("pulled at %v before any pull", at)
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "p1", "updated_at": "2026-10-26T09:00:05.123456+00:00"}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.pullTable(context.Background(), "pages", "*", func(map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if at, ok := PulledAt(st, "pages"); !ok || !at.Equal(time.Date(2026, 10, 26, 9, 0, 5, 123456000, time.UTC)) {
		t.Errorf("pulled at %v, %v", at, ok)
	}
	if err := st.SetValue(cursorKey("pages"), "soon|p1"); err != nil {
		t.Fatal(err)
	}
	if at, ok := PulledAt(st, "pages"); ok {
		t.Errorf("pulled at %v by a cursor that does not read", at)
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
	rows, _, err := svc.changed(context.Background(), "pages", "id", "*", cursor{at, "b"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(asked) != 2 || !strings.Contains(asked[1], "or=") {
		t.Errorf("rows %v, asked %v", rows, asked)
	}
}

// A cloud model connection deleted on another device, which the server
// removes outright, goes here too; one kept on this device alone stays.
func TestPullRemovesDeletedProviders(t *testing.T) {
	st := openStore(t)
	for _, p := range []store.Provider{
		{ID: "kept", Preset: "openai", Name: "Kept", BaseURL: "https://example.com", AuthKind: "api_key"},
		{ID: "gone", Preset: "openai", Name: "Gone", BaseURL: "https://example.com", AuthKind: "api_key"},
		{ID: "local", Preset: "ollama", Name: "Local", BaseURL: "http://localhost:11434", AuthKind: "none", IsLocal: true},
	} {
		if err := st.PutProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if r.URL.Path == "/rest/v1/model_providers" && r.URL.Query().Get("select") == "id" {
			out = append(out, map[string]any{"id": "kept"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.pullProviderRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left []string
	providers, _ := st.Providers()
	for _, p := range providers {
		left = append(left, p.ID)
	}
	slices.Sort(left)
	if strings.Join(left, ",") != "kept,local" {
		t.Errorf("left %v", left)
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

// A first sync of many rows reads them all in one pass, a page at a time,
// rather than a page each pass.
func TestPullReadsEveryPageOfChanges(t *testing.T) {
	st := openStore(t)
	pullRows = 2
	t.Cleanup(func() { pullRows = 500 })
	const space = "00000000-0000-0000-0000-0000000000ff"
	var rows []map[string]any
	for i := range 5 {
		at := time.Date(2026, 10, 1, 9, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		rows = append(rows, map[string]any{"id": fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i), "space_id": space,
			"title": strconv.Itoa(i), "sort_key": "V", "created_at": at, "updated_at": at})
	}
	calls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from := min(calls*2, len(rows))
		calls++
		_ = json.NewEncoder(w).Encode(rows[from:min(from+2, len(rows))])
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.pullTable(context.Background(), "pages", "*", st.PutRemotePage); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := st.Page(store.S(r, "id")); err != nil {
			t.Errorf("page %s: %v", store.S(r, "title"), err)
		}
	}
}
