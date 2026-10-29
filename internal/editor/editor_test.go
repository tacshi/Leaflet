package editor

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

type fakeHost struct {
	picked []MenuItem
	opened []string
	edited []string
	// imageFailed is whether images fail to load, rather than load forever.
	imageFailed bool
}

func (h *fakeHost) PageTitle(id string) string { return "Sub" }
func (h *fakeHost) OpenPage(id string)         { h.opened = append(h.opened, id) }
func (h *fakeHost) Image(n *doc.Node) (ui.ImageSource, *ui.VideoPlayer, bool) {
	return nil, nil, h.imageFailed
}
func (h *fakeHost) Embed(c *ui.Context, n *doc.Node, w float32) {}
func (h *fakeHost) Video(c *ui.Context, n *doc.Node, w float32) {}
func (h *fakeHost) Card(c *ui.Context, n *doc.Node)             {}
func (h *fakeHost) Recording(c *ui.Context, n *doc.Node)        {}
func (h *fakeHost) CommentCount(string) int                     { return 0 }
func (h *fakeHost) OpenComments(string)                         {}
func (h *fakeHost) CommentPreview(*ui.Context, string)          {}
func (h *fakeHost) SlashItems(q string) []MenuItem {
	return []MenuItem{{Key: "h1", Label: "Heading 1"}}
}
func (h *fakeHost) MentionItems(q string) []MenuItem { return nil }
func (h *fakeHost) Pick(t Trigger, item MenuItem)    { h.picked = append(h.picked, item) }
func (h *fakeHost) Comment(id, quote string)         {}
func (h *fakeHost) AskAgent(id, quote string)        {}
func (h *fakeHost) EditBlock(id string)              { h.edited = append(h.edited, id) }
func (h *fakeHost) OpenBlock(page, id string)        { h.opened = append(h.opened, page+"#"+id) }
func (h *fakeHost) BlockLink(id string) string       { return doc.BlockLink("this", id) }

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

