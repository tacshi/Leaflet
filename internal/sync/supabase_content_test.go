package sync

import (
	"context"
	"encoding/json"
	"errors"
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

func TestSaveMergesWithASaveInBetween(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, base)
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

func TestRefreshKeepsASaveMadeWhileMerging(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one"), para("b", "two")},
		Blocks: map[string]int64{"a": 1000, "b": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, p.ID, base)
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
	putSynced(t, st, p.ID, base)
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
	putSynced(t, st, p.ID, base)
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

// conflictedPage is a page edited here, on block a, since it synced at
// revision 1, and a server that has a later edit of the same block at
// revision 2: merging them is a conflict, which the server's edit wins.
func conflictedPage(t *testing.T, st *store.Store) (store.Page, *contentServer) {
	t.Helper()
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
	theirs := base.Copy()
	theirs.Find("a").SetDelta(doc.Delta{{Text: "one, edited there"}})
	theirs.Blocks["a"] = time.Now().UnixMilli() + 60_000
	return p, &contentServer{content: theirs.Encode(), revision: 2}
}

// refuseConflicts has the database fail to record a conflict, as a full
// disk would, until the returned func lets it again.
func refuseConflicts(t *testing.T, st *store.Store) func() {
	t.Helper()
	if _, err := st.DB().Exec(`CREATE TRIGGER refuse BEFORE INSERT ON conflicts BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
			t.Fatal(err)
		}
	}
}

// localSides are the sides this device lost of the conflicts on a block.
func localSides(t *testing.T, st *store.Store, blockID string) []string {
	t.Helper()
	conflicts, err := st.Conflicts()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range conflicts {
		if c.BlockID == blockID && c.LocalNode != nil {
			out = append(out, c.LocalNode.Delta().Plain())
		}
	}
	return out
}

// A merge whose conflict cannot be recorded is not stored as merged: the
// revision stays unmerged and the change queued, so the next push merges
// again and records the side its edit lost.
func TestPushStoresNoMergeWithoutItsConflicts(t *testing.T) {
	st := openStore(t)
	p, srv := conflictedPage(t, st)
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	allow := refuseConflicts(t, st)
	if done, err := svc.pushContent(context.Background(), p.ID); err == nil || done {
		t.Fatalf("push with its conflict refused: done %v, %v", done, err)
	}
	if row, _ := st.ContentRow(p.ID); row.RemoteRevision != 1 {
		t.Errorf("device has revision %d stored, though its conflict was not", row.RemoteRevision)
	}
	if !st.HasPending("page_content", p.ID) {
		t.Error("the change is no longer queued")
	}
	allow()
	if done, err := svc.pushContent(context.Background(), p.ID); err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	if got := localSides(t, st, "a"); !slices.Contains(got, "one, edited here") {
		t.Errorf("conflicts on a keep %q", got)
	}
}

// A merge the server takes keeps its conflicts though this device then
// fails to store it: the next push merges over the server's copy, which
// has this device's side already, and would never meet the side the
// server lost.
func TestPushKeepsConflictsTheServerTookOver(t *testing.T) {
	st := openStore(t)
	p, srv := conflictedPage(t, st)
	// The server's edit is the older here, so this device's is kept and
	// the server's is the side lost.
	theirs := doc.MustDecode(srv.content)
	theirs.Blocks["a"] = 1500
	srv.content = theirs.Encode()
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if _, err := st.DB().Exec(`CREATE TRIGGER refuse BEFORE UPDATE ON page_contents BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if done, err := svc.pushContent(context.Background(), p.ID); err == nil || done {
		t.Fatalf("push stored with the store failing: done %v, %v", done, err)
	}
	if srv.updates != 1 {
		t.Fatalf("server took %d updates, want 1", srv.updates)
	}
	if _, err := st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if done, err := svc.pushContent(context.Background(), p.ID); err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	conflicts, err := st.Conflicts()
	if err != nil {
		t.Fatal(err)
	}
	var lost []string
	for _, c := range conflicts {
		if c.BlockID == "a" && c.RemoteNode != nil {
			lost = append(lost, c.RemoteNode.Delta().Plain())
		}
	}
	if !slices.Equal(lost, []string{"one, edited there"}) {
		t.Errorf("server's sides kept %q, want the one it lost, once", lost)
	}
}

// A merge the server refuses, as another device saved first, takes its
// conflicts back: the merge over that save meets its own.
func TestStalePushTakesItsConflictsBack(t *testing.T) {
	st := openStore(t)
	p, srv := conflictedPage(t, st)
	srv.between = func(s *contentServer) { s.revision++ }
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if _, err := svc.saveContent(context.Background(), p.ID); !errors.Is(err, errStale) {
		t.Fatalf("save over a newer save: %v, want errStale", err)
	}
	if conflicts, err := st.Conflicts(); err != nil || len(conflicts) != 0 {
		t.Errorf("conflicts of a merge the server refused: %d, %v", len(conflicts), err)
	}
	// Merged again over that save, the conflict is the push's, once.
	if done, err := svc.pushContent(context.Background(), p.ID); err != nil || !done {
		t.Fatalf("push: done %v, %v", done, err)
	}
	if got := localSides(t, st, "a"); !slices.Equal(got, []string{"one, edited here"}) {
		t.Errorf("conflicts on a keep %q", got)
	}
}

// A pulled revision is stored with the conflicts of its merge or not at
// all: stored without them, the page would no longer be behind, and the
// merge that meets them would never run again.
func TestPullStoresNoRevisionWithoutItsConflicts(t *testing.T) {
	st := openStore(t)
	p, srv := conflictedPage(t, st)
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	allow := refuseConflicts(t, st)
	if _, err := svc.RefreshContent(context.Background(), p.ID); err == nil {
		t.Fatal("the pull stored a merge whose conflict it could not record")
	}
	row, _ := st.ContentRow(p.ID)
	if row.RemoteRevision != 1 || textOf(doc.MustDecode(row.Content), "a") != "one, edited here" {
		t.Errorf("device has revision %d, %q", row.RemoteRevision, textOf(doc.MustDecode(row.Content), "a"))
	}
	allow()
	if _, err := svc.RefreshContent(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if got := localSides(t, st, "a"); !slices.Equal(got, []string{"one, edited here"}) {
		t.Errorf("conflicts on a keep %q", got)
	}
	if row, _ := st.ContentRow(p.ID); row.RemoteRevision != 2 {
		t.Errorf("device has revision %d, want 2", row.RemoteRevision)
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

// keepContent has svc keep pageIDs' content, as KeepContent does, and
// returns once they wait for a pass, which it asks for.
func keepContent(t *testing.T, svc *Service, pageIDs ...string) {
	t.Helper()
	select {
	case <-svc.kick:
	default:
	}
	svc.KeepContent(pageIDs...)
	select {
	case <-svc.kick:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass asked for")
	}
}

// A page marked to keep on this device, and every page once all are kept,
// brings the content that changed on the server while it was not kept,
// which the pulls passed over; a page still not kept does not come.
func TestKeptPagesBringWhatChangedMeanwhile(t *testing.T) {
	st := openStore(t)
	settings := st.Settings()
	settings.AllPagesOffline = false
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		here := &doc.Content{Children: []*doc.Node{para("a", "here")}}
		p, err := st.CreatePage(store.NewPageOptions{Title: "Page", Content: here})
		if err != nil {
			t.Fatal(err)
		}
		putSynced(t, st, p.ID, st.Content(p.ID))
		if err := st.SetKeepOffline(p.ID, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	dequeueAll(t, st)
	theirs := &doc.Content{Children: []*doc.Node{para("a", "there")}, Blocks: map[string]int64{"a": time.Now().UnixMilli() + 1000}, Deleted: map[string]int64{}}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	lists := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if strings.Contains(r.URL.Query().Get("select"), "content") {
			out = contentRows(r, func(string) map[string]any {
				return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 2, "updated_at": at}
			})
		} else if lists++; lists == 1 {
			for _, id := range ids {
				out = append(out, map[string]any{"page_id": id, "updated_at": at})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	texts := func() []string {
		var out []string
		for _, id := range ids {
			out = append(out, textOf(st.Content(id), "a"))
		}
		return out
	}

	ctx := context.Background()
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(); !slices.Equal(got, []string{"here", "here", "here"}) {
		t.Fatalf("pages not kept have %v", got)
	}
	if err := st.SetKeepOffline(ids[0], true); err != nil {
		t.Fatal(err)
	}
	keepContent(t, svc, ids[0])
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(); !slices.Equal(got, []string{"there", "here", "here"}) {
		t.Errorf("with the first kept, %v", got)
	}
	settings.AllPagesOffline = true
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	keepContent(t, svc)
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(); !slices.Equal(got, []string{"there", "there", "there"}) {
		t.Errorf("with all kept, %v", got)
	}
	if len(svc.waiting()) != 0 {
		t.Errorf("still waiting for %v", svc.waiting())
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
			out = contentRows(r, func(string) map[string]any {
				return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at}
			})
		} else if lists++; lists == 1 {
			out = append(out, map[string]any{"page_id": id, "updated_at": at})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

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
	putSynced(t, st, p.ID, base)
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
	svc, requests := fullPass(t, st, onlyContent(srv))

	// A whole pass: the page waits, and the rest of it goes on.
	if err := svc.pass(context.Background(), true); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if srv.updates != 0 || srv.content != base.Encode() {
		t.Errorf("server saved over: %d updates, %s", srv.updates, srv.content)
	}
	if e := pendingOf(t, st, "page_content", p.ID); e == nil || e.Attempts != 1 || !strings.Contains(e.LastError, errUnreadable.Error()) {
		t.Errorf("queued %+v", e)
	}
	if !slices.Contains(*requests, "GET /rest/v1/comments") {
		t.Errorf("the pass stopped before the pulls: %v", *requests)
	}

	// The server moves on: the change waiting here is not merged either.
	theirs := base.Copy()
	theirs.Children = append(theirs.Children, para("b", "two, added there"))
	theirs.Blocks["b"] = time.Now().UnixMilli()
	srv.content, srv.revision = theirs.Encode(), 2
	if _, err := svc.RefreshContent(context.Background(), p.ID); !errors.Is(err, errUnreadable) {
		t.Errorf("refresh: %v", err)
	}
	if row, _ := st.ContentRow(p.ID); row.Content != broken || !st.HasPending("page_content", p.ID) {
		t.Errorf("content %q, pending %v", row.Content, st.HasPending("page_content", p.ID))
	}

	// A pull leaves the page waiting until it can be read, and the other
	// pages' content still comes.
	at := time.Now().UTC().Format(time.RFC3339Nano)
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	hs2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if strings.Contains(r.URL.Query().Get("select"), "content") {
			out = contentRows(r, func(string) map[string]any {
				return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 2, "updated_at": at}
			})
		} else {
			out = append(out, map[string]any{"page_id": p.ID, "updated_at": at}, map[string]any{"page_id": other.ID, "updated_at": at})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs2.Close()
	svc, _ = fullPass(t, st, hs2.Config.Handler)
	if err := svc.pass(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.ContentRow(p.ID); row.Content != broken {
		t.Errorf("content %q", row.Content)
	}
	if c := st.Content(other.ID); c == nil || textOf(c, "b") != "two, added there" {
		t.Errorf("the other page has %v", c)
	}
	if !svc.waiting()[p.ID] {
		t.Errorf("waiting for %v", svc.waiting())
	}
}

// Content listed for a page whose row this device fails to read, as with
// the database failing, stops the pull before it, for the next to go on
// from: only a page not here yet waits for its row.
func TestPullStopsAtAPageItCannotRead(t *testing.T) {
	st := openStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"page_id": p.ID, "updated_at": at}})
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if _, err := st.DB().Exec(`ALTER TABLE pages RENAME TO pages_away`); err != nil {
		t.Fatal(err)
	}
	if err := svc.pullContents(context.Background()); err == nil {
		t.Error("the pull went on past the page")
	}
	if v, ok := st.Value("sync.cursor.page_content"); ok {
		t.Errorf("the cursor passed the page: %s", v)
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
	putSynced(t, st, p.ID, base)
	mine := st.Content(p.ID)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(p.ID, mine); err != nil {
		t.Fatal(err)
	}
	srv := &contentServer{content: newerKind, revision: 2}
	svc, requests := fullPass(t, st, onlyContent(srv))
	ctx := context.Background()

	if err := svc.pass(ctx, true); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if srv.updates != 0 || srv.content != newerKind {
		t.Errorf("server saved over: %d updates, %s", srv.updates, srv.content)
	}
	if e := pendingOf(t, st, "page_content", p.ID); e == nil || e.Attempts != 1 || !strings.Contains(e.LastError, errServerUnreadable.Error()) {
		t.Errorf("queued %+v", e)
	}
	if !slices.Contains(*requests, "GET /rest/v1/comments") {
		t.Errorf("the pass stopped before the pulls: %v", *requests)
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
		if !strings.Contains(r.URL.Query().Get("select"), "content") {
			out = append(out, map[string]any{"page_id": p.ID, "updated_at": at}, map[string]any{"page_id": other.ID, "updated_at": at})
		} else {
			out = contentRows(r, func(id string) map[string]any {
				if id == p.ID {
					return map[string]any{"content": json.RawMessage(newerKind), "revision": 2, "updated_at": at}
				}
				return map[string]any{"content": json.RawMessage(theirs.Encode()), "revision": 1, "updated_at": at}
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs2.Close()
	svc, _ = fullPass(t, st, hs2.Config.Handler)
	if err := svc.pass(ctx, true); err != nil {
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

// Content from the server that is not a page's, as null, is refused rather
// than taken for an empty page in place of the one here.
func TestRefreshRefusesContentThatIsNotAPage(t *testing.T) {
	st := openStore(t)
	base := doc.FromBlocks([]*doc.Node{para("a", "important text")})
	p, err := st.CreatePage(store.NewPageOptions{Content: base})
	if err != nil {
		t.Fatal(err)
	}
	putSynced(t, st, p.ID, base)
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

// Content the server refuses to save, as the user may only view the page
// now, gives way to the server's: else it would go, and fail, on every
// pass, and the pulls would pass over the page for good.
func TestRefusedContentTakesTheServers(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	const (
		shared = "00000000-0000-0000-0000-0000000000e1"
		space  = "00000000-0000-0000-0000-0000000000ff"
	)
	old := "2026-01-01T00:00:00Z"
	if err := st.PutRemotePage(map[string]any{"id": shared, "space_id": space, "title": "Shared", "sort_key": "V", "created_at": old, "updated_at": old}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(shared, []store.Member{{PageID: shared, UserID: "u1", Role: "editor", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	base := &doc.Content{Children: []*doc.Node{para("a", "one")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
	putSynced(t, st, shared, base)
	mine := st.Content(shared)
	mine.Find("a").SetDelta(doc.Delta{{Text: "one, edited here"}})
	if _, _, err := st.SaveContent(shared, mine); err != nil {
		t.Fatal(err)
	}
	theirs := base.Copy()
	theirs.Find("a").SetDelta(doc.Delta{{Text: "one, edited by the owner"}})
	theirs.Blocks["a"] = 2000
	var mu gosync.Mutex
	patches := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var out any = []map[string]any{}
		switch {
		case r.URL.Path == "/rest/v1/rpc/page_members":
			out = []map[string]any{{"user_id": "u2", "role": "owner", "accepted": true}, {"user_id": "u1", "role": "viewer", "accepted": true}}
		case r.URL.Path == "/rest/v1/page_content" && r.Method == "PATCH":
			patches++ // row-level security matches no row
		case r.URL.Path == "/rest/v1/page_content":
			out = []map[string]any{{"content": json.RawMessage(theirs.Encode()), "revision": 3, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.HasPending("page_content", shared) {
		t.Error("the refused change still waits")
	}
	if got := textOf(st.Content(shared), "a"); got != "one, edited by the owner" {
		t.Errorf("content %q", got)
	}
	if row, _ := st.ContentRow(shared); row.RemoteRevision != 3 {
		t.Errorf("revision %d", row.RemoteRevision)
	}
	if patches != 3 {
		t.Errorf("%d saves tried", patches)
	}
}

// Merged content the server took is stored here before its change leaves
// the queue: one that cannot be stored goes again.
func TestSaveKeepsTheChangeQueuedWhenItCannotStore(t *testing.T) {
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
	// The row cannot be read once the server has saved, as a database that
	// is busy may not be.
	srv := &contentServer{content: base.Encode(), revision: 1, between: func(*contentServer) {
		if _, err := st.DB().Exec(`UPDATE page_contents SET updated_at = 'busy' WHERE page_id = ?`, p.ID); err != nil {
			t.Error(err)
		}
	}}
	hs := httptest.NewServer(http.StripPrefix("/rest/v1/page_content", srv))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if done, err := svc.pushContent(context.Background(), p.ID); done || err == nil {
		t.Errorf("push: done %v, %v", done, err)
	}
}

// Pages whose content together takes longer than a request may come in
// fewer at a time, and the next pass starts from there: asked for the same
// way, they would never come.
func TestContentTooSlowToComeTogetherComesApart(t *testing.T) {
	old := contentBatch
	contentBatch = 4
	defer func() { contentBatch = old }()
	st := openStore(t)
	var ids []string
	for range 6 {
		p, err := st.CreatePage(store.NewPageOptions{Title: "Heavy"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	dequeueAll(t, st)
	var mu gosync.Mutex
	var asked []int
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Query().Get("page_id"), "in."), "()"), ",")
		mu.Lock()
		asked = append(asked, len(list))
		mu.Unlock()
		if len(list) > 1 {
			time.Sleep(300 * time.Millisecond)
		}
		out := []map[string]any{}
		for _, id := range list {
			c := &doc.Content{Children: []*doc.Node{para("a", "from the server")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
			out = append(out, map[string]any{"page_id": strings.Trim(id, `"`), "content": json.RawMessage(c.Encode()), "revision": 1,
				"updated_at": "2026-10-26T09:00:00Z"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	be := remote.SignedInForTest(hs.URL, st, "u1")
	be.TimeoutForTest(100 * time.Millisecond)
	svc := New(st, be)
	waiting := map[string]bool{}
	done, err := svc.fetchContents(context.Background(), ids[:3], waiting)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 3 || len(waiting) != 0 {
		t.Errorf("%d done, %d waiting", len(done), len(waiting))
	}
	for _, id := range ids[:3] {
		if c := st.Content(id); c == nil || textOf(c, "a") != "from the server" {
			t.Errorf("%s: %v", id, c)
		}
	}
	mu.Lock()
	first := len(asked)
	mu.Unlock()
	if first < 3 {
		t.Fatalf("asked for %d times", first)
	}
	if _, err := svc.fetchContents(context.Background(), ids[3:5], waiting); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked[first] != 1 {
		t.Errorf("the next pass began with %d pages at a time", asked[first])
	}
}

// slowContent is page_content on a server that gives up, as answer says, on
// a request for more pages than it can take, and counts how many each
// asked for. It answers a light read of spaces.
type slowContent struct {
	mu    gosync.Mutex
	asked []int
	take  int
	fail  func(w http.ResponseWriter)
}

func (s *slowContent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/rest/v1/spaces" {
		_, _ = w.Write([]byte("[]"))
		return
	}
	list := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Query().Get("page_id"), "in."), "()"), ",")
	s.mu.Lock()
	s.asked = append(s.asked, len(list))
	s.mu.Unlock()
	if len(list) > s.take {
		s.fail(w)
		return
	}
	out := []map[string]any{}
	for _, id := range list {
		c := &doc.Content{Children: []*doc.Node{para("a", "from the server")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
		out = append(out, map[string]any{"page_id": strings.Trim(id, `"`), "content": json.RawMessage(c.Encode()), "revision": 1,
			"updated_at": "2026-10-26T09:00:00Z"})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// heavyPages makes pages, none of them waiting to go up.
func heavyPages(t *testing.T, st *store.Store, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		p, err := st.CreatePage(store.NewPageOptions{Title: "Heavy"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	dequeueAll(t, st)
	return ids
}

// Pages whose content is too much for a request come apart however the
// request ended: the database out of time, or a gateway giving up.
func TestContentComesApartWhoeverGaveUp(t *testing.T) {
	old := contentBatch
	contentBatch = 4
	defer func() { contentBatch = old }()
	for name, fail := range map[string]func(w http.ResponseWriter){
		"statement timeout": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"57014","message":"canceling statement due to statement timeout"}`))
		},
		"gateway": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"message":"gateway timeout"}`))
		},
		"cloudflare": func(w http.ResponseWriter) {
			w.WriteHeader(524)
			_, _ = w.Write([]byte(`<html>A timeout occurred</html>`))
		},
	} {
		st := openStore(t)
		ids := heavyPages(t, st, 3)
		srv := &slowContent{take: 1, fail: fail}
		hs := httptest.NewServer(srv)
		svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
		waiting := map[string]bool{}
		done, err := svc.fetchContents(context.Background(), ids, waiting)
		hs.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(done) != 3 || len(waiting) != 0 || svc.contentsAt != 1 {
			t.Errorf("%s: %d done, %d waiting, %d at a time, asked %v", name, len(done), len(waiting), svc.contentsAt, srv.asked)
		}
		for _, id := range ids {
			if c := st.Content(id); c == nil || textOf(c, "a") != "from the server" {
				t.Errorf("%s: %s did not come", name, id)
			}
		}
	}
}

// A page too heavy alone waits, and the others come: left to stop the pull,
// it would hold back every page after it for good.
func TestAPageTooHeavyAloneWaits(t *testing.T) {
	st := openStore(t)
	ids := heavyPages(t, st, 3)
	heavy := ids[1]
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/v1/spaces":
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(r.URL.Query().Get("page_id"), heavy):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"57014","message":"canceling statement due to statement timeout"}`))
		default:
			c := &doc.Content{Children: []*doc.Node{para("a", "from the server")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
			id := strings.Trim(strings.TrimPrefix(r.URL.Query().Get("page_id"), "in."), `()"`)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"page_id": id, "content": json.RawMessage(c.Encode()), "revision": 1, "updated_at": "2026-10-26T09:00:00Z"}})
		}
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	waiting := map[string]bool{}
	done, err := svc.fetchContents(context.Background(), ids, waiting)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 3 || !waiting[heavy] || len(waiting) != 1 {
		t.Errorf("%d done, waiting %v", len(done), waiting)
	}
	if c := st.Content(ids[2]); c == nil || textOf(c, "a") != "from the server" {
		t.Error("the page after it did not come")
	}
}

// A database timing out on every statement stops the pull, with what it
// asked for waiting: no page is passed over for a server in trouble.
func TestContentStopsForADatabaseInTrouble(t *testing.T) {
	st := openStore(t)
	ids := heavyPages(t, st, 3)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"57014","message":"canceling statement due to statement timeout"}`))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	done, err := svc.fetchContents(context.Background(), ids, map[string]bool{})
	if !remote.IsStatementTimeout(err) || len(done) != 0 {
		t.Errorf("%d done, %v", len(done), err)
	}
}

// The size a slow request left is kept through pulls that fetched nothing,
// which tell nothing of it, and grows at most once a pull, by those that
// fetched and took no such wait.
func TestContentSizeGrowsOncePerPullThatFetched(t *testing.T) {
	old := contentBatch
	contentBatch = 8
	defer func() { contentBatch = old }()
	st := openStore(t)
	const at = "2026-10-26T09:00:00Z"
	ids := heavyPages(t, st, 2)
	var mu gosync.Mutex
	var rows []map[string]any
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Query().Get("select"), "content") {
			c := &doc.Content{Children: []*doc.Node{para("a", "from the server")}, Blocks: map[string]int64{"a": 1000}, Deleted: map[string]int64{}}
			out := []map[string]any{}
			for _, id := range ids {
				if strings.Contains(r.URL.Query().Get("page_id"), id) {
					out = append(out, map[string]any{"page_id": id, "content": json.RawMessage(c.Encode()), "revision": 1, "updated_at": at})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	svc.contentsAt = 1
	for range 3 {
		if err := svc.pullContents(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if svc.contentsAt != 1 {
		t.Fatalf("pulls that fetched nothing left %d at a time", svc.contentsAt)
	}
	// One pull fetches twice, the content changed and the pages that wait.
	mu.Lock()
	rows = []map[string]any{{"page_id": ids[0], "updated_at": at}}
	mu.Unlock()
	if err := svc.setWaiting(map[string]bool{ids[1]: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.pullContents(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.contentsAt != 2 {
		t.Errorf("a pull left %d at a time", svc.contentsAt)
	}
}
