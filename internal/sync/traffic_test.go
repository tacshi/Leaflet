package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// recorder is a fake server that notes each request it has, as
// "METHOD /path?query", and answers it with answer, or [].
type recorder struct {
	mu       gosync.Mutex
	requests []string
	answer   func(r *http.Request) any
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.mu.Lock()
	rec.requests = append(rec.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	answer := rec.answer
	rec.mu.Unlock()
	var out any = []any{}
	if answer != nil {
		if a := answer(r); a != nil {
			out = a
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// asked are the requests had, of those whose line contains each of parts.
func (rec *recorder) asked(parts ...string) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, r := range rec.requests {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(r, p) }) {
			out = append(out, r)
		}
	}
	return out
}

func (rec *recorder) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = nil
}

// A page pushed over the revision this device merged reads only the
// revision, never the content, and the server answers the save with its
// revision and time, not the content sent.
func TestPushReadsContentOnlyWhenBehind(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, base)
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	srv := &contentServer{content: base.Encode(), revision: 1}
	var mu gosync.Mutex
	var reads, saves []string
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == "GET" {
			reads = append(reads, r.URL.Query().Get("select"))
		} else {
			saves = append(saves, r.URL.Query().Get("select"))
		}
		mu.Unlock()
		srv.ServeHTTP(w, r)
	})))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if done, err := svc.pushContent(context.Background(), p.ID); err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	mu.Lock()
	if !slices.Equal(reads, []string{"revision, updated_at"}) || !slices.Equal(saves, []string{"revision, updated_at"}) {
		t.Errorf("up to date: read %q, saved asking for %q", reads, saves)
	}
	reads, saves = nil, nil
	mu.Unlock()

	// Another device saved since: its content comes, and merges.
	theirs := doc.MustDecode(srv.content)
	theirs.Children = append(theirs.Children, para("b", "two, added there"))
	theirs.Blocks["b"] = time.Now().UnixMilli()
	srv.content, srv.revision = theirs.Encode(), srv.revision+1
	again := st.Content(p.ID)
	again.Find("a").SetDelta(doc.Delta{{Text: "one, edited again"}})
	if _, _, err := st.SaveContent(p.ID, again); err != nil {
		t.Fatal(err)
	}
	if done, err := svc.pushContent(context.Background(), p.ID); err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(reads, []string{"revision, updated_at", "content, revision, updated_at"}) {
		t.Errorf("behind: read %q", reads)
	}
	saved := doc.MustDecode(srv.content)
	if textOf(saved, "a") != "one, edited again" || saved.Find("b") == nil {
		t.Errorf("server has %s", srv.content)
	}
}

// The content this device pushed is not fetched again by the next pull,
// overlap or not: it has that version.
func TestPullPassesOverWhatThisDevicePushed(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	row, _ := st.ContentRow(p.ID)
	if err := st.PushedContent(p.ID, row.Content, row.Content, "", row.Content, at, 4); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{answer: func(r *http.Request) any {
		if r.URL.Path == "/rest/v1/page_content" && r.URL.Query().Get("select") == "page_id, updated_at" {
			return []map[string]any{{"page_id": p.ID, "updated_at": at.Format(time.RFC3339Nano)}}
		}
		return nil
	}}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	// The cursor is well before the row, which is no row of the overlap.
	svc.setCursor("page_content", cursor{at.Add(-time.Hour).Format(time.RFC3339Nano), ""})
	if err := svc.pullContents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rec.asked("/rest/v1/page_content", "content%2C"); len(got) != 0 {
		t.Errorf("fetched the content pushed: %v", got)
	}
	if c := svc.cursor("page_content"); c.key != p.ID {
		t.Errorf("cursor at %+v", c)
	}
}