func TestComposeCommits(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("a")
	for _, s := range []string{"v", "ve", "ve g", "ve ge"} {
		tt.Compose(s, len([]rune(s)))
	}
	tt.Type("这个")
	if got := texts(e); len(got) != 1 || got[0] != "paragraph:a这个" {
		t.Fatalf("committed %v", got)
	}
	tt.Type("b")
	if got := texts(e); got[0] != "paragraph:a这个b" {
		t.Fatalf("typed after %v", got)
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

func TestSelectionAcrossBlocksForAgent(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("alpha")
	tt.Key(0, ui.KeyEnter)
	tt.Type("**beta**")
	tt.Key(0, ui.KeyEnter)
	tt.Type("gamma")
	us := e.units()
	e.Select(Pos{us[0].ID, 2}, Pos{us[2].ID, 3})
	id, quote := e.SelectedText()
	if id != us[0].ID || quote != "pha\nbeta\ngam" {
		t.Fatalf("selected %q %q", id, quote)
	}
	if got := e.SelectedBlocks(); !slices.Equal(got, []string{us[0].ID, us[1].ID, us[2].ID}) {
		t.Fatalf("blocks %v", got)
	}
	// Up to the start of a block leaves that block out.
	e.Select(Pos{us[0].ID, 0}, Pos{us[2].ID, 0})
	if got := e.SelectedBlocks(); len(got) != 2 {
		t.Fatalf("blocks %v", got)
	}
	bold := func(a doc.Attrs) bool { return a.Bold }
	if e.Has(bold) {
		t.Fatal("bold across blocks with plain text")
	}
	e.Select(Pos{us[1].ID, 0}, Pos{us[2].ID, 0})
	if !e.Has(bold) {
		t.Fatal("not bold over the bold block")
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

// Charts are MyGo's: a bar chart's labels and values, a line chart's
// ticks, and a pie's shares.
func TestCharts(t *testing.T) {
	for _, kind := range []string{"bar", "line", "pie"} {
		e, tt, _ := setup(t)
		n, err := doc.SpecToNode(map[string]any{"type": "chart", "chart": map[string]any{
			"chart_type": kind, "title": "Sales", "labels": []any{"Q1", "Q2", "Q3"},
			"series": []any{map[string]any{"name": "2026", "values": []any{120.0, 340.0, 1720.0}}},
		}}, "", "Agent")
		if err != nil {
			t.Fatal(err)
		}
		e.InsertBlocks(n)
		tt.Frame()
		tt.Frame()
		want := map[string][]string{"bar": {"Sales", "Q1", "Q3", "1720"}, "line": {"Q2", "0", "1000", "2000"}, "pie": {"Sales", "Q1", "Q3"}}[kind]
		for _, s := range want {
			if !tt.HasText(s) {
				t.Errorf("%s chart has no %q: %v", kind, s, tt.Texts())
			}
		}
	}
}

func TestPasteVideoLink(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("https://youtu.be/dQw4w9WgXcQ")
	if top := e.Content.Children; len(top) != 2 || top[0].Type != "video" || top[0].Str("url") != "https://youtu.be/dQw4w9WgXcQ" || top[1].Type != "paragraph" {
		t.Fatalf("got %v", texts(e))
	}
	// On a line with text the link stays text.
	e.InsertText("see ")
	e.paste("https://youtu.be/dQw4w9WgXcQ")
	if got := texts(e); got[len(got)-1] != "paragraph:see https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("got %v", got)
	}
}

func TestPasteImageLink(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("https://example.com/a/cover.JPG?w=600")
	if top := e.Content.Children; len(top) != 2 || top[0].Type != "image" || top[0].Str("url") != "https://example.com/a/cover.JPG?w=600" || top[1].Type != "paragraph" {
		t.Fatalf("got %v", texts(e))
	}
	// A link to a page stays text.
	e.paste("https://example.com/a/cover")
	if got := texts(e); got[len(got)-1] != "paragraph:https://example.com/a/cover" {
		t.Errorf("got %v", got)
	}
}

func TestEmacsKeys(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Control keys act as Emacs only on macOS")
	}
	e, tt, _ := setup(t)
	tt.Type("Hello")
	tt.Key(ui.Ctrl, ui.KeyJ)
	tt.Type("world")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyF)
	tt.Key(ui.Ctrl, ui.KeyD)
	tt.Key(ui.Ctrl, ui.KeyB)
	tt.Key(ui.Ctrl, ui.KeyP)
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Key(ui.Ctrl, ui.KeyH)
	if got := texts(e); len(got) != 2 || got[0] != "paragraph:Hell" || got[1] != "paragraph:wrld" {
		t.Fatalf("after moving and deleting %v", got)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyF)
	tt.Key(ui.Ctrl, ui.KeyK)
	if got := texts(e); len(got) != 2 || got[0] != "paragraph:H" {
		t.Fatalf("after kill %v", got)
	}
	tt.Key(ui.Ctrl, ui.KeyK)
	if got := texts(e); len(got) != 1 || got[0] != "paragraph:Hwrld" {
		t.Fatalf("kill at the end joins %v", got)
	}
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Key(ui.Ctrl, ui.KeyY)
	tt.Key(ui.Ctrl, ui.KeyN)
	tt.Key(ui.Ctrl, ui.KeyL)
	if got := texts(e); len(got) != 1 || got[0] != "paragraph:Hwrldell" {
		t.Fatalf("after yank %v", got)
	}
}

func TestEmacsKeysInSlashMenu(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Control keys act as Emacs only on macOS")
	}
	_, tt, host := setup(t)
	tt.Type("/")
	tt.Key(ui.Ctrl, ui.KeyN)
	tt.Key(ui.Ctrl, ui.KeyP)
	tt.Key(0, ui.KeyEnter)
	if len(host.picked) != 1 || host.picked[0].Key != "h1" {
		t.Fatalf("picked %v", host.picked)
	}
}

// code makes the page an empty code block, with the caret in it, and
// the paragraph left after it.
func code(t *testing.T) (*Editor, *doc.Node) {
	t.Helper()
	e, _, _ := setup(t)
	e.SetType("code", 0)
	top := e.Content.Children
	if len(top) != 2 || top[0].Type != "code" || top[1].Type != "paragraph" || e.FocusBlock() != top[0].ID {
		t.Fatalf("got %v", texts(e))
	}
	return e, top[0]
}

func TestCodeTyping(t *testing.T) {
	e, n := code(t)
	e.InsertText("func f() {")
	e.Enter()
	e.InsertText("  **x** / @y")
	e.Enter()
	if got := n.Delta().Plain(); got != "func f() {\n  **x** / @y\n  " {
		t.Fatalf("code %q", got)
	}
	if len(n.Delta()) != 1 || !n.Delta()[0].Attrs.Zero() || e.Trigger() != nil {
		t.Errorf("code took formats or a menu: %+v", n.Delta())
	}
	// The second blank line at the end leaves the block, without them.
	e.Enter()
	e.Enter()
	if got := n.Delta().Plain(); got != "func f() {\n  **x** / @y" {
		t.Errorf("code %q", got)
	}
	if top := e.Content.Children; len(top) != 2 || top[1].Type != "paragraph" || e.FocusBlock() != top[1].ID {
		t.Errorf("after %v", texts(e))
	}
}

