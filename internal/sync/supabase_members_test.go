package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// A page whose share the server could not be asked about keeps its
// members until it can: forgotten, the page would turn read-only here.
func TestWithdrawnKeepsMembersWhenTheServerFails(t *testing.T) {
	st := openStore(t)
	old := "2026-01-01T00:00:00Z"
	const page = "00000000-0000-0000-0000-0000000000a1"
	if err := st.PutRemotePage(map[string]any{"id": page, "space_id": "00000000-0000-0000-0000-0000000000ff", "title": "Shared", "sort_key": "V", "created_at": old, "updated_at": old}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(page, []store.Member{{UserID: "u1", Role: "editor", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	// The page's members are refused, and the server fails to say more.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/rpc/page_members") {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "42501", "message": "forbidden"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message": "down"}`))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	if err := svc.withdrawn(context.Background(), page); err == nil {
		t.Error("no error from a server that failed")
	}
	if st.MembersOrNil(page) == nil {
		t.Error("members forgotten")
	}
	// Nor do they count as loaded, so the next pass tries again.
	if err := svc.LoadMembers(context.Background(), page); err == nil {
		t.Error("no error from a server that failed")
	}
	svc.mu.Lock()
	_, stamped := svc.membersAt[page]
	svc.mu.Unlock()
	if stamped {
		t.Error("members taken for loaded")
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
			role := roles[args.PageID]
			mu.Unlock()
			out = []map[string]any{
				{"user_id": "u2", "role": "owner", "accepted": true},
				{"user_id": "u1", "role": role, "accepted": true},
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

	// The share changes to view only. The members kept of a page inside
	// the tree, as for one opened, load again with the top page's.
	if err := st.SetMembers(child, []store.Member{{PageID: child, UserID: "u1", Role: "editor", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	roles[root], roles[child] = "viewer", "viewer"
	asked = nil
	mu.Unlock()
	svc.membersAt[root] = time.Now().Add(-membersEvery)
	svc.pullMembers(context.Background())
	if strings.Join(asked, ",") != root+","+child {
		t.Errorf("members asked of %v, want %s and %s", asked, root, child)
	}
	for _, id := range []string{root, child} {
		if p, _ := st.Page(id); st.CanEdit(p) {
			t.Errorf("changed to view only, %s is still editable", id)
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

// A share withdrawn while it was pulled is handled once its page is gone:
// the pull goes on past it to the comments and the content.
func TestPullGoesOnPastAWithdrawnShare(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	const gone = "00000000-0000-0000-0000-0000000000e1"
	var mu gosync.Mutex
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/rest/v1/rpc/page_members":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"42501","message":"forbidden"}`))
		case r.URL.Path == "/rest/v1/page_permissions" && q.Get("user_id") == "eq.u1" && q.Get("updated_at") == "gt.1970-01-01T00:00:00Z":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"page_id": gone, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}})
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))

	if err := svc.pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(asked, "GET /rest/v1/page_content") {
		t.Errorf("the pull stopped at the share: %v", asked)
	}
	if c := svc.cursor("page_permissions"); c.key != gone {
		t.Errorf("cursor %+v", c)
	}
}

// Sync keeps who the user's own pages are shared with, so an agent on one
// knows whether someone else writes in it: the shares the server lists
// load the page's members, or stand for them when they do not load, and
// members of a share withdrawn go.
func TestPullKeepsOwnPagesMembers(t *testing.T) {
	st := openStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	var mu gosync.Mutex
	shares := []map[string]any{{"page_id": p.ID, "user_id": "u2", "role": "editor"}}
	membersLoad := false
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var out any = []map[string]any{}
		switch r.URL.Path {
		case "/rest/v1/page_permissions":
			if r.URL.Query().Get("user_id") == "neq.u1" {
				out = shares
			}
		case "/rest/v1/rpc/page_members":
			if !membersLoad {
				http.Error(w, `{"message":"try again"}`, http.StatusInternalServerError)
				return
			}
			list := []map[string]any{{"user_id": "u1", "role": "owner", "accepted": true}}
			for _, s := range shares {
				list = append(list, map[string]any{"user_id": s["user_id"], "role": s["role"], "display_name": "Ana", "accepted": true})
			}
			out = list
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	ctx := context.Background()
	others := func() []string {
		var ids []string
		for _, m := range st.MembersOrNil(p.ID) {
			if m.UserID != "u1" {
				ids = append(ids, m.UserID)
			}
		}
		return ids
	}

	if err := svc.pullOwnMembers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := others(); !slices.Equal(got, []string{"u2"}) {
		t.Errorf("members that did not load: %v", got)
	}
	if v, _ := st.Value(OwnMembersKey); v == "" {
		t.Error("not kept as known")
	}

	mu.Lock()
	shares, membersLoad = nil, true
	mu.Unlock()
	svc.membersAt[ownSwept] = time.Now().Add(-membersEvery)
	if err := svc.pullOwnMembers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := others(); len(got) != 0 {
		t.Errorf("the share went, members %v stayed", got)
	}
}
