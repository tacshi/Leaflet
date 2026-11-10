package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func folderParents(t *testing.T, st *store.Store, ids ...string) []string {
	t.Helper()
	out := make([]string, len(ids))
	for i, id := range ids {
		f, err := st.Folder(id)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = f.ParentFolderID
	}
	return out
}

func TestICloudSettlesFoldersMovedIntoEachOther(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	x, _ := a.st.CreateFolder("X")
	y, err := a.st.CreateFolder("Y")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := a.st.MoveFolder(x.ID, y.ID, ""); err != nil {
		t.Fatal(err)
	}
	nextStamp()
	if err := b.st.MoveFolder(y.ID, x.ID, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	syncAll(a, b)
	least, other := min(x.ID, y.ID), max(x.ID, y.ID)
	for name, d := range map[string]*cloudDevice{"A": a, "B": b} {
		if got := folderParents(t, d.st, least, other); got[0] != "" || got[1] != least {
			t.Errorf("%s: the folders are in %q, want the smaller id at the top level and the other in it", name, got)
		}
		if n := d.st.PendingCount(); n != 0 {
			t.Errorf("%s: %d changes still wait", name, n)
		}
	}
}

func TestPullSettlesFoldersMovedIntoEachOther(t *testing.T) {
	st := openStore(t)
	const x, y = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	row := func(id, parent string, at time.Time) map[string]any {
		return map[string]any{"id": id, "space_id": st.Session().SpaceID, "parent_folder_id": parent, "name": id[:1],
			"sort_key": "V", "created_at": at.Format(time.RFC3339Nano), "updated_at": at.Format(time.RFC3339Nano)}
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []map[string]any{}
		if r.URL.Path == "/rest/v1/folders" {
			rows = append(rows, row(y, x, at), row(x, y, at.Add(time.Second)))
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := folderParents(t, st, x, y); got[0] != "" || got[1] != x {
		t.Errorf("the folders are in %q, want the smaller id at the top level and the other in it", got)
	}
	e := pendingOf(t, st, "folders", x)
	if e == nil || pendingOf(t, st, "folders", y) != nil {
		t.Fatal("the lift, and nothing else, should be queued to sync")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if parent, sent := payload["parent_folder_id"]; !sent || parent != nil {
		t.Errorf("the queued change sets the parent to %v", parent)
	}
}

func TestAFolderThatIsItsOwnParentDoesNotWaitForItself(t *testing.T) {
	st := openStore(t)
	f, err := st.CreateFolder("Self")
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	f.ParentFolderID = f.ID
	if err := st.PutFolder(f); err != nil {
		t.Fatal(err)
	}
	if err := st.Queue("folders", f.ID, store.FolderToRemote(f), "moved folder", ""); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"23514","message":"new row for relation \"folders\" violates check constraint"}`))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := attempts(t, st, "folders", f.ID); n != 1 {
		t.Errorf("the refused folder failed %d times, want 1: it must not wait for itself", n)
	}
}

func TestALoopOfFoldersFromBeforeIsSettledByTheNextPass(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	x, _ := a.st.CreateFolder("X")
	y, err := a.st.CreateFolder("Y")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	for _, d := range []*cloudDevice{a, b} {
		for id, parent := range map[string]string{x.ID: y.ID, y.ID: x.ID} {
			f, err := d.st.Folder(id)
			if err != nil {
				t.Fatal(err)
			}
			f.ParentFolderID = parent
			if err := d.st.PutFolder(f); err != nil {
				t.Fatal(err)
			}
		}
	}
	syncAll(a, b)
	least, other := min(x.ID, y.ID), max(x.ID, y.ID)
	for name, d := range map[string]*cloudDevice{"A": a, "B": b} {
		if got := folderParents(t, d.st, least, other); got[0] != "" || got[1] != least {
			t.Errorf("%s: the folders are in %q, want the smaller id at the top level and the other in it", name, got)
		}
		if n := d.st.PendingCount(); n != 0 {
			t.Errorf("%s: %d changes still wait", name, n)
		}
	}
}

// servesRows answers a pull of each table with its rows, oldest change first as the server orders them.
func servesRows(t *testing.T, st *store.Store, tables map[string][]map[string]any) *Service {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := tables[strings.TrimPrefix(r.URL.Path, "/rest/v1/")]
		if rows == nil || r.Method != http.MethodGet {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(hs.Close)
	return New(st, remote.SignedInForTest(hs.URL, st, "u1"))
}

func TestPullKeepsFoldersSwappedOnAnotherDevice(t *testing.T) {
	st := openStore(t)
	x, _ := st.CreateFolder("X")
	y, err := st.CreateFolderIn("Y", x.ID)
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	// Elsewhere Y went to the top level, X into it, and Y was renamed: X's row is the older, and comes first.
	at := time.Now().UTC().Add(time.Minute)
	rx, ry := store.FolderToRemote(x), store.FolderToRemote(y)
	rx["parent_folder_id"], rx["updated_at"] = y.ID, at.Format(time.RFC3339Nano)
	ry["parent_folder_id"], ry["name"], ry["updated_at"] = nil, "Renamed", at.Add(time.Second).Format(time.RFC3339Nano)
	svc := servesRows(t, st, map[string][]map[string]any{"folders": {rx, ry}})
	if err := svc.pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := folderParents(t, st, x.ID, y.ID); got[0] != y.ID || got[1] != "" {
		t.Errorf("the folders are in %q, want X in Y and Y at the top level", got)
	}
	if n := st.PendingCount(); n != 0 {
		t.Errorf("%d changes were queued over the other device's moves", n)
	}
}

func TestPullKeepsPagesSwappedOnAnotherDevice(t *testing.T) {
	st := openStore(t)
	x, _ := st.CreatePage(store.NewPageOptions{ID: "11111111-1111-4111-8111-111111111111", Title: "X"})
	y, err := st.CreatePage(store.NewPageOptions{ID: "22222222-2222-4222-8222-222222222222", Title: "Y", ParentID: x.ID})
	if err != nil {
		t.Fatal(err)
	}
	dequeueAll(t, st)
	at := time.Now().UTC().Add(time.Minute)
	rx, ry := store.PageToRemote(x), store.PageToRemote(y)
	rx["parent_id"], rx["updated_at"] = y.ID, at.Format(time.RFC3339Nano)
	ry["parent_id"], ry["title"], ry["updated_at"] = nil, "Renamed", at.Add(time.Second).Format(time.RFC3339Nano)
	svc := servesRows(t, st, map[string][]map[string]any{"pages": {rx, ry}})
	if err := svc.pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	gx, _ := st.Page(x.ID)
	gy, _ := st.Page(y.ID)
	if gx.ParentID != y.ID || gy.ParentID != "" {
		t.Errorf("X is in %q and Y in %q, want X in Y and Y at the top level", gx.ParentID, gy.ParentID)
	}
}

func TestICloudKeepsFoldersSwappedOnAnotherDevice(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	f1, _ := a.st.CreateFolder("One")
	f2, err := a.st.CreateFolder("Two")
	if err != nil {
		t.Fatal(err)
	}
	x, y := min(f1.ID, f2.ID), max(f1.ID, f2.ID)
	if err := a.st.MoveFolder(y, x, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	// X's change is written first, and still says Y is inside it when B reads it.
	if err := a.st.RenameFolder(store.Folder{ID: x}, "X"); err != nil {
		t.Fatal(err)
	}
	for _, move := range [][2]string{{y, ""}, {x, y}} {
		if err := a.st.MoveFolder(move[0], move[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(a, b)
	for name, d := range map[string]*cloudDevice{"A": a, "B": b} {
		if got := folderParents(t, d.st, x, y); got[0] != y || got[1] != "" {
			t.Errorf("%s: the folders are in %q, want X in Y and Y at the top level", name, got)
		}
		if n := d.st.PendingCount(); n != 0 {
			t.Errorf("%s: %d changes still wait", name, n)
		}
	}
}

func TestICloudKeepsPagesSwappedOnAnotherDevice(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p1, p2 := a.page("One"), a.page("Two")
	x, y := min(p1.ID, p2.ID), max(p1.ID, p2.ID)
	if err := a.st.MovePage(y, x, "", ""); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := a.st.RenamePage(x, "X"); err != nil {
		t.Fatal(err)
	}
	for _, move := range [][2]string{{y, ""}, {x, y}} {
		if err := a.st.MovePage(move[0], move[1], "", ""); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(a, b)
	for name, d := range map[string]*cloudDevice{"A": a, "B": b} {
		gx, _ := d.st.Page(x)
		gy, _ := d.st.Page(y)
		if gx.ParentID != y || gy.ParentID != "" {
			t.Errorf("%s: X is in %q and Y in %q, want X in Y and Y at the top level", name, gx.ParentID, gy.ParentID)
		}
	}
}