func TestCodeIndent(t *testing.T) {
	e, n := code(t)
	e.paste("a\nb\nc")
	if got := n.Delta().Plain(); got != "a\nb\nc" || len(e.Content.Children) != 2 {
		t.Fatalf("pasted %q %v", got, texts(e))
	}
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 3})
	e.indentCode(n, false)
	if got := n.Delta().Plain(); got != "  a\n  b\nc" {
		t.Errorf("indented %q", got)
	}
	if a, b := e.Range(); a.Off != 2 || b.Off != 7 {
		t.Errorf("selection %v %v", a, b)
	}
	e.indentCode(n, true)
	if got := n.Delta().Plain(); got != "a\nb\nc" {
		t.Errorf("outdented %q", got)
	}
	e.PlaceCaret(n.ID, 1)
	e.indentCode(n, false)
	if got := n.Delta().Plain(); got != "a  \nb\nc" {
		t.Errorf("tab at the caret %q", got)
	}
}

func TestFencedCode(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("Intro\n```go\nfunc a() {\n\treturn\n}\n```\nAfter")
	top := e.Content.Children
	if len(top) != 3 || top[1].Type != "code" || top[1].Str("language") != "go" || top[1].Delta().Plain() != "func a() {\n\treturn\n}" {
		t.Fatalf("got %v", texts(e))
	}
	e.Select(Pos{top[0].ID, 0}, Pos{top[2].ID, 5})
	if got := e.copyText(); got != "Intro\n```go\nfunc a() {\n\treturn\n}\n```\nAfter" {
		t.Errorf("copied %q", got)
	}
	// A fence typed in a paragraph and Enter start a code block.
	f, _, _ := setup(t)
	f.InsertText("```py")
	f.Enter()
	if top := f.Content.Children; len(top) != 2 || top[0].Type != "code" || top[0].Str("language") != "python" || top[0].Delta().Plain() != "" || top[1].Type != "paragraph" {
		t.Errorf("got %v", texts(f))
	}
	// One before a paragraph leaves none more.
	g, _, _ := setup(t)
	g.InsertText("After")
	g.PlaceCaret(g.Content.Children[0].ID, 0)
	g.Enter()
	g.PlaceCaret(g.Content.Children[0].ID, 0)
	g.InsertText("```go")
	g.Enter()
	if top := g.Content.Children; len(top) != 2 || top[0].Type != "code" || top[1].Delta().Plain() != "After" {
		t.Errorf("got %v", texts(g))
	}
}

func TestCodeCopyButton(t *testing.T) {
	e, tt, _ := setup(t)
	e.SetType("code", 0)
	n := e.Content.Children[0]
	e.InsertText("x := 1")
	tt.Frame()
	if err := tt.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "x := 1" {
		t.Errorf("clipboard %q", got)
	}
	// The press went to the button, not to the code: the caret stays.
	if p := e.Caret(); p.ID != n.ID || p.Off != 6 {
		t.Errorf("caret %v", p)
	}
	tt.Frame()
	if _, ok := tt.Find("Copied"); !ok {
		t.Error("the button does not say it copied")
	}
}

func TestCallout(t *testing.T) {
	e, _, _ := setup(t)
	e.SetType("callout", 0)
	n := e.Content.Children[0]
	e.InsertText("Note")
	if n.Type != "callout" || n.Str("tone") != "info" {
		t.Fatalf("got %+v", n.Data)
	}
	e.CycleTone(n.ID)
	e.CycleTone(n.ID)
	if n.Str("tone") != "warning" {
		t.Errorf("tone %q", n.Str("tone"))
	}
	// Enter leaves it for a paragraph; Backspace at its start ends it.
	e.Enter()
	if top := e.Content.Children; len(top) != 2 || top[1].Type != "paragraph" {
		t.Errorf("after Enter %v", texts(e))
	}
	e.PlaceCaret(n.ID, 0)
	e.Backspace(false)
	if n.Type != "paragraph" || n.Data["tone"] != nil {
		t.Errorf("after Backspace %+v", n.Data)
	}
	// A GitHub alert pasted is a callout.
	f, _, _ := setup(t)
	f.paste("> [!TIP]\n> Keep it short.\n> Then stop.\nAfter")
	if c := f.Content.Children[0]; c.Type != "callout" || c.Str("tone") != "tip" || c.Delta().Plain() != "Keep it short.\nThen stop." {
		t.Errorf("pasted %v", texts(f))
	}
}

