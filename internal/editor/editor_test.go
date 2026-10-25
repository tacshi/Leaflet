package editor

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

type fakeHost struct {
	picked []MenuItem
	opened []string
}

func (h *fakeHost) PageTitle(id string) string                  { return "Sub" }
func (h *fakeHost) OpenPage(id string)                          { h.opened = append(h.opened, id) }
func (h *fakeHost) Image(url string) *ui.Bitmap                 { return nil }
func (h *fakeHost) Embed(c *ui.Context, n *doc.Node, w float32) {}
func (h *fakeHost) Recording(c *ui.Context, n *doc.Node)        {}
func (h *fakeHost) CommentCount(string) int                     { return 0 }
func (h *fakeHost) OpenComments(string)                         {}
func (h *fakeHost) SlashItems(q string) []MenuItem {
	return []MenuItem{{Key: "h1", Label: "Heading 1"}}
}
func (h *fakeHost) MentionItems(q string) []MenuItem { return nil }
func (h *fakeHost) Pick(t Trigger, item MenuItem)    { h.picked = append(h.picked, item) }
func (h *fakeHost) Comment(id, quote string)         {}
func (h *fakeHost) AskAgent(id, quote string)        {}
func (h *fakeHost) EditChart(id string)              {}

func setup(t *testing.T) (*Editor, *ui.Tester, *fakeHost) {
	t.Helper()
	host := &fakeHost{}
	e := New(doc.Empty(), host)
	e.SetWidth(500)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(20).Children(func() { e.View(c) })
	}, 600, 400)
	if err := tt.Click("Page"); err != nil {
		t.Fatal(err)
	}
	return e, tt, host
}

func texts(e *Editor) []string {
	var out []string
	for _, n := range e.units() {
		out = append(out, n.Type+":"+n.Delta().Plain())
	}
	return out
}

func TestTypingAndEnter(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Hello")
	tt.Key(0, ui.KeyEnter)
	tt.Type("world")
	got := texts(e)
	if len(got) != 2 || got[0] != "paragraph:Hello" || got[1] != "paragraph:world" {
		t.Fatalf("blocks %v", got)
	}
	for i := 0; i < 5; i++ {
		tt.Key(0, ui.KeyLeft)
	}
	tt.Key(0, ui.KeyBackspace)
	if got := texts(e); len(got) != 1 || got[0] != "paragraph:Helloworld" {
		t.Fatalf("after merge %v", got)
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	if got := texts(e); len(got) != 2 {
		t.Fatalf("undo merge %v", got)
	}
}

func TestMarkdownShortcuts(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("# Title")
	tt.Key(0, ui.KeyEnter)
	tt.Type("- one")
	tt.Key(0, ui.KeyEnter)
	tt.Type("two")
	tt.Key(0, ui.KeyTab)
	got := texts(e)
	want := []string{"heading:Title", "bulleted_list:one", "bulleted_list:two"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("blocks %v", got)
		}
	}
	one := e.Content.Children[1]
	if len(one.Children) != 1 || one.Children[0].Delta().Plain() != "two" {
		t.Errorf("tab did not nest: %+v", e.Content.Children)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter) // an empty item ends the nesting
	if n := e.node(e.focus.ID); e.depth(n.ID) != 0 {
		t.Errorf("empty item stayed nested")
	}
}

func TestInlineFormats(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship **v1** now")
	d := e.Content.Children[0].Delta()
	if d.Plain() != "ship v1 now" || len(d) != 3 || !d[1].Attrs.Bold {
		t.Fatalf("delta %+v", d)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyI)
	if !e.Content.Children[0].Delta().All(0, 11, func(a doc.Attrs) bool { return a.Italic }) {
		t.Errorf("not italic: %+v", e.Content.Children[0].Delta())
	}
}

func TestSlashMenu(t *testing.T) {
	e, tt, host := setup(t)
	tt.Type("/he")
	if e.Trigger() == nil || e.Trigger().Query != "he" {
		t.Fatalf("trigger %+v", e.Trigger())
	}
	tt.Key(0, ui.KeyEnter)
	if len(host.picked) != 1 || host.picked[0].Key != "h1" {
		t.Errorf("picked %+v", host.picked)
	}
}

func TestSelectionAcrossBlocksDeletes(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("alpha")
	tt.Key(0, ui.KeyEnter)
	tt.Type("beta")
	tt.Key(0, ui.KeyEnter)
	tt.Type("gamma")
	tt.Key(ui.Shift, ui.KeyUp)
	tt.Key(ui.Shift, ui.KeyUp)
	tt.Key(0, ui.KeyBackspace)
	got := texts(e)
	if len(got) != 1 {
		t.Fatalf("blocks %v", got)
	}
}

