package app

import (
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
)

// A page shared with the user opens read-only until its members say
// they may edit it, and follows their role as it changes.
func TestSharedPageFollowsTheUsersRole(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.PutRemotePage(map[string]any{"id": id, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	a.openPage(id)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	if !a.page.editor.ReadOnly {
		t.Error("editable before its members loaded")
	}
	for _, c := range []struct {
		role     string
		readOnly bool
	}{{"editor", false}, {"viewer", true}} {
		if err := st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: c.role}}); err != nil {
			t.Fatal(err)
		}
		a.reload(store.Change{Table: "page_members", PageID: id})
		if a.page.editor.ReadOnly != c.readOnly {
			t.Errorf("as %s, read-only is %v", c.role, a.page.editor.ReadOnly)
		}
	}
}
