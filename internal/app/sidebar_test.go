package app

import (
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
	a.route = Route{Kind: "settings", Section: "account"}
	tt := ui.NewTester(func(c *ui.Context) { a.settingsNav(c).FillHeight() }, 300, 400)
	tt.Frame()
	if err := tt.Click("Offline and storage"); err != nil {
		t.Fatal(err)
	}
	if a.route.Section != "offline" {
		t.Errorf("section %q", a.route.Section)
	}
}

// Navigation is MyGo's router: pages opened go into its history, Back and
// Forward go through it, and a settings section chosen joins it.
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
	a.navigate(Route{Kind: "settings", Section: "account"})
	a.route.Section = "offline" // as the sections' sidebar chooses one
	a.follow()
	if a.router.Path() != "/settings/offline" {
		t.Errorf("router at %q", a.router.Path())
	}
	a.back()
	if a.route != (Route{Kind: "settings", Section: "account"}) {
		t.Errorf("Back went to %+v", a.route)
	}
	for _, r := range []Route{{Kind: "home"}, {Kind: "page", ID: "x"}, {Kind: "settings", Section: "speech"}, {Kind: "todos"}, {Kind: "signin"}} {
		if got := routeAt(r.path()); got != r {
			t.Errorf("%+v went to %q and back to %+v", r, r.path(), got)
		}
	}
}
