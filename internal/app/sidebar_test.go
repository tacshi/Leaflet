package app

import (
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
)

// The sidebar's tree is MyGo's Outline: choosing a page opens it and is
// its row chosen, choosing a folder opens it to its pages, and a page
// opened shows the pages above it opened.
func TestSidebarTree(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	f, err := a.st.CreateFolder("Product")
	if err != nil {
		t.Fatal(err)
	}
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", FolderID: f.ID})
	faq, _ := a.st.CreatePage(store.NewPageOptions{Title: "FAQ", ParentID: prd.ID})
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if !tt.HasText("Product") || !tt.HasText("Notes") || tt.HasText("PRD") {
		t.Fatalf("closed folder shows %v", tt.Texts())
	}
	if tt.HasText("My space") {
		t.Errorf("the tree has a My space header: %v", tt.Texts())
	}
	if err := tt.Click("Product"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("PRD") || tt.HasText("FAQ") {
		t.Fatalf("open folder shows %v", tt.Texts())
	}
	if err := tt.Click("Notes"); err != nil {
		t.Fatal(err)
	}
	if a.route.Kind != "page" || a.route.ID != notes.ID {
		t.Errorf("route %+v", a.route)
	}
	// Opening a subpage elsewhere opens the pages above it in the tree.
	a.openPage(faq.ID)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("FAQ") {
		t.Errorf("the open page's row does not show: %v", tt.Texts())
	}
	if a.treeRow < 0 || a.treeState.Item(a.treeRow) != "page:"+faq.ID {
		t.Errorf("chosen row %d", a.treeRow)
	}
}

// A page dragged in the tree goes between pages by their top and bottom
// edges, and inside one by its middle, which then links to it, until it
// moves out; a click on it still opens it.
func TestSidebarDragPages(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	var ids []string
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		p, _ := a.st.CreatePage(store.NewPageOptions{Title: title})
		ids = append(ids, p.ID)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	order := func() (out []string) {
		for _, p := range a.children("", "", true) {
			out = append(out, p.Title)
		}
		return out
	}
	drag := func(from, onto string, dy float32) {
		t.Helper()
		f, ok1 := tt.Find(from)
		o, ok2 := tt.Find(onto)
		if !ok1 || !ok2 {
			t.Fatalf("no %s or %s in %v", from, onto, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		ty := o.Y + o.H/2 + dy
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x, y+(ty-y)*float32(i)/4)
		}
		tt.Release(x, ty)
		a.reloadAll()
		tt.Frame()
	}
	// Gamma before Alpha: onto Alpha's top edge.
	drag("Gamma", "Alpha", -10)
	if got := fmt.Sprint(order()); got != "[Gamma Alpha Beta]" {
		t.Fatalf("order %s", got)
	}
	// Gamma after Beta: onto Beta's bottom edge.
	drag("Gamma", "Beta", 10)
	if got := fmt.Sprint(order()); got != "[Alpha Beta Gamma]" {
		t.Fatalf("order %s", got)
	}
	// Alpha inside Beta: onto its middle.
	drag("Alpha", "Beta", 0)
	if p := a.pageByID(ids[0]); p == nil || p.ParentID != ids[1] {
		t.Fatalf("Alpha is under %q", p.ParentID)
	}
	if got := fmt.Sprint(order()); got != "[Beta Gamma]" {
		t.Fatalf("order %s", got)
	}
	if !a.hasSubpageLink(ids[1], ids[0]) {
		t.Error("Beta does not link to Alpha inside it")
	}
	// Beta cannot go inside Alpha, its own subpage.
	tt.Frame()
	drag("Beta", "Alpha", 0)
	if p := a.pageByID(ids[1]); p == nil || p.ParentID != "" {
		t.Fatalf("Beta moved under %q", p.ParentID)
	}
	// Alpha back to the top level, by Move to…: Beta's link goes.
	if err := a.movePage(ids[0], "", func() error { return a.st.MovePage(ids[0], "", "", "") }); err != nil {
		t.Fatal(err)
	}
	if a.hasSubpageLink(ids[1], ids[0]) {
		t.Error("Beta still links to Alpha, moved out")
	}
	a.reloadAll()
	tt.Frame()
	if err := tt.Click("Gamma"); err != nil {
		t.Fatal(err)
	}
	if a.route.Kind != "page" || a.route.ID != ids[2] {
		t.Errorf("a click on a draggable row opens %+v", a.route)
	}
}

// A subpage comes out of its page by the bottom edge of the last
// subpage with the pointer left of it, or by the room below the tree.
func TestSidebarDragOut(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	welcome, _ := a.st.CreatePage(store.NewPageOptions{Title: "Welcome"})
	reversi, _ := a.st.CreatePage(store.NewPageOptions{Title: "Reversi", ParentID: welcome.ID})
	sync, _ := a.st.CreatePage(store.NewPageOptions{Title: "Sync", ParentID: welcome.ID})
	a.reloadAll()
	a.treeOpen("page", welcome.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	drag := func(from string, tx, ty float32) {
		t.Helper()
		f, ok := tt.Find(from)
		if !ok {
			t.Fatalf("no %s in %v", from, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x+(tx-x)*float32(i)/4, y+(ty-y)*float32(i)/4)
		}
		tt.Release(tx, ty)
		a.reloadAll()
		tt.Frame()
	}
	// Onto the bottom edge of Sync, the last subpage, left of its icon:
	// after Welcome.
	// Find gives the whole row; its indent is at its start.
	s, _ := tt.Find("Sync")
	drag("Reversi", s.X+12, s.Y+s.H/2+10)
	if p := a.pageByID(reversi.ID); p == nil || p.ParentID != "" {
		t.Fatalf("Reversi is under %q", p.ParentID)
	}
	var top []string
	for _, p := range a.children("", "", true) {
		top = append(top, p.Title)
	}
	if fmt.Sprint(top) != "[Welcome Reversi]" {
		t.Fatalf("top level %v", top)
	}
	// Into the room below the tree: the end of the top level.
	drag("Sync", 130, 450)
	if p := a.pageByID(sync.ID); p == nil || p.ParentID != "" {
		t.Fatalf("Sync is under %q", p.ParentID)
	}
	if a.hasSubpageLink(welcome.ID, sync.ID) {
		t.Error("Welcome still links to Sync")
	}
}

// A page's path is MyGo's breadcrumbs: its folder, the pages above it and
// it; a click on a page above opens it.
func TestBreadcrumbs(t *testing.T) {
	a := templatesApp(t)
	f, _ := a.st.CreateFolder("Product")
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", FolderID: f.ID})
	faq, _ := a.st.CreatePage(store.NewPageOptions{Title: "FAQ", ParentID: prd.ID})
	a.reloadAll()
	a.openPage(faq.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		if p := a.pageByID(a.page.id); p != nil {
			a.breadcrumbs(c, *p)
		}
	}, 600, 60)
	tt.Frame()
	for _, s := range []string{"Product", "PRD", "FAQ"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %v", s, tt.Texts())
		}
	}
	if err := tt.Click("PRD"); err != nil {
		t.Fatal(err)
	}
	if a.route.ID != prd.ID {
		t.Errorf("route %+v", a.route)
	}
}

