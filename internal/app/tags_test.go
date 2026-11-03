package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/recording"
	"leaflet/internal/store"
)

// The byline's tag button edits the page's tags in a token field, which
// saves each one added; closed, the byline shows them, and a click on one
// shows its pages in the sidebar.
func TestBylineTags(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	a.settings.SidebarOpen, a.settings.TextSize = true, 16
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan"})
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	if err := tt.Click("Add tag"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	tt.Type("work")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"work"}) {
		t.Fatalf("tags %q", got.Tags)
	}
	// Escape closes the suggestions first, as in MyGo's comboboxes, then
	// the field.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	a.reloadAll()
	tt.Frame()
	if a.page.tagsEditing {
		t.Fatal("Escape left the tags field open")
	}
	if err := tt.Click("work"); err != nil {
		t.Fatal(err)
	}
	if a.settings.SidebarTab != tabTags || !a.tagTree.Open.Has("tag:work") {
		t.Errorf("a click on the tag showed tab %d, open %v", a.settings.SidebarTab, a.tagTree.Open.Has("tag:work"))
	}
}