// Content the server stamped again without a new revision, as a move of its
// page does, is fetched once: the passes after, which read the row again
// while it is in the overlap behind the cursor, pass over it.
func TestPullFetchesContentStampedAgainOnce(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	merged := time.Now().UTC().Add(-time.Hour)
	row, _ := st.ContentRow(p.ID)
	if err := st.PushedContent(p.ID, row.Content, row.Content, "", row.Content, merged, 4); err != nil {
		t.Fatal(err)
	}
	stamped := time.Now().UTC().Format(time.RFC3339Nano)
	rec := &recorder{answer: func(r *http.Request) any {
		if r.URL.Path != "/rest/v1/page_content" {
			return nil
		}
		if r.URL.Query().Get("select") == "page_id, updated_at" {
			return []map[string]any{{"page_id": p.ID, "updated_at": stamped}}
		}
		return contentRows(r, func(string) map[string]any {
			return map[string]any{"content": json.RawMessage(row.Content), "revision": 4, "updated_at": stamped}
		})
	}}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.setCursor("page_content", cursor{merged.Add(-time.Hour).Format(time.RFC3339Nano), ""})
	for range 3 {
		if err := svc.pullContents(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.asked("/rest/v1/page_content", "content%2C"); len(got) != 1 {
		t.Errorf("content fetched %d times over 3 passes, want once: %v", len(got), got)
	}
	if c := svc.cursor("page_content"); c.key != p.ID {
		t.Errorf("cursor at %+v", c)
	}
	if got, _ := st.ContentRow(p.ID); got.Content != row.Content || got.RemoteRevision != 4 {
		t.Errorf("content %q at revision %d", got.Content, got.RemoteRevision)
	}
}

// The content of many pages changed elsewhere comes a batch of pages at a
// time, not a request each; the pages that wait come after the rows the
// cursor passes, and one of them failing leaves the cursor past those.
func TestPullFetchesContentInBatches(t *testing.T) {
	was := contentBatch
	contentBatch = 2
	t.Cleanup(func() { contentBatch = was })
	st := openStore(t)
	var ids []string
	for i := range 4 {
		p, err := st.CreatePage(store.NewPageOptions{Title: fmt.Sprint("P", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	late, err := st.CreatePage(store.NewPageOptions{Title: "Late"})
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	theirs := &doc.Content{Children: []*doc.Node{para("a", "there")}, Blocks: map[string]int64{"a": time.Now().UnixMilli() + 1000}, Deleted: map[string]int64{}}
	rec := &recorder{answer: func(r *http.Request) any {
		if r.URL.Path != "/rest/v1/page_content" {
			return nil
		}
		if !strings.Contains(r.URL.Query().Get("select"), "content") {
			var out []map[string]any
			for _, id := range ids {
				out = append(out, map[string]any{"page_id": id, "updated_at": at})
			}
			return out
		}
		return contentRows(r, func(string) map[string]any {
			return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at}
		})
	}}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(askedPages(r), late.ID) {
			// The page that waits fails to come.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message": "not now"}`))
			return
		}
		rec.ServeHTTP(w, r)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.setWaiting(map[string]bool{late.ID: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.pullContents(context.Background()); err == nil {
		t.Error("the page that waits did not fail")
	}
	for _, id := range ids {
		if c := st.Content(id); textOf(c, "a") != "there" {
			t.Errorf("%s has %v", id, c)
		}
	}
	if c := svc.cursor("page_content"); c.key != ids[len(ids)-1] {
		t.Errorf("cursor at %+v", c)
	}
	if got := rec.asked("/rest/v1/page_content", "content%2C"); len(got) != 2 {
		t.Errorf("%d requests for the changed pages' content, want 2 batches: %v", len(got), got)
	}
	if w := waitingOn(t, svc); !w[late.ID] {
		t.Errorf("waiting for %v", w)
	}
}

// A pass that finds the pages waiting as they were does not write them
// again, nor tell the app key_values changed.
func TestWaitingIsWrittenOnlyAsItChanges(t *testing.T) {
	st := openStore(t)
	svc, _ := fullPass(t, st, nil)
	if err := svc.setWaiting(map[string]bool{"p1": true}); err != nil {
		t.Fatal(err)
	}
	told := 0
	st.Subscribe(func(c store.Change) {
		if c.Table == "key_values" {
			told++
		}
	})
	for range 2 {
		if err := svc.setWaiting(map[string]bool{"p1": true}); err != nil {
			t.Fatal(err)
		}
		if err := svc.setWaiting(waitingOn(t, svc)); err != nil {
			t.Fatal(err)
		}
	}
	if told != 0 {
		t.Errorf("told of key_values %d times", told)
	}
	if err := svc.setWaiting(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if told != 1 || len(waitingOn(t, svc)) != 0 {
		t.Errorf("told %d times, waiting %v", told, waitingOn(t, svc))
	}
}

// An idle pass asks the server for what changed in each table it pulls,
// but says the device is in use, and lists the model connections removed
// elsewhere, only every so often: once after Start.
func TestIdlePassesAskLess(t *testing.T) {
	st := openStore(t)
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	for range 3 {
		if err := svc.SyncNow(); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.asked("/rest/v1/devices"); len(got) != 1 || !strings.HasPrefix(got[0], "POST") || !strings.Contains(got[0], "select=revoked_at") {
		t.Errorf("heartbeats %v, want one upsert", got)
	}
	if got := slices.DeleteFunc(rec.asked("GET /rest/v1/model_providers?"), func(r string) bool {
		return r != "GET /rest/v1/model_providers?select=id"
	}); len(got) != 1 {
		t.Errorf("listed the model connections %d times", len(got))
	}
	// A loop started again says so again at once.
	svc.mu.Lock()
	svc.beat, svc.providersSwept = every{}, every{}
	svc.mu.Unlock()
	rec.reset()
	if err := svc.SyncNow(); err != nil {
		t.Fatal(err)
	}
	if got := rec.asked("/rest/v1/devices"); len(got) != 1 {
		t.Errorf("heartbeats after a start %v", got)
	}
}

// A pass asked for after a save sends it and pulls nothing; a Kick, as
// for what another device did, pulls too.
func TestAPassForASaveOnlyPushes(t *testing.T) {
	st := openStore(t)
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Plan"}); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.syncNow(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if got := rec.asked("POST /rest/v1/pages"); len(got) == 0 {
		t.Error("the page did not go up")
	}
	if got := rec.asked("GET /rest/v1/comments"); len(got) != 0 {
		t.Errorf("pulled %v", got)
	}
	svc.KickPush()
	if svc.wantPull.Load() {
		t.Error("a pass for a save asks to pull")
	}
	svc.Kick()
	if !svc.wantPull.Load() {
		t.Error("a kick does not ask to pull")
	}
}

// A page opened and closed again before the pass in progress is done is
// not brought up to date for nothing, holding the passes after it.
func TestAPageClosedMeanwhileIsNotRefreshed(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	svc.running.Lock()
	svc.SetOpen(p.ID, true)
	svc.SetOpen(p.ID, false)
	svc.running.Unlock()
	// Let the goroutine take the lock and go.
	time.Sleep(200 * time.Millisecond)
	svc.running.Lock()
	defer svc.running.Unlock()
	if got := rec.asked("/rest/v1/"); len(got) != 0 {
		t.Errorf("asked %v", got)
	}
}

// A snapshot's entry names it alone: its content is read as it goes up,
// once, from page_history. One an earlier version queued whole still goes
// as it is, and one pruned meanwhile is done.
func TestHistoryGoesUpFromItsSnapshot(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	if err := st.AddHistory(p.ID, doc.FromBlocks([]*doc.Node{para("a", "the snapshot")}), "manual", "", "v1"); err != nil {
		t.Fatal(err)
	}
	entries, err := st.Pending()
	if err != nil || len(entries) != 1 || entries[0].Entity != "page_history" || strings.Contains(entries[0].Payload, "the snapshot") {
		t.Fatalf("queued %+v, %v", entries, err)
	}
	// One pruned before it went has nothing to send.
	if err := st.AddHistory(p.ID, doc.Empty(), "autosave", "", "Autosave"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM page_history WHERE reason = 'autosave'`); err != nil {
		t.Fatal(err)
	}
	old := map[string]any{"id": doc.NewRowID(), "page_id": p.ID, "content": json.RawMessage(`{"document":{"children":[]},"meta":{"v":1}}`), "reason": "manual"}
	if err := st.Queue("page_history", store.S(old, "id"), old, "page history", p.ID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	var bodies []map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/v1/page_history" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("sent %v", bodies)
	}
	raw, _ := json.Marshal(bodies[0]["content"])
	if c := doc.MustDecode(string(raw)); textOf(c, "a") != "the snapshot" || bodies[0]["created_by"] != "u1" || bodies[0]["page_id"] != p.ID {
		t.Errorf("sent %v", bodies[0])
	}
	if bodies[1]["id"] != old["id"] || bodies[1]["content"] == nil {
		t.Errorf("sent %v", bodies[1])
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d left", n)
	}
}

// Pages that wait for good, as with content this version cannot read, do
// not keep the pages after them from coming, more of them than a pass
// brings: each pass goes on from where the last stopped, and has the next
// run at once only when it brought some.
func TestPagesThatStayWaitingDoNotHoldUpTheOthers(t *testing.T) {
	was := waitingBatch
	waitingBatch = 3
	t.Cleanup(func() { waitingBatch = was })
	st := openStore(t)
	waiting := map[string]bool{}
	for i := range 4 {
		p, err := st.CreatePage(store.NewPageOptions{Title: fmt.Sprint("P", i)})
		if err != nil {
			t.Fatal(err)
		}
		waiting[p.ID] = true
	}
	dequeueAll(t, st)
	ids := slices.Sorted(maps.Keys(waiting))
	last := ids[len(ids)-1]
	at := time.Now().UTC().Format(time.RFC3339Nano)
	theirs := &doc.Content{Children: []*doc.Node{para("a", "there")}, Blocks: map[string]int64{"a": time.Now().UnixMilli() + 1000}, Deleted: map[string]int64{}}
	rec := &recorder{answer: func(r *http.Request) any {
		if r.URL.Path != "/rest/v1/page_content" {
			return nil
		}
		return contentRows(r, func(id string) map[string]any {
			if id == last {
				return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at}
			}
			return map[string]any{"content": json.RawMessage(`"from a newer Leaflet"`), "revision": 1, "updated_at": at}
		})
	}}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.setWaiting(waiting); err != nil {
		t.Fatal(err)
	}
	var kicked []bool
	for range 3 {
		if err := svc.pullWaiting(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-svc.kick:
			kicked = append(kicked, true)
		default:
			kicked = append(kicked, false)
		}
	}
	if c := st.Content(last); c == nil || textOf(c, "a") != "there" {
		t.Errorf("the page after those that stay waiting has %v", c)
	}
	if w := waitingOn(t, svc); len(w) != 3 || w[last] {
		t.Errorf("waiting for %v", w)
	}
	if !slices.Equal(kicked, []bool{false, true, false}) {
		t.Errorf("asked for a pass at once after each: %v", kicked)
	}
}

// A session begun, as the user signing in again, says the device is in use
// at once, so a device revoked while its last session ended on its own
// learns it as the new one starts, and forgets its ID, to register anew
// as the user signs in again.
func TestASessionBegunAsksWhetherTheDeviceIsRevoked(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("space.u1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	revoked := false
	rec := &recorder{answer: func(r *http.Request) any {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/rest/v1/devices" && revoked {
			return []map[string]any{{"revoked_at": "2026-01-01T00:00:00Z"}}
		}
		return nil
	}}
	hs := httptest.NewServer(rec)
	defer hs.Close()
	be := remote.SignedInForTest(hs.URL, st, "u1")
	svc := New(st, be)
	for range 2 {
		if err := svc.SyncNow(); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.asked("/rest/v1/devices"); len(got) != 1 {
		t.Fatalf("heartbeats %v", got)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	was := st.DeviceID()
	be.SignInForTest("u1")
	if err := svc.SyncNow(); !errors.Is(err, remote.ErrSignedOut) {
		t.Errorf("pass after signing in again: %v", err)
	}
	if got := rec.asked("/rest/v1/devices"); len(got) != 2 {
		t.Errorf("heartbeats %v", got)
	}
	if st.DeviceID() == was {
		t.Error("the device kept its revoked ID")
	}
}