func TestSubpageCardOpensPage(t *testing.T) {
	e, tt, host := setup(t)
	tt.Type("Intro")
	e.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": "p1"}})
	tt.Frame()
	if err := tt.Click("Sub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(host.opened) != 1 || host.opened[0] != "p1" {
		t.Errorf("opened %v", host.opened)
	}
}

func TestClickImageThenDelete(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Intro")
	e.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "divider"})
	tt.Frame()
	var box ui.Rect
	for _, n := range e.units() {
		if n.Type == "divider" {
			box = e.geom[n.ID]
		}
	}
	tt.ClickAt(box.X+box.W/2, box.Y+box.H/2)
	tt.Key(0, ui.KeyBackspace)
	for _, n := range e.units() {
		if n.Type == "divider" {
			t.Fatalf("divider still there: %v", texts(e))
		}
	}
}

func TestDividerShortcut(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("---")
	tt.Type("after")
	got := texts(e)
	if len(got) != 2 || got[0] != "divider:" || got[1] != "paragraph:after" {
		t.Fatalf("got %q", got)
	}
	// Dashes inside text stay text.
	tt.Key(0, ui.KeyEnter)
	tt.Type("a---")
	if got := texts(e); got[len(got)-1] != "paragraph:a---" {
		t.Errorf("got %q", got)
	}
}

func TestPasteMarkdownKeepsNestingAndTables(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("- Day 1\n  - Kiyomizu-dera\n- Day 2\n\n| Item | Cost |\n| --- | --- |\n| Hotel | 800 |\nafter")
	top := e.Content.Children
	var types []string
	for _, n := range top {
		types = append(types, n.Type)
	}
	if len(top) < 4 || top[0].Type != "bulleted_list" || len(top[0].Children) != 1 || top[0].Children[0].Delta().Plain() != "Kiyomizu-dera" {
		t.Fatalf("lists %v %v", types, texts(e))
	}
	var table *doc.Node
	for _, n := range top {
		if n.Type == "table" {
			table = n
		}
	}
	if table == nil {
		t.Fatalf("no table: %v", types)
	}
	if rows := doc.TableRows(table); len(rows) != 2 || rows[1][0] != "Hotel" || rows[1][1] != "800" {
		t.Errorf("rows %v", rows)
	}
	if last := e.units()[len(e.units())-1]; last.Delta().Plain() != "after" {
		t.Errorf("last %v", texts(e))
	}
}

func TestCopyPasteKeepsBlocks(t *testing.T) {
	e, _, _ := setup(t)
	item := newText("bulleted_list", doc.FromMarkdown("Day 1"))
	item.Children = []*doc.Node{newText("bulleted_list", doc.FromMarkdown("Kiyomizu-dera"))}
	table, _ := doc.TableNode([][]string{{"Item", "Cost"}, {"Hotel", "800"}}, "")
	end := newText("paragraph", doc.FromMarkdown("end"))
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{item, table, end} })
	e.Select(Pos{item.ID, 0}, Pos{end.ID, 3})
	text := e.copyText()
	clip.text, clip.blocks = text, e.copyBlocks()
	// Paste into an empty page of its own.
	f := New(doc.Empty(), &fakeHost{})
	f.PlaceCaret(f.Content.Children[0].ID, 0)
	f.paste(text)
	top := f.Content.Children
	if len(top) < 3 || top[0].Type != "bulleted_list" || len(top[0].Children) != 1 || top[1].Type != "table" {
		t.Fatalf("got %v", texts(f))
	}
	if top[1].ID == table.ID || top[0].ID == item.ID {
		t.Error("pasted blocks kept the copied IDs")
	}
	if rows := doc.TableRows(top[1]); len(rows) != 2 || rows[1][1] != "800" {
		t.Errorf("rows %v", rows)
	}
}

func TestPasteLinesInTableCellJoin(t *testing.T) {
	e, _, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"a"}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	cell := table.Children[0].Children[0]
	e.PlaceCaret(cell.ID, 1)
	e.paste("one\ntwo")
	if got := cell.Delta().Plain(); got != "aone two" {
		t.Errorf("cell %q", got)
	}
	if len(e.Content.Children) != 1 {
		t.Errorf("blocks %v", texts(e))
	}
}

func TestNiceTicks(t *testing.T) {
	got := niceTicks(0, 1720, 3)
	if fmt.Sprint(got) != "[0 1000 2000]" {
		t.Errorf("ticks %v", got)
	}
	if got := niceTicks(0, 12, 3); fmt.Sprint(got) != "[0 5 10 15]" {
		t.Errorf("ticks %v", got)
	}
}
