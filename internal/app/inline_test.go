package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/recording"
	"leaflet/internal/store"
)

// As an agent asked inline writes on past the window, the page stays
// where the user has it, for them to scroll: neither what it writes nor
// the caret, left where the user asked, moves it.
func TestInlineRunLeavesTheScrollToTheUser(t *testing.T) {
	a := templatesApp(t)
	a.rec = recording.NewController(a.st, a.backend)
	a.hub = agents.NewHub(a.st, a.backend, a)
	var ids []string
	var nodes []*doc.Node
	for i := range 20 {
		n := &doc.Node{ID: fmt.Sprintf("b%d", i), Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: fmt.Sprintf("Paragraph %d of the story so far.", i)}})
		nodes, ids = append(nodes, n), append(ids, n.ID)
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Story", Content: doc.FromBlocks(nodes)})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1300, 800)
	// The caret stays where the user asked, in the first block.
	a.page.editor.PlaceCaret("b0", 0)
	a.page.editor.Focus()
	tt.Frame()
	tt.Frame()
	run := &agents.Run{ID: "r1", Status: "running", Edits: []agents.Edit{{Summary: "Wrote", BlockIDs: ids}}}
	a.hub.Session(p.ID).Runs = append(a.hub.Session(p.ID).Runs, run)
	a.dialogs.inline = &inlinePrompt{blockID: "b0", from: editor.Pos{ID: "b0"}, to: editor.Pos{ID: "b0"}, working: true, runID: "r1"}
	live := a.Live(p.ID)
	for i := range 40 {
		n := &doc.Node{ID: fmt.Sprintf("n%d", i), Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: fmt.Sprintf("New paragraph %d, as the agent writes on.", i)}})
		if err := live.ApplyOp(doc.InsertOp(doc.Position{After: ids[len(ids)-1]}, n), false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.ID)
		run.Edits[0].BlockIDs = append([]string(nil), ids...)
		tt.Frame()
	}
	tt.Frame()
	if a.page.scroll.Y != 0 {
		t.Errorf("the page scrolled to %v as the agent wrote", a.page.scroll.Y)
	}
	// The user scrolls down to read on; the page stays there.
	a.page.scroll.Y = 400
	tt.Frame()
	tt.Frame()
	if a.page.scroll.Y != 400 {
		t.Errorf("the page went to %v from where the user scrolled it", a.page.scroll.Y)
	}
}
