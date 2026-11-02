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

// A page shared through the page it is under can be edited while it is
// there, and only viewed once it moves under one shared for viewing.
func TestReadOnlyFollowsWhereThePageIs(t *testing.T) {
	a := templatesApp(t)
	const space = "00000000-0000-0000-0000-0000000000cc"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	put := func(id, parent string) {
		t.Helper()
		row := map[string]any{"id": id, "space_id": space, "title": id, "sort_key": "V", "created_at": at, "updated_at": at}
		if parent != "" {
			row["parent_id"] = parent
		}
		if err := a.st.PutRemotePage(row); err != nil {
			t.Fatal(err)
		}
	}
	const edit, view, page = "00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2", "00000000-0000-0000-0000-0000000000e3"
	put(edit, "")
	put(view, "")
	put(page, edit)
	for id, role := range map[string]string{edit: "editor", view: "viewer"} {
		if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: role, Accepted: true}}); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	a.openPage(page)
	if a.page.editor.ReadOnly {
		t.Fatal("a page under one shared for editing is read-only")
	}
	put(page, view)
	a.reload(store.Change{Table: "pages", PageID: page})
	if !a.page.editor.ReadOnly {
		t.Error("the page moved under one shared for viewing can still be edited")
	}
}

// The byline says who created the page, which is all a page keeps, and
// apart from it when the page was edited.
func TestBylineSaysWhoCreated(t *testing.T) {
	a := templatesApp(t)
	const id = "00000000-0000-0000-0000-0000000000bd"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.st.PutRemotePage(map[string]any{"id": id, "space_id": "00000000-0000-0000-0000-0000000000cc", "title": "Theirs",
		"sort_key": "V", "created_by": "00000000-0000-0000-0000-0000000000ee", "created_at": at, "updated_at": at}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(id)
	tt := ui.NewTester(func(c *ui.Context) { a.byline(c, *a.pageByID(id)) }, 900, 100)
	tt.Frame()
	if !tt.HasText(locales.PageBylineCreated(locales.PageBylineSomeone())) || !tt.HasText(locales.PageBylineEditedAt(locales.PageTimeJustNow())) {
		t.Errorf("byline %v", tt.Texts())
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

// The user is a member of their own page, as its owner: a page that only
// syncs between their devices is not shared, and one shared with Ann is
// shared with 1.
func TestBylineCountsOthers(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	owner := store.Member{PageID: p.ID, UserID: a.session.UserID, Role: "owner", Accepted: true}
	for _, c := range []struct {
		members []store.Member
		shared  bool
	}{
		{[]store.Member{owner}, false},
		{[]store.Member{owner, {PageID: p.ID, UserID: "ann", DisplayName: "Ann", Role: "editor", Accepted: true}}, true},
	} {
		if err := a.st.SetMembers(p.ID, c.members); err != nil {
			t.Fatal(err)
		}
		a.reload(store.Change{Table: "page_members", PageID: p.ID})
		tt := ui.NewTester(func(c *ui.Context) { a.byline(c, *a.pageByID(p.ID)) }, 900, 100)
		tt.Frame()
		if got := tt.HasText(locales.PageBylineSharedWith(1)); got != c.shared {
			t.Errorf("with %d members, shared with 1 is %v: %v", len(c.members), got, tt.Texts())
		}
	}
}