// Settings' sections are MyGo's sidebar: a click shows a section.
func TestSettingsSections(t *testing.T) {
	a := templatesApp(t)
	a.settingsSection = "account"
	tt := ui.NewTester(func(c *ui.Context) { a.settingsNav(c).FillHeight() }, 300, 400)
	tt.Frame()
	if err := tt.Click("Sync and storage"); err != nil {
		t.Fatal(err)
	}
	if a.settingsSection != "offline" {
		t.Errorf("section %q", a.settingsSection)
	}
}

// Settings is a dialog over the window: opening it leaves the route and
// the router's history, Escape closes the dialog on top first.
func TestSettingsModal(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen, a.sidebarW = true, 240
	a.route = Route{Kind: "signin"}
	a.router = ui.NewRouter("/signin")
	a.routerAt = a.router.Path()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	if err := tt.Click("Settings (⌘,)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settingsOpen || a.settingsSection != "account" {
		t.Errorf("the footer's button opened %v at %q", a.settingsOpen, a.settingsSection)
	}
	a.openSettings("providers")
	tt.Frame()
	if !tt.HasText("Agent providers") {
		t.Error("Settings did not open at Agent providers")
	}
	if a.route != (Route{Kind: "signin"}) || a.router.Path() != "/signin" || a.router.CanGoBack() {
		t.Errorf("route %+v, router at %q", a.route, a.router.Path())
	}
	if err := tt.Click("Account"); err != nil {
		t.Fatal(err)
	}
	account.deleteOpen = true
	t.Cleanup(func() { account.deleteOpen = false })
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if account.deleteOpen || !a.settingsOpen {
		t.Fatalf("Escape left delete %v, settings %v", account.deleteOpen, a.settingsOpen)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.settingsOpen {
		t.Error("Escape did not close Settings")
	}
	if a.route != (Route{Kind: "signin"}) {
		t.Errorf("closing Settings went to %+v", a.route)
	}
}

// Navigation is MyGo's router: pages opened go into its history, Back and
// Forward go through it.
func TestRouterHistory(t *testing.T) {
	a := templatesApp(t)
	one, _ := a.st.CreatePage(store.NewPageOptions{Title: "One"})
	two, _ := a.st.CreatePage(store.NewPageOptions{Title: "Two"})
	a.reloadAll()
	a.route = Route{Kind: "home"}
	a.router = ui.NewRouter("/")
	a.routerAt = a.router.Path()
	a.navigate(Route{Kind: "page", ID: one.ID})
	a.navigate(Route{Kind: "page", ID: two.ID})
	if a.router.Path() != "/page/"+two.ID || !a.router.CanGoBack() {
		t.Fatalf("router at %q", a.router.Path())
	}
	a.back()
	if a.route.ID != one.ID || a.page == nil || a.page.id != one.ID {
		t.Fatalf("Back went to %+v", a.route)
	}
	a.forward()
	if a.route.ID != two.ID {
		t.Fatalf("Forward went to %+v", a.route)
	}
	for _, r := range []Route{{Kind: "home"}, {Kind: "page", ID: "x"}, {Kind: "signin"}} {
		if got := routeAt(r.path()); got != r {
			t.Errorf("%+v went to %q and back to %+v", r, r.path(), got)
		}
	}
}

// The sidebar's header makes folders, beside New page, and its tabs show
// the pages, the tags and the to-dos, the tab chosen kept in Settings.
func TestSidebarTabs(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	plan, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Tags: []string{"work"}})
	_, _ = a.st.CreatePage(store.NewPageOptions{Title: "Standup", Tags: []string{"Work", "daily"}})
	_, _ = a.st.CreatePage(store.NewPageOptions{Title: "Untagged"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if err := tt.Click("New folder"); err != nil {
		t.Fatal(err)
	}
	if !a.dialogs.folderOpen {
		t.Error("New folder opened no dialog")
	}
	a.dialogs.folderOpen = false
	if err := tt.Click("Tags"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.SidebarTab != tabTags {
		t.Fatalf("tab %d", a.settings.SidebarTab)
	}
	if !tt.HasText("work") || !tt.HasText("daily") || tt.HasText("Untagged") || tt.HasText("Plan") {
		t.Fatalf("Tags tab shows %v", tt.Texts())
	}
	if err := tt.Click("work"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Plan") || !tt.HasText("Standup") {
		t.Fatalf("an open tag shows %v", tt.Texts())
	}
	if err := tt.Click("Plan"); err != nil {
		t.Fatal(err)
	}
	if a.route.Kind != "page" || a.route.ID != plan.ID {
		t.Errorf("route %+v", a.route)
	}
	if err := tt.Click("Pages"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.SidebarTab != tabPages || !tt.HasText("Untagged") {
		t.Errorf("Pages tab %d shows %v", a.settings.SidebarTab, tt.Texts())
	}
}

// A tag renamed in the Tags tab is renamed on its pages, and one deleted
// comes back with Undo.
func TestRenameAndDeleteTag(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Tags: []string{"work", "q4"}})
	a.reloadAll()
	a.dialogs.openRenameTag("work")
	tt := ui.NewTester(func(c *ui.Context) { a.tagDialog(c) }, 600, 400)
	tt.Frame()
	tt.Frame() // the name is selected as the field takes the focus
	tt.Type("job")
	tt.Key(0, ui.KeyEnter)
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job", "q4"}) || a.dialogs.tagOpen {
		t.Fatalf("renamed to %q, dialog open %v", got.Tags, a.dialogs.tagOpen)
	}
	a.reloadAll()
	a.deleteTag("q4")
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job"}) {
		t.Fatalf("after delete %q", got.Tags)
	}
	a.toasts[len(a.toasts)-1].fn()
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job", "q4"}) {
		t.Errorf("after undo %q", got.Tags)
	}
}
