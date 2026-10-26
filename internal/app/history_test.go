package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// A named version shows in the page's history, marked, and Changes since
// shows what was edited after it.
func TestNamedVersionAndChanges(t *testing.T) {
	a := templatesApp(t)
	para := &doc.Node{ID: "p1", Type: "paragraph"}
	para.SetDelta(doc.Delta{{Text: "Ship v1 to beta users"}})
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{para})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openHistory(p.ID)
	a.nameVersion("Sent to legal")
	h := a.dialogs.history
	if len(h) != 1 || h[0].Reason != "manual" || historyLabel(h[0]) != "Sent to legal" {
		t.Fatalf("history %+v", h)
	}
	// Edit the page after it.
	c := a.st.Content(p.ID)
	c.Find("p1").SetDelta(doc.Delta{{Text: "Ship v2 to all beta users"}})
	if _, _, err := a.st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	a.dialogs.historyView = 1
	tt := ui.NewTester(func(c *ui.Context) { a.historyDialog(c) }, 1000, 700)
	tt.Frame()
	if !tt.HasText("Sent to legal") {
		t.Error("the named version is not listed")
	}
	added, deleted := a.dialogs.historyDiff.Stats()
	if added != 1 || deleted != 1 {
		t.Errorf("stats +%d -%d", added, deleted)
	}
	if !tt.HasText("+1") || !tt.HasText("−1") {
		t.Errorf("no counts in %v", tt.Texts())
	}
	// Side by side shows the two versions in columns.
	if err := tt.Click("Side by side"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dialogs.historyDiff.Layout != codeeditor.DiffSplit {
		t.Error("not side by side")
	}
}