func TestToggle(t *testing.T) {
	e, _, _ := setup(t)
	e.SetType("toggle_list", 0)
	tg := e.Content.Children[0]
	e.InsertText("Details")
	// Enter in an open toggle writes inside it.
	e.Enter()
	e.InsertText("Hidden")
	if len(tg.Children) != 1 || tg.Children[0].Delta().Plain() != "Hidden" {
		t.Fatalf("children %v", texts(e))
	}
	inside := tg.Children[0].ID
	// Folding it takes its children out of the caret's way, and moves the
	// caret out of them.
	e.ToggleCollapsed(tg.ID)
	if got := len(e.units()); got != 1 || e.Caret().ID != tg.ID {
		t.Errorf("units %d, caret %v", got, e.Caret())
	}
	// Enter in a folded toggle starts the next toggle.
	e.Enter()
	if top := e.Content.Children; len(top) != 2 || top[1].Type != "toggle_list" {
		t.Errorf("after Enter %v", texts(e))
	}
	// A paragraph after an open toggle nests in it with Tab.
	e.ToggleCollapsed(tg.ID)
	e.SetType("paragraph", 0)
	e.Indent()
	if len(tg.Children) != 2 || tg.Children[1].Type != "paragraph" || len(e.Content.Children) != 1 {
		t.Errorf("after Tab %v", texts(e))
	}
	// Copying the folded toggle takes its children too.
	e.ToggleCollapsed(tg.ID)
	end := newText("paragraph", nil)
	e.Edit(func(c *doc.Content) { c.Children = append(c.Children, end) })
	e.Select(Pos{tg.ID, 0}, Pos{end.ID, 0})
	if blocks := e.copyBlocks(); len(blocks) != 2 || len(blocks[0].Children) != 2 || blocks[0].Children[0].ID != inside {
		t.Errorf("copied %+v", blocks)
	}
}

func TestPasteLinksAndSources(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("https://go.dev/doc")
	n := e.Content.Children[0]
	if d := n.Delta(); len(d) != 1 || d[0].Attrs.Href != "https://go.dev/doc" {
		t.Errorf("pasted link %+v", d)
	}
	// On selected text a link links it.
	e.Edit(func(c *doc.Content) { n.SetDelta(doc.Delta{{Text: "the docs"}}) })
	e.Select(Pos{n.ID, 4}, Pos{n.ID, 8})
	e.paste("https://go.dev")
	if d := n.Delta(); n.Delta().Plain() != "the docs" || len(d) != 2 || d[1].Attrs.Href != "https://go.dev" {
		t.Errorf("linked %+v", d)
	}
	f, _, _ := setup(t)
	f.paste("```mermaid\ngraph TD\n  A-->B\n```\n$$\nx^2\n$$\nend")
	top := f.Content.Children
	if len(top) < 3 || top[0].Type != "diagram" || top[0].Str("source") != "graph TD\n  A-->B" || top[1].Type != "math_equation" || top[1].Str("formula") != "x^2" {
		t.Fatalf("pasted %v", texts(f))
	}
	// A Mermaid fence typed and Enter make a diagram, open to write.
	g, _, host := setup(t)
	g.InsertText("```mermaid")
	g.Enter()
	if top := g.Content.Children; top[0].Type != "diagram" || len(host.edited) != 1 || host.edited[0] != top[0].ID {
		t.Errorf("got %v, edited %v", texts(g), host.edited)
	}
}

func TestTableCellsTabAndEnter(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID", "Name"}, {"", ""}}, "")
	after := newText("paragraph", doc.FromMarkdown("after"))
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table, after} })
	tt.Frame()
	// Click the first body cell: it opens to its Markdown.
	box := e.geom[table.ID]
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	if e.cellEdit != cellKey(table.ID, 1, 0) {
		t.Fatalf("editing %q", e.cellEdit)
	}
	tt.Type("R1")
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Type("**Queue** edits")
	// Tab past the last cell adds a row and goes to its first cell.
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if rows := table.Int("rowsLen", 0); rows != 3 {
		t.Fatalf("rows %d", rows)
	}
	if e.cellEdit != cellKey(table.ID, 2, 0) {
		t.Fatalf("after Tab, editing %q", e.cellEdit)
	}
	rows := doc.TableRows(table)
	if rows[1][0] != "R1" || rows[1][1] != "**Queue** edits" {
		t.Errorf("rows %q", rows)
	}
	// Enter on the last row leaves the table for the block after it.
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if e.cellEdit != "" || e.Caret().ID != after.ID {
		t.Errorf("after Enter: editing %q, caret in %s", e.cellEdit, e.Caret().ID)
	}
}

