package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	leafletsync "leaflet/internal/sync"
)

// TestUsingAnotherAccountCutsThePassUnderWay has the server answer a pull of the account before only once the device
// has moved to another: the pass under way is cut, so none of the answer reaches the store.
func TestUsingAnotherAccountCutsThePassUnderWay(t *testing.T) {
	const old = "00000000-0000-0000-0000-0000000000a1"
	asked, release := make(chan struct{}), make(chan struct{})
	var once gosync.Once
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/rest/v1/pages" && r.URL.Query().Get("select") == "*" {
			first := false
			once.Do(func() { first = true })
			if first {
				close(asked)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				at := "2026-01-01T00:00:00Z"
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": old, "space_id": "00000000-0000-0000-0000-0000000000ff",
					"title": "Of the account before", "sort_key": "V", "created_at": at, "updated_at": at}})
				return
			}
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer hs.Close()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"account": "u1", "space.u1": st.Session().SpaceID} {
		if err := st.SetValue(k, v); err != nil {
			t.Fatal(err)
		}
	}
	c := &Core{Store: st, Remote: remote.SignedInForTest(hs.URL, st, "u1")}
	c.Sync = leafletsync.New(st, c.Remote)

	passed := make(chan struct{})
	go func() {
		defer close(passed)
		_ = c.Sync.SyncNow()
	}()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass never pulled")
	}
	c.Remote.SignInForTest("u2")
	used := make(chan error, 1)
	go func() { used <- c.UseAccount(&store.User{ID: "u2", Email: "u2@example.com"}, true) }()
	select {
	case err := <-used:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UseAccount waited on the pass rather than cut it")
	}
	close(release)
	<-passed
	if _, err := st.Page(old); !store.IsNotFound(err) {
		t.Errorf("a page of the account before is in the store: %v", err)
	}
	if cursors := st.ValuesWithPrefix("sync.cursor."); len(cursors) != 0 {
		t.Errorf("cursors of the account before: %v", cursors)
	}
	if got := st.Session().UserID; got != "u2" {
		t.Errorf("session of %q", got)
	}
}
