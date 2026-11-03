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
	"leaflet/locales"
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

// An agent's reply in words goes into the page after the block it was
// asked about, its Markdown read as blocks, or at the end once that block
// has gone.
func TestInsertReply(t *testing.T) {
	a := templatesApp(t)
	first, second := textNode("First"), textNode("Second")
	first.ID, second.ID = "b1", "b2"
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{first, second})})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	a.insertReply("b1", "**Note**\n\n- one\n- two")
	var got []string
	for _, n := range a.page.editor.Content.Children {
		got = append(got, n.Type+":"+n.Delta().Plain())
	}
	if want := "[paragraph:First paragraph:Note bulleted_list:one bulleted_list:two paragraph:Second]"; fmt.Sprint(got) != want {
		t.Errorf("blocks %v, want %s", got, want)
	}
	a.insertReply("gone", "Last")
	ch := a.page.editor.Content.Children
	if last := ch[len(ch)-1]; last.Delta().Plain() != "Last" {
		t.Errorf("the reply after a block gone went before %q", last.Delta().Plain())
	}
}

// Asking inline sends nothing empty. Asked with no agent to run it, the
// prompt stops working and says why, staying open for that to be read.
func TestSendInline(t *testing.T) {
	a := templatesApp(t)
	a.hub = agents.NewHub(a.st, a.backend, a)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	ip := &inlinePrompt{blockID: "b1", blocks: []string{"b1", "b2"}, quote: "Hello", selection: true, text: "  "}
	a.dialogs.inline = ip
	a.sendInline(ip)
	if ip.working || ip.runID != "" || ip.reply != "" {
		t.Fatalf("an empty prompt was sent: %+v", ip)
	}
	if got := ip.focus(); fmt.Sprint(got) != "[b1 b2]" {
		t.Errorf("focus %v", got)
	}
	ip.text = "Make it shorter"
	a.sendInline(ip)
	if ip.working || ip.runID == "" || ip.reply != locales.AgentNoProvider() {
		t.Errorf("with no agent: working %v, run %q, reply %q", ip.working, ip.runID, ip.reply)
	}
	if a.dialogs.inline != ip {
		t.Error("the prompt closed without its reply read")
	}
}