// Markdown typed into a cell a key at a time stays as typed: a "[" or a
// "*" not closed yet is not shown escaped under the caret.
func TestTableCellKeepsMarkdownAsTyped(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID"}, {""}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := e.geom[table.ID]
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	for _, r := range "[Go](https://go.dev) and **this**" {
		tt.Type(string(r))
		tt.Frame()
	}
	if got := e.cellText; got != "[Go](https://go.dev) and **this**" {
		t.Errorf("input shows %q", got)
	}
	if rows := doc.TableRows(table); rows[1][0] != "[Go](https://go.dev) and **this**" {
		t.Errorf("cell %q", rows[1][0])
	}
	// Undo shows the cell as it is then.
	e.Undo()
	tt.Frame()
	if rows := doc.TableRows(e.Content.Children[0]); e.cellText != rows[1][0] {
		t.Errorf("after undo, input %q, cell %q", e.cellText, rows[1][0])
	}
}

func TestPasteMarkdownBlankLinesSeparate(t *testing.T) {
	nodes := parseLines("## Start\n\n> [!NOTE]\n> Needs Go\n\n1. one\n\n2. two\n\n```go\na := 1\n\nb := 2\n```\n")
	var types []string
	for _, n := range nodes {
		types = append(types, n.Type)
		if n.Type == "paragraph" && n.Delta().Len() == 0 {
			t.Errorf("an empty paragraph in %v", types)
		}
	}
	if got := strings.Join(types, " "); got != "heading callout numbered_list numbered_list code" {
		t.Fatalf("blocks %s", got)
	}
	if code := nodes[4].Delta().Plain(); code != "a := 1\n\nb := 2" {
		t.Errorf("code %q lost its blank line", code)
	}
}

func TestMarkdownBlocksReadsAReply(t *testing.T) {
	var types []string
	for _, n := range MarkdownBlocks("It says:\n\n> a quote\n\n- one\n") {
		types = append(types, n.Type)
	}
	if got := strings.Join(types, " "); got != "paragraph quote bulleted_list" {
		t.Errorf("blocks %q", got)
	}
}

// A page exported as Markdown reads back as the same blocks: text that
// looks like a block's marker stays text, and a | stays in its cell.
func TestMarkdownExportReadsBack(t *testing.T) {
	var blocks []*doc.Node
	for _, s := range []string{"```js", "# not a heading", "2*3*4", "- not an item", "1. not numbered", "> not a quote", "| not a table |", "$$", "---", "[!NOTE]"} {
		blocks = append(blocks, newText("paragraph", doc.Delta{{Text: s}}))
	}
	table, _ := doc.TableNode([][]string{{"a|b", `c\|d`}, {"`x|y`", "e"}}, "")
	blocks = append(blocks, table, newText("paragraph", doc.Delta{{Text: "last"}}))
	md := doc.PageMarkdown("", doc.FromBlocks(blocks), nil)
	got := MarkdownBlocks(md)
	if len(got) != len(blocks) {
		t.Fatalf("%q read back as %d blocks", md, len(got))
	}
	for i, n := range got {
		if n.Type != blocks[i].Type || !reflect.DeepEqual(n.Delta(), blocks[i].Delta()) {
			t.Errorf("block %d: %s %+v, want %s %+v", i, n.Type, n.Delta(), blocks[i].Type, blocks[i].Delta())
		}
	}
	if rows := doc.TableRows(got[len(got)-2]); !reflect.DeepEqual(rows, doc.TableRows(table)) {
		t.Errorf("table rows %q, want %q", rows, doc.TableRows(table))
	}
}

func TestTableRowSplitsOnUnescapedPipes(t *testing.T) {
	for line, want := range map[string][]string{
		"| a | b |":       {"a", "b"},
		`| a\|b | c |`:    {"a|b", "c"},
		`| a\\| b |`:      {`a\\`, "b"},
		`| \*x\* | y`:     {`\*x\*`, "y"},
		"| --- | :---: |": nil,
	} {
		if got := tableRow(line); !reflect.DeepEqual(got, want) {
			t.Errorf("%q split as %q, want %q", line, got, want)
		}
	}
}

func TestLeadingFollowsScript(t *testing.T) {
	for text, want := range map[string]float32{
		"":                    1.5,
		"Plain English prose": 1.5,
		"An English paragraph that quotes 模型 once":            1.5,
		"模型可以通过两种方式访问到这些 MCP 服务":                              1.8,
		"更完整的介绍可以参考官方文档 https://modelcontextprotocol.io/docs": 1.8,
		"ひらがなとカタカナ":                                           1.8,
		"한국어 문장":                                              1.8,
	} {
		if got := leading(text); got != want {
			t.Errorf("leading(%q) = %v, want %v", text, got, want)
		}
	}
}

