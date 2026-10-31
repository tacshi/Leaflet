package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
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

// What a page is shows after its breadcrumbs as icons, named by their
// tooltips; that it is shared with the user is in its byline.
func TestPageMarks(t *testing.T) {
	a := templatesApp(t)
	const id = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.st.PutRemotePage(map[string]any{"id": id, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(id)
	p := a.pageByID(id)
	tt := ui.NewTester(func(c *ui.Context) {
		a.breadcrumbs(c, *p)
		a.byline(c, *p)
	}, 900, 300)
	tt.Frame()
	if _, ok := tt.Find(locales.PageViewOnly()); !ok {
		t.Errorf("no view-only mark in %v", tt.Texts())
	}
	if !tt.HasText(locales.PageBylineSharedWithYou()) {
		t.Errorf("the byline does not say it is shared: %v", tt.Texts())
	}
	for _, pill := range []string{"Shared", "Template"} {
		if _, ok := tt.Find(pill); ok {
			t.Errorf("a %q pill", pill)
		}
	}
}

// Someone invited to a page who has not accepted yet is no account: the
// @ menu does not offer to mention them.
func TestMentionsLeaveOutInvites(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.page = &pageState{id: "p", members: []store.Member{
		{UserID: "ann", DisplayName: "Ann", Role: "editor", Accepted: true},
		{UserID: "invite-1", Email: "kim@example.com", Role: "viewer"},
	}}
	var people []string
	for _, it := range (&host{a: a}).MentionItems("") {
		if it.Group == locales.HostGroupPeople() {
			people = append(people, it.Label)
		}
	}
	if len(people) != 1 || people[0] != "Ann" {
		t.Errorf("people %v", people)
	}
}

// A viewer is offered no Resolve on someone else's thread, which the
// server would refuse, and keeps it on their own.
func TestViewerResolvesOnlyTheirOwnThreads(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	const id = "00000000-0000-0000-0000-0000000000bb"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.st.PutRemotePage(map[string]any{"id": id, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: "viewer"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutRemoteComment(map[string]any{"id": "c1", "page_id": id, "block_id": "b1", "author_id": "someone",
		"author_kind": "user", "body": "Their question", "created_at": at, "updated_at": at}, "Sam"); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(id)
	a.panelOpen, a.panelTab = true, 1
	tt := ui.NewTester(func(c *ui.Context) { a.commentsPanel(c) }, 400, 800)
	tt.Frame()
	if !tt.HasText("Their question") {
		t.Fatalf("no thread in %v", tt.Texts())
	}
	if tt.HasText(locales.PanelResolve()) {
		t.Error("a viewer may resolve someone else's thread")
	}
	if _, err := a.st.AddComment(id, "b2", "My question", "", "", "user", ""); err != nil {
		t.Fatal(err)
	}
	a.page.threads, _ = a.st.Threads(id)
	tt.Frame()
	if !tt.HasText(locales.PanelResolve()) {
		t.Error("a viewer may not resolve their own thread")
	}
}
