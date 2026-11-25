package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestBinShowsRestoresAndDeletesForever(t *testing.T) {
	a := templatesApp(t)
	keep, _ := a.st.CreatePage(store.NewPageOptions{Title: "Keep me", Content: &doc.Content{Children: []*doc.Node{
		{ID: "b1", Type: "paragraph", Data: map[string]any{"delta": []any{map[string]any{"insert": "what was written"}}}},
	}}})
	drop, _ := a.st.CreatePage(store.NewPageOptions{Title: "Drop me"})
	for _, id := range []string{keep.ID, drop.ID} {
		if _, err := a.st.DeletePage(id); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	subscribe(a)
	a.openBin()
	tt := ui.NewTester(a.main, 1100, 700)
	tt.Frame()
	for _, want := range []string{binName(), "Keep me", "Drop me", locales.BinChoose()} {
		if !tt.HasText(want) {
			t.Fatalf("no %q in %v", want, tt.Texts())
		}
	}
	if err := tt.Click("Keep me"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("what was written") {
		t.Fatalf("the page is not shown: %v", tt.Texts())
	}
	if err := tt.Click(locales.BinRestore()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.pageByID(keep.ID) == nil || tt.HasText("Keep me") {
		t.Fatalf("not restored: %v", tt.Texts())
	}

	if err := tt.Click("Drop me"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.BinDeleteForever()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(locales.BinDeleteForeverTitle("Drop me")) {
		t.Fatalf("no alert in %v", tt.Texts())
	}
	if err := tt.Click(locales.BinDeleteForever()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !a.st.IsPurged(drop.ID) {
		t.Error("not deleted forever")
	}
	if !tt.HasText(locales.BinNothing(binName())) {
		t.Errorf("the bin is not empty: %v", tt.Texts())
	}
}

// subscribe has the app take in the store's changes, as it does running.
func subscribe(a *App) {
	a.st.Subscribe(func(ch store.Change) { a.update(func() { a.reload(ch) }) })
}

// Restoring, deleting for good and emptying the bin each read it once more, as the store tells what changed.
func TestBinReadOnceAChange(t *testing.T) {
	a := templatesApp(t)
	for _, title := range []string{"One", "Two", "Three"} {
		p, _ := a.st.CreatePage(store.NewPageOptions{Title: title})
		if _, err := a.st.DeletePage(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	subscribe(a)
	a.openBin()
	if len(a.bin.items) != 3 {
		t.Fatalf("the bin lists %d", len(a.bin.items))
	}
	reads := func(what string, do func()) {
		t.Helper()
		was := a.bin.listReads
		do()
		if n := a.bin.listReads - was; n != 1 {
			t.Errorf("%s read the bin %d times", what, n)
		}
	}
	back := a.bin.items[0]
	reads("restoring", func() { a.restoreFromBin(back) })
	if len(a.bin.items) != 2 || a.pageByID(back.ID) == nil {
		t.Errorf("after restoring the bin lists %d, the page listed %v", len(a.bin.items), a.pageByID(back.ID) != nil)
	}
	reads("deleting for good", func() {
		ref := a.bin.items[0].BinRef
		a.purgeFromBin(func(cr *core.Core) error { return cr.PurgeBin(ref) }, "")
	})
	reads("emptying", func() { a.purgeFromBin((*core.Core).EmptyBin, "") })
	if len(a.bin.items) != 0 {
		t.Errorf("after emptying the bin lists %d", len(a.bin.items))
	}
}

// A bin that cannot be read keeps what it listed, says why, and doesn't say it is empty.
func TestBinThatCannotBeReadKeepsItsList(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Still here"})
	if _, err := a.st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openBin()
	tt := ui.NewTester(a.binView, 1100, 700)
	tt.Frame()
	if !tt.HasText("Still here") {
		t.Fatalf("not listed: %v", tt.Texts())
	}
	if err := a.st.Close(); err != nil {
		t.Fatal(err)
	}
	a.loadBin()
	tt.Frame()
	if a.bin.readErr == nil {
		t.Fatal("the bin was read from a closed store")
	}
	if !tt.HasText("Still here") || tt.HasText(locales.BinNothing(binName())) {
		t.Errorf("after a failed read: %v", tt.Texts())
	}
	if !tt.HasText(locales.BinCouldNotRead(binName(), a.bin.readErr.Error())) {
		t.Errorf("no error in %v", tt.Texts())
	}

	a.bin.items = nil
	tt.Frame()
	if tt.HasText(locales.BinNothing(binName())) {
		t.Errorf("a bin that could not be read shows as empty: %v", tt.Texts())
	}
}

// TestTheTrashButtonClosesTheTrash clicks the sidebar's Trash button twice: the second click goes back to the page
// shown before, as Back would, and with nothing before, home.
func TestTheTrashButtonClosesTheTrash(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen = true
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plans"})
	a.reloadAll()
	a.route = Route{Kind: "page", ID: p.ID}
	a.loadPage(p.ID)
	a.router = ui.NewRouter(a.route.path())
	a.router.Transition = ui.TransitionNone
	a.routerAt = a.router.Path()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	clickBin := func() {
		t.Helper()
		b, ok := tt.AccessByRole(ui.RoleButton, binName())
		if !ok {
			t.Fatalf("no %s button", binName())
		}
		tt.ClickAt(b.Bounds.X+b.Bounds.W/2, b.Bounds.Y+b.Bounds.H/2)
		tt.Frame()
	}
	for _, want := range []Route{{Kind: "bin"}, {Kind: "page", ID: p.ID}, {Kind: "bin"}} {
		clickBin()
		if a.route != want {
			t.Fatalf("the Trash button went to %+v, want %+v", a.route, want)
		}
	}

	a.router = ui.NewRouter(a.route.path())
	a.routerAt = a.router.Path()
	clickBin()
	if a.route != (Route{Kind: "home"}) {
		t.Errorf("with nothing shown before the Trash, its button went to %+v", a.route)
	}
}