// Right-clicking an image or a video from the web offers to change its
// link, an uploaded image to replace it; a block of text offers neither.
func TestEditMedia(t *testing.T) {
	e, tt, host := setup(t)
	img := &doc.Node{ID: "img", Type: "image", Data: map[string]any{"url": "https://example.com/a.png"}}
	video := &doc.Node{ID: "vid", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"}}
	upload := &doc.Node{ID: "up", Type: "image", Data: map[string]any{"url": "file:///x/a.png", "file_id": "f1"}}
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{para("p", "text"), img, video, upload} })
	tt.Frame()
	if _, ok := tt.Find("Loading image…"); !ok {
		t.Error("the image does not say it is loading")
	}
	for id, item := range map[string]string{"img": "Edit link", "vid": "Edit link", "up": "Replace image…"} {
		r := e.geom[id]
		tt.RightClickAt(r.X+20, r.Y+r.H/2)
		tt.Frame()
		if tt.HasText("Edit link") && tt.HasText("Replace image…") {
			t.Errorf("%s offers both: %v", id, tt.Texts())
		}
		if err := chooseDrawn(tt, item); err != nil {
			t.Fatalf("%s: %v; shown %v", id, err, tt.Texts())
		}
	}
	slices.Sort(host.edited)
	if !reflect.DeepEqual(host.edited, []string{"img", "up", "vid"}) {
		t.Errorf("edited %v", host.edited)
	}
	r := e.geom["p"]
	tt.RightClickAt(r.X+5, r.Y+r.H/2)
	tt.Frame()
	if !tt.HasText("Copy link to block") || tt.HasText("Edit link") || tt.HasText("Replace image…") {
		t.Errorf("a paragraph's menu: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	// An image that cannot load says so, rather than loading forever.
	host.imageFailed = true
	tt.Frame()
	if _, ok := tt.Find("Couldn't load image"); !ok {
		t.Error("the image does not say it failed")
	}
}

// A link to a video or picture shows as its block: in place of a line
// that is only the link, after a line with words of its own.
func TestLinkToMedia(t *testing.T) {
	e, _, _ := setup(t)
	clip := &doc.Node{ID: "clip", Type: "paragraph"}
	clip.SetDelta(doc.Delta{{Text: "🎬 "}, {Text: "视频", Attrs: doc.Attrs{Href: "https://example.com/a.mp4"}}})
	line := &doc.Node{ID: "line", Type: "paragraph"}
	line.SetDelta(doc.Delta{{Text: "See "}, {Text: "the ", Attrs: doc.Attrs{Href: "https://example.com/b.png"}}, {Text: "chart", Attrs: doc.Attrs{Href: "https://example.com/b.png", Bold: true}}, {Text: " here"}})
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{clip, line} })

	e.LinkToMedia(Pos{"line", 9})
	top := e.Content.Children
	if len(top) != 4 || top[1].ID != "line" || top[2].Type != "image" || top[2].Str("url") != "https://example.com/b.png" || top[2].Str("alt") != "the chart" || top[3].Type != "paragraph" {
		t.Fatalf("image: %v", top)
	}
	e.LinkToMedia(Pos{"clip", 3})
	top = e.Content.Children
	if top[0].Type != "video" || top[0].Str("url") != "https://example.com/a.mp4" || top[1].ID != "line" || e.Content.Find("clip") != nil {
		t.Fatalf("video: %v", top)
	}
	e.Undo()
	if e.Content.Find("clip") == nil || e.Content.Children[0].Type == "video" {
		t.Errorf("undo: %v", e.Content.Children)
	}
	// A link to a page is not media.
	e.LinkToMedia(Pos{"line", 0})
	if len(e.Content.Children) != 4 {
		t.Errorf("not media: %v", e.Content.Children)
	}
	if mediaLink("https://example.com/") != "" || mediaLink("https://youtu.be/dQw4w9WgXcQ") == "" || mediaLink("https://example.com/b.png") == "" {
		t.Error("menu items")
	}
}

// chooseDrawn chooses an item of the context menu MyGo draws in the
// window (PopoverMenu), which shows from the frame after the click.
func chooseDrawn(tt *ui.Tester, item string) error {
	tt.Frame()
	if err := tt.Click(item); err != nil {
		return err
	}
	tt.Frame()
	return nil
}

func TestFontsChangeLayout(t *testing.T) {
	t.Cleanup(func() { theme.SetFonts("", "", "", "") })
	e, tt, _ := setup(t)
	tt.Type("中文 and `code`")
	tt.Frame()
	n := e.units()[0]
	before := e.layoutOf(n)
	if before == nil || before.font.Family != theme.PageFont {
		t.Fatalf("layout before = %+v", before)
	}
	// Fonts every Mac has: one macOS downloads on demand, as Kaiti SC,
	// holds up the layout for good where it is not installed.
	theme.SetFonts("Georgia", "Hiragino Sans GB", "", "Menlo")
	tt.Frame()
	after := e.layoutOf(n)
	if after == before || after.font.Family != "Georgia, Hiragino Sans GB" {
		t.Fatalf("layout after = %+v, same %v", after.font, after == before)
	}
}

// A mention is one unit: the caret doesn't stop inside it, what is typed
// after it isn't part of it, and Backspace takes it out whole.
func TestMentionIsOneUnit(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hi @")
	tr := e.Trigger()
	if tr == nil {
		t.Fatal("no @ menu")
	}
	e.InsertMention(*tr, doc.Mention{Kind: "user", ID: "a", Name: "Alice"})
	n := e.Content.Children[0]
	e.PlaceCaret(n.ID, 5)
	if off := e.Caret().Off; off != 3 {
		t.Errorf("caret in the mention at %d", off)
	}
	tt.Key(0, ui.KeyRight)
	if off := e.Caret().Off; off != 9 {
		t.Errorf("right arrow to %d", off)
	}
	tt.Type("x")
	if d := n.Delta(); d.Plain() != "hi @Alicex " || d[1].Text != "@Alice" || d[2].Attrs.Mention != nil {
		t.Fatalf("typed after it: %+v", d)
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	if got := n.Delta().Plain(); got != "hi  " || e.Caret().Off != 3 {
		t.Errorf("after backspace %q, caret %d", got, e.Caret().Off)
	}
}

// In a toggle, Backspace at the start of a heading and Enter in an empty
// quote make them paragraphs where they are, and Shift-Tab takes a
// paragraph out after the toggle, leaving the blocks after it inside: a
// block other than a list never gets children.
func TestBlocksInAToggleBecomeParagraphs(t *testing.T) {
	e, tt, _ := setup(t)
	h, q, p, r := newText("heading", doc.Delta{{Text: "H"}}), newText("quote", nil), para("p", "P"), para("r", "R")
	h.Set("level", 2)
	tg := newText("toggle_list", doc.Delta{{Text: "T"}})
	tg.Children = []*doc.Node{h, q, p, r}
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{tg} })
	tt.Frame()
	e.PlaceCaret(h.ID, 0)
	tt.Key(0, ui.KeyBackspace)
	e.PlaceCaret(q.ID, 0)
	tt.Key(0, ui.KeyEnter)
	if h.Type != "paragraph" || q.Type != "paragraph" || len(tg.Children) != 4 {
		t.Fatalf("heading %s, quote %s, in the toggle %d", h.Type, q.Type, len(tg.Children))
	}
	e.PlaceCaret(p.ID, 0)
	tt.Key(ui.Shift, ui.KeyTab)
	if top := e.Content.Children; len(top) != 2 || top[1] != p || len(tg.Children) != 3 || tg.Children[2] != r {
		t.Errorf("after Shift-Tab %v", texts(e))
	}
	e.Content.Walk(func(n, _ *doc.Node) {
		if len(n.Children) > 0 && !NestableTypes(n) {
			t.Errorf("%s %q has children", n.Type, n.Delta().Plain())
		}
	})
}

// Typing joins the undo step before it only in the same block, and not
// once the caret has moved.
func TestUndoStepsBreakWhenTheCaretMoves(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ab")
	tt.Key(0, ui.KeyEnter)
	tt.Type("cd")
	b := e.Content.Children[1]
	tt.Key(0, ui.KeyUp)
	tt.Type("x")
	tt.Key(0, ui.KeyLeft)
	tt.Type("y")
	// Typing in another block without moving there with a key.
	e.PlaceCaret(b.ID, -1)
	tt.Type("z")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:abyx", "paragraph:cdz"}) {
		t.Fatalf("typed %v", got)
	}
	for _, want := range [][]string{
		{"paragraph:abyx", "paragraph:cd"},
		{"paragraph:abx", "paragraph:cd"},
		{"paragraph:ab", "paragraph:cd"},
	} {
		e.Undo()
		if got := texts(e); !slices.Equal(got, want) {
			t.Errorf("after undo %v, want %v", got, want)
		}
	}
}

// A frame with a selection over many blocks takes time in proportion to
// the blocks, not to their square: each block finds its place and depth
// without walking the page. Eight times the blocks take about eight times
// as long, and would take 64 times walking the page; the limit between
// leaves room for a busy machine's timing, as CI's.
func TestFrameWithSelectionIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("times frames")
	}
	frame := func(n int) time.Duration {
		var kids []*doc.Node
		for i := range n {
			kids = append(kids, newText("paragraph", doc.Delta{{Text: fmt.Sprintf("line %d with some words", i)}}))
		}
		e := New(doc.FromBlocks(kids), &fakeHost{})
		e.SetWidth(500)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Padding(20).Children(func() { e.View(c) })
		}, 600, 400)
		e.Select(Pos{kids[1].ID, 0}, Pos{kids[n-2].ID, 2})
		tt.Frame()
		best := time.Duration(math.MaxInt64)
		for range 5 {
			runtime.GC()
			start := time.Now()
			tt.Frame()
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := frame(1000), frame(8000)
	if large > 24*small {
		t.Errorf("a frame of 1000 blocks takes %v, of 8000 %v", small, large)
	}
}

// The / and @ menus open after Chinese or Japanese text, which puts no
// space before them, but not inside a word; a full-width space, as their
// input methods type, takes Markdown shortcuts.
func TestMenusAndShortcutsInCJK(t *testing.T) {
	for _, tc := range []struct {
		typed string
		opens bool
	}{
		{"我想/", true}, {"会議は@", true}, {"好的，@", true}, {"/", true},
		{"a@", false}, {"a/", false}, {"./", false},
	} {
		e, tt, _ := setup(t)
		tt.Type(tc.typed)
		if got := e.Trigger() != nil; got != tc.opens {
			t.Errorf("%q opens a menu: %v", tc.typed, got)
		}
	}
	e, tt, _ := setup(t)
	tt.Type("#　标题")
	if got := texts(e); got[0] != "heading:标题" {
		t.Errorf("full-width space after # made %v", got)
	}
}

// Copied text keeps its formats, within one block too, and goes on the
// clipboard as HTML for other apps; HTML from another app pastes as blocks
// in its formats, but HTML without any as its text, which reads as
// Markdown.
func TestCopyAndPasteFormats(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship **v1** now")
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 2}, Pos{n.ID, 7})
	tt.Command("copy")
	html, err := tt.ClipboardData().Read(transfer.HTML)
	if err != nil || !strings.Contains(string(html), "<b>v1</b>") {
		t.Errorf("copied HTML %q, %v", html, err)
	}
	e.PlaceCaret(n.ID, -1)
	tt.Command("paste")
	if d := n.Delta(); d.Plain() != "ship v1 nowip v1" || !d[len(d)-1].Attrs.Bold {
		t.Errorf("pasted %+v", d)
	}

	paste := func(html, text string) []*doc.Node {
		e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{newText("paragraph", nil)} })
		e.PlaceCaret(e.Content.Children[0].ID, 0)
		tt.SetClipboardData(transfer.New(transfer.NewItem(
			transfer.Bytes(transfer.HTML, []byte(html)), transfer.Bytes(transfer.Text, []byte(text)))))
		tt.Command("paste")
		return e.Content.Children
	}
	got := paste("<h2>Plan</h2><ul><li><b>Ship</b> it<ul><li>soon</li></ul></li></ul><pre>a\nb</pre>", "Plan\nShip it\nsoon\na\nb")
	// Somewhere to go on writing after the code.
	if len(got) != 4 || got[0].Type != "heading" || got[0].Int("level", 0) != 2 ||
		got[1].Type != "bulleted_list" || !got[1].Delta()[0].Attrs.Bold || len(got[1].Children) != 1 ||
		got[2].Type != "code" || got[2].Delta().Plain() != "a\nb" {
		t.Errorf("pasted HTML as %v", texts(e))
	}
	got = paste("<div>## Title</div><div>- item</div>", "## Title\n- item")
	if len(got) != 2 || got[0].Type != "heading" || got[1].Type != "bulleted_list" {
		t.Errorf("pasted plain HTML as %v", texts(e))
	}
}

func TestCaretIsAsTallAsItsText(t *testing.T) {
	font := ui.Font{Size: 16, LineHeight: 1.8}
	l := ui.ShapeRichText([]ui.Span{{Text: "也就是「波形」：", Size: font.Size}}, font, 400)
	line, cr := l.Lines()[0], textCaret(l, 3)
	if cr.H >= line.Rect.H || cr.Y <= line.Rect.Y || cr.Y+cr.H >= line.Rect.Y+line.Rect.H {
		t.Errorf("caret %+v fills its line %+v", cr, line.Rect)
	}
	if cr.H < font.Size {
		t.Errorf("caret %+v shorter than the text, %v", cr, font.Size)
	}
}
