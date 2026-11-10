package editor

import (
	"fmt"
	"math"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/richtext"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

type fakeHost struct {
	picked      []MenuItem
	opened      []string
	edited      []string
	imageFailed bool
	follows     []follow
	slash       []MenuItem
}

func (h *fakeHost) PageTitle(id string) string { return "Sub" }
func (h *fakeHost) OpenPage(id string)         { h.opened = append(h.opened, id) }
func (h *fakeHost) Image(n *doc.Node) (ui.ImageSource, *ui.VideoPlayer, bool) {
	return nil, nil, h.imageFailed
}
func (h *fakeHost) Embed(c *ui.Context, n *doc.Node, w float32) {}
func (h *fakeHost) Video(c *ui.Context, n *doc.Node, w float32) {}
func (h *fakeHost) Card(c *ui.Context, n *doc.Node)             {}
func (h *fakeHost) File(c *ui.Context, n *doc.Node, w float32)  {}
func (h *fakeHost) Recording(c *ui.Context, n *doc.Node)        {}
func (h *fakeHost) CommentCount(string) int                     { return 0 }
func (h *fakeHost) OpenComments(string)                         {}
func (h *fakeHost) CommentPreview(*ui.Context, string)          {}
func (h *fakeHost) SlashItems(q string) []MenuItem {
	if h.slash != nil {
		return h.slash
	}
	return []MenuItem{{Key: "h1", Label: "Heading 1"}}
}
func (h *fakeHost) MentionItems(q string) []MenuItem  { return nil }
func (h *fakeHost) Pick(t Trigger, item MenuItem)     { h.picked = append(h.picked, item) }
func (h *fakeHost) Comment(id, quote string)          {}
func (h *fakeHost) AskAgent(id, quote string)         {}
func (h *fakeHost) EditBlock(id string)               { h.edited = append(h.edited, id) }
func (h *fakeHost) VideoMenu(m *ui.Menu, n *doc.Node) { m.Item("Video item for " + n.ID) }
func (h *fakeHost) FileMenu(_ *ui.Context, m *ui.Menu, n *doc.Node) {
	m.Item("File item for " + n.ID)
}
func (h *fakeHost) OpenBlock(page, id string, at int) {
	if at >= 0 {
		id += "@" + strconv.Itoa(at)
	}
	h.opened = append(h.opened, page+"#"+id)
}
func (h *fakeHost) BlockLink(id string) string { return doc.BlockLink("this", id) }

func (h *fakeHost) FollowVideoLink(link string, done func(string)) {
	h.follows = append(h.follows, follow{link, done})
}

type follow struct {
	link string
	done func(string)
}

func setup(t *testing.T) (*Editor, *ui.Tester, *fakeHost) {
	t.Helper()
	freshClipboard(t)
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

func freshClipboard(t *testing.T) {
	empty := func() {
		clip.text, clip.blocks, clip.delta, clip.token = "", nil, nil, ""
		killed = ""
	}
	empty()
	t.Cleanup(empty)
}

func boxOf(e *Editor, id string) ui.Rect {
	r, _ := e.box(id)
	return r
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
	tt.Key(0, ui.KeyEnter)
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

func longMenu(t *testing.T) (*Editor, *ui.Tester) {
	t.Helper()
	e, tt, host := setup(t)
	tt.SetSize(900, 900)
	for i := range 20 {
		group := "First"
		if i >= 12 {
			group = "Second"
		}
		host.slash = append(host.slash, MenuItem{Key: strconv.Itoa(i), Label: fmt.Sprintf("Item %d", i), Hint: "Hint", Group: group})
	}
	tt.Type("/")
	return e, tt
}

func TestMenuKeysShowTheChoice(t *testing.T) {
	e, tt := longMenu(t)
	top, _ := tt.Find("Item 0")
	for range 15 {
		tt.Key(0, ui.KeyDown)
		r, ok := tt.Find(fmt.Sprintf("Item %d", e.menuIndex))
		if !ok || r.H == 0 || r.Y < top.Y-20 || r.Y+r.H > top.Y+320 {
			t.Errorf("item %d at %+v, %v, under a menu from %v", e.menuIndex, r, ok, top.Y)
		}
	}
}

func TestMenuScrollsByWheel(t *testing.T) {
	e, tt := longMenu(t)
	was, _ := tt.Find("Item 2")
	tt.Scroll(was.X+10, was.Y, 0, 200)
	tt.Frame()
	if now, _ := tt.Find("Item 2"); now.H != 0 && now.Y > was.Y-150 {
		t.Errorf("Item 2 from %+v to %+v: the menu went back to the choice", was, now)
	}
	if e.menuIndex != 0 {
		t.Errorf("the wheel chose %d", e.menuIndex)
	}
	tt.Key(0, ui.KeyDown)
	if r, _ := tt.Find("Item 1"); r.H != was.H {
		t.Errorf("Item 1 at %+v: the keys left the choice out of view", r)
	}
}

func TestSpaceAfterTriggerClosesTheMenu(t *testing.T) {
	for _, typed := range []string{"ratio 1 / 2", "yes / no", "meet @ home"} {
		e, tt, host := setup(t)
		tt.Type(typed)
		if e.Trigger() != nil {
			t.Errorf("%q: trigger %+v", typed, e.Trigger())
		}
		tt.Key(0, ui.KeyEnter)
		if len(host.picked) != 0 {
			t.Errorf("%q: picked %+v", typed, host.picked)
		}
		if got := texts(e); len(got) != 2 || got[0] != "paragraph:"+typed {
			t.Errorf("%q: blocks %v", typed, got)
		}
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
	if len(got) != 1 || got[0] != "paragraph:alpha" {
		t.Fatalf("blocks %v", got)
	}
	if p := e.Caret(); p.ID != e.Content.Children[0].ID || p.Off != len("alpha") || !e.Collapsed() {
		t.Errorf("caret %v, collapsed %v", p, e.Collapsed())
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
			box = boxOf(e, n.ID)
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

func TestPasteLinesInTableCell(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"head"}, {"cell"}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	if err := tt.Click("cell"); err != nil {
		t.Fatalf("%v in %v", err, tt.Texts())
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.SetClipboard("one\ntwo | three")
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()
	if rows := doc.TableRows(table); len(rows) != 2 || rows[1][0] != "one\ntwo | three" {
		t.Fatalf("rows %q", rows)
	}
	if len(e.Content.Children) != 1 {
		t.Errorf("blocks %v", texts(e))
	}
	md := doc.PageMarkdown("", e.Content, doc.MarkdownOptions{})
	if !strings.Contains(md, "| one<br>two \\| three |") {
		t.Errorf("markdown %q", md)
	}
	if back := doc.MarkdownBlocks(md); len(back) != 1 || !slices.Equal(doc.TableRows(back[0])[1], []string{"one\ntwo | three"}) {
		t.Errorf("read back as %v", back)
	}
}

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
	e.InsertText("see ")
	e.paste("https://youtu.be/dQw4w9WgXcQ")
	if got := texts(e); got[len(got)-1] != "paragraph:see https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("got %v", got)
	}
}

func TestPasteShareLink(t *testing.T) {
	e, _, h := setup(t)
	const share, video = "https://b23.tv/xuajGqj", "https://www.bilibili.com/video/BV1bY4G6pEEg"
	e.paste(share)
	if got := texts(e); len(h.follows) != 1 || h.follows[0].link != share || got[len(got)-1] != "paragraph:"+share {
		t.Fatalf("pasted %v, following %v", got, h.follows)
	}
	h.follows[0].done(video)
	top := e.Content.Children
	if len(top) != 2 || top[0].Type != "video" || top[0].Str("url") != video || top[1].Type != "paragraph" || e.focus.ID != top[1].ID {
		t.Fatalf("got %v, caret in %s", texts(e), e.focus.ID)
	}
	e.Undo()
	if got := texts(e); got[len(got)-1] != "paragraph:"+share {
		t.Errorf("undone to %v", got)
	}

	e.Enter()
	e.paste(share)
	e.InsertText(" later")
	h.follows[1].done(video)
	if got := texts(e); got[len(got)-1] != "paragraph:"+share+" later" {
		t.Errorf("a changed line became %v", got)
	}
	e.Enter()
	e.paste(share)
	h.follows[2].done(share)
	if got := texts(e); got[len(got)-1] != "paragraph:"+share {
		t.Errorf("a share of no video became %v", got)
	}
}

func TestPasteImageLink(t *testing.T) {
	e, _, _ := setup(t)
	e.paste("https://example.com/a/cover.JPG?w=600")
	if top := e.Content.Children; len(top) != 2 || top[0].Type != "image" || top[0].Str("url") != "https://example.com/a/cover.JPG?w=600" || top[1].Type != "paragraph" {
		t.Fatalf("got %v", texts(e))
	}
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

func TestCtrlKeysOffTheMac(t *testing.T) {
	e, tt, _ := setup(t)
	press := func(mods ui.Modifiers, key ui.Key) {
		if runtime.GOOS == "darwin" {
			e.key(ctrlKeys(ui.InputEvent{Kind: ui.InputKeyDown, Key: key, Mods: mods}))
			tt.Frame()
			return
		}
		tt.Key(mods, key)
	}
	tt.Type("first")
	tt.Key(0, ui.KeyEnter)
	tt.Type("one two three")
	press(ui.Ctrl, ui.KeyBackspace)
	if got := texts(e); len(got) != 2 || got[1] != "paragraph:one two " {
		t.Fatalf("after Ctrl+Backspace %v", got)
	}
	press(ui.Ctrl, ui.KeyLeft)
	if p := e.Caret(); p.Off != len("one ") {
		t.Errorf("after Ctrl+Left the caret is at %d", p.Off)
	}
	press(ui.Ctrl, ui.KeyHome)
	if p := e.Caret(); p.ID != e.Content.Children[0].ID || p.Off != 0 {
		t.Errorf("after Ctrl+Home the caret is at %v", p)
	}
	press(ui.Ctrl|ui.Shift, ui.KeyEnd)
	if p := e.Caret(); p.ID != e.Content.Children[1].ID || p.Off != len("one two ") || e.Collapsed() {
		t.Errorf("after Ctrl+Shift+End the caret is at %v, collapsed %v", p, e.Collapsed())
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
	if got := e.copyText(); got != "Intro\n\n```go\nfunc a() {\n\treturn\n}\n```\n\nAfter" {
		t.Errorf("copied %q", got)
	}
	f, _, _ := setup(t)
	f.InsertText("```py")
	f.Enter()
	if top := f.Content.Children; len(top) != 2 || top[0].Type != "code" || top[0].Str("language") != "python" || top[0].Delta().Plain() != "" || top[1].Type != "paragraph" {
		t.Errorf("got %v", texts(f))
	}
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
	e.Enter()
	if top := e.Content.Children; len(top) != 2 || top[1].Type != "paragraph" {
		t.Errorf("after Enter %v", texts(e))
	}
	e.PlaceCaret(n.ID, 0)
	e.Backspace(false)
	if n.Type != "paragraph" || n.Data["tone"] != nil {
		t.Errorf("after Backspace %+v", n.Data)
	}
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
	e.Enter()
	e.InsertText("Hidden")
	if len(tg.Children) != 1 || tg.Children[0].Delta().Plain() != "Hidden" {
		t.Fatalf("children %v", texts(e))
	}
	inside := tg.Children[0].ID
	e.ToggleCollapsed(tg.ID)
	if got := len(e.units()); got != 1 || e.Caret().ID != tg.ID {
		t.Errorf("units %d, caret %v", got, e.Caret())
	}
	e.Enter()
	if top := e.Content.Children; len(top) != 2 || top[1].Type != "toggle_list" {
		t.Errorf("after Enter %v", texts(e))
	}
	e.ToggleCollapsed(tg.ID)
	e.SetType("paragraph", 0)
	e.Indent()
	if len(tg.Children) != 2 || tg.Children[1].Type != "paragraph" || len(e.Content.Children) != 1 {
		t.Errorf("after Tab %v", texts(e))
	}
	e.ToggleCollapsed(tg.ID)
	start, end := newText("paragraph", nil), newText("paragraph", nil)
	e.Edit(func(c *doc.Content) { c.Children = append([]*doc.Node{start}, append(c.Children, end)...) })
	e.Select(Pos{start.ID, 0}, Pos{end.ID, 0})
	if blocks := e.copyBlocks(); len(blocks) != 3 || len(blocks[1].Children) != 2 || blocks[1].Children[0].ID != inside {
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
	box := boxOf(e, table.ID)
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	if e.cellEdit != cellKey(table.ID, 1, 0) {
		t.Fatalf("editing %q", e.cellEdit)
	}
	tt.Type("R1")
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Type("**Queue** edits")
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
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if e.cellEdit != "" || e.Caret().ID != after.ID {
		t.Errorf("after Enter: editing %q, caret in %s", e.cellEdit, e.Caret().ID)
	}
}

func TestTableWithAHugeSize(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID", "Name"}, {"", ""}}, "")
	table.Set("colsLen", 1_000_000_000)
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := boxOf(e, table.ID)
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if e.cellEdit != cellKey(table.ID, 2, 0) || len(table.Children) != 6 {
		t.Fatalf("editing %q, %d cells", e.cellEdit, len(table.Children))
	}
	e.Edit(func(*doc.Content) { insertTableColumn(table, 2) })
	if rows, cols := doc.TableSize(table); rows != 3 || cols != 3 || len(table.Children) != 9 {
		t.Errorf("after a column, %d by %d, %d cells", rows, cols, len(table.Children))
	}
}

func TestTableCellKeepsMarkdownAsTyped(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID"}, {""}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := boxOf(e, table.ID)
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
	e.Undo()
	tt.Frame()
	if rows := doc.TableRows(e.Content.Children[0]); e.cellText != rows[1][0] {
		t.Errorf("after undo, input %q, cell %q", e.cellText, rows[1][0])
	}
}

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
		r := boxOf(e, id)
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
	r := boxOf(e, "p")
	tt.RightClickAt(r.X+5, r.Y+r.H/2)
	tt.Frame()
	if !tt.HasText("Copy link to block") || tt.HasText("Edit link") || tt.HasText("Replace image…") {
		t.Errorf("a paragraph's menu: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	host.imageFailed = true
	tt.Frame()
	if _, ok := tt.Find("Couldn't load image"); !ok {
		t.Error("the image does not say it failed")
	}
}

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
	e.LinkToMedia(Pos{"line", 0})
	if len(e.Content.Children) != 4 {
		t.Errorf("not media: %v", e.Content.Children)
	}
	if mediaLink("https://example.com/") != "" || mediaLink("https://youtu.be/dQw4w9WgXcQ") == "" || mediaLink("https://example.com/b.png") == "" {
		t.Error("menu items")
	}
}

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
	theme.SetFonts("Georgia", "Hiragino Sans GB", "", "Menlo")
	tt.Frame()
	after := e.layoutOf(n)
	if after == before || after.font.Family != "Georgia, Hiragino Sans GB" {
		t.Fatalf("layout after = %+v, same %v", after.font, after == before)
	}
}

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
		if len(n.Children) > 0 && !nestable(n) {
			t.Errorf("%s %q has children", n.Type, n.Delta().Plain())
		}
	})
}

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
			for runs := 1; ; runs++ {
				tt.Frame()
				if took := time.Since(start); took >= 200*time.Millisecond && runs >= 4 {
					best = min(best, took/time.Duration(runs))
					break
				}
			}
		}
		return best
	}
	// A busy machine can slow one of the two: a frame that is linear shows it in one of a few tries.
	var small, large time.Duration
	for range 3 {
		if small, large = frame(1000), frame(8000); large <= 24*small {
			return
		}
	}
	t.Errorf("a frame of 1000 blocks takes %v, of 8000 %v", small, large)
}

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

func TestPasteKeepsColorsOfLeafletsRichText(t *testing.T) {
	e, tt, _ := setup(t)
	n := newText("paragraph", doc.Delta{{Text: "red", Attrs: doc.Attrs{Color: "#ef4444"}}, {Text: " and "},
		{Text: "marked", Attrs: doc.Attrs{Background: "#eab30840", Bold: true}}})
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{n} })
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 14})
	tt.Command("copy")
	rich, err := tt.ClipboardData().Read(richtext.Format)
	if err != nil {
		t.Fatal(err)
	}
	paste := func(format transfer.Format, data string) doc.Delta {
		end := newText("paragraph", nil)
		e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{end} })
		e.PlaceCaret(end.ID, 0)
		tt.SetClipboardData(transfer.New(transfer.NewItem(
			transfer.Bytes(format, []byte(data)), transfer.Bytes(transfer.Text, []byte("red and marked")))))
		tt.Command("paste")
		return e.Content.Children[0].Delta()
	}
	if got := paste(richtext.Format, string(rich)); !reflect.DeepEqual(got, n.Delta()) {
		t.Errorf("pasted %+v, want %+v", got, n.Delta())
	}
	got := paste(transfer.HTML, `<p><span style="color:#ef4444">red</span> and <b style="background-color:#eab30840">marked</b></p>`)
	if want := (doc.Delta{{Text: "red and "}, {Text: "marked", Attrs: doc.Attrs{Bold: true}}}); !reflect.DeepEqual(got, want) {
		t.Errorf("pasted HTML as %+v, want %+v", got, want)
	}
}

func TestCopyPlainText(t *testing.T) {
	e, tt, _ := setup(t)
	item := newText("bulleted_list", doc.FromMarkdown("Day **1**"))
	item.Children = []*doc.Node{newText("todo_list", doc.FromMarkdown("Kiyomizu-dera"))}
	table, _ := doc.TableNode([][]string{{"Item", "Cost"}, {"Hotel", "800"}}, "")
	end := newText("paragraph", doc.FromMarkdown("*end* here"))
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{item, table, end} })
	e.Select(Pos{item.ID, 4}, Pos{end.ID, 3})
	e.CopyPlainText()
	data := tt.ClipboardData()
	if formats := data.Formats(); len(formats) != 1 || formats[0] != transfer.Text {
		t.Errorf("copied as %v", formats)
	}
	if got, want := tt.Clipboard(), "1\n\tKiyomizu-dera\nItem\tCost\nHotel\t800\nend"; got != want {
		t.Errorf("copied %q, want %q", got, want)
	}

	e.Select(Pos{end.ID, 0}, Pos{end.ID, 8})
	e.CopyPlainText()
	if got := tt.Clipboard(); got != "end here" {
		t.Errorf("copied %q", got)
	}
	e.PlaceCaret(end.ID, -1)
	tt.Command("paste")
	if d := end.Delta(); d.Plain() != "end hereend here" || d[len(d)-1].Attrs.Italic {
		t.Errorf("pasted %+v", d)
	}

	tt.SetClipboard("before")
	e.CopyPlainText()
	if got := tt.Clipboard(); got != "before" {
		t.Errorf("copied %q at the caret", got)
	}
}

func TestCaretIsAsTallAsItsText(t *testing.T) {
	font := ui.Font{Size: 16, LineHeight: 1.8}
	l := ui.ShapeRichText([]ui.Span{{Text: "也就是「波形」：", Size: font.Size}}, font, 400)
	line, cr := l.Lines()[0], textCaret(l, ui.TextCaretPosition{Index: 3})
	if cr.H >= line.Rect.H || cr.Y <= line.Rect.Y || cr.Y+cr.H >= line.Rect.Y+line.Rect.H {
		t.Errorf("caret %+v fills its line %+v", cr, line.Rect)
	}
	if cr.H < font.Size {
		t.Errorf("caret %+v shorter than the text, %v", cr, font.Size)
	}
}

func TestClickEndsComposition(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello world")
	tt.Key(0, ui.KeyEnter)
	tt.Type("ab")
	tt.Compose("ni", 2)
	tt.Frame()
	first := e.Content.Children[0]
	r := boxOf(e, first.ID)
	tt.ClickAt(r.X+1, r.Y+r.H/2)
	if e.marked {
		t.Fatal("the click kept the composition")
	}
	tt.Compose("nih", 3)
	tt.Type("你好")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:你好hello world", "paragraph:abni"}) {
		t.Errorf("blocks %v", got)
	}
	tt.Compose("x", 1)
	e.PlaceCaret(e.Content.Children[1].ID, 0)
	tt.Compose("y", 1)
	tt.Type("也")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:你好xhello world", "paragraph:也abni"}) {
		t.Errorf("blocks after moving the caret %v", got)
	}
}

func TestCompositionIsOneUndoStep(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("a")
	tt.Compose("ni", 2)
	tt.Type("你")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a你"}) {
		t.Fatalf("typed %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a"}) {
		t.Errorf("after one undo %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:"}) {
		t.Errorf("after two undos %v", got)
	}
	e.Redo()
	steps := len(e.undo)
	tt.Compose("ha", 2)
	tt.Compose("", 0)
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a"}) || len(e.undo) != steps || len(e.redo) == 0 {
		t.Errorf("given up: %v, %d steps, want %d; redo %d", got, len(e.undo), steps, len(e.redo))
	}
	e.Select(Pos{e.Content.Children[0].ID, 0}, Pos{e.Content.Children[0].ID, 1})
	tt.Compose("ni", 2)
	tt.Type("你")
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a"}) {
		t.Errorf("after undoing over a selection %v", got)
	}
	steps = len(e.undo)
	e.PlaceCaret(e.Content.Children[0].ID, -1)
	tt.Compose("ni", 2)
	tt.Type("\n")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "paragraph:"}) || len(e.undo) != steps+1 {
		t.Errorf("ended by Enter: %v, %d steps, want %d", got, len(e.undo), steps+1)
	}
}

func TestCmdClickOpensOnlyWebLinks(t *testing.T) {
	e, tt, _ := setup(t)
	var blocks []*doc.Node
	for _, href := range []string{"https://x.dev", "mailto:a@b.c", "file:///tmp/run.command", "x-app://do", "javascript:alert(1)"} {
		blocks = append(blocks, newText("paragraph", doc.Delta{{Text: "link link link", Attrs: doc.Attrs{Href: href}}}))
	}
	e.Edit(func(c *doc.Content) { c.Children = blocks })
	tt.Frame()
	for _, n := range blocks {
		r := boxOf(e, n.ID)
		tt.ClickAtWith(ui.Cmd, r.X+10, r.Y+r.H/2)
		tt.Frame()
	}
	if got := tt.OpenedURLs(); !slices.Equal(got, []string{"https://x.dev", "mailto:a@b.c"}) {
		t.Errorf("opened %q", got)
	}
}

func TestNoUndoStepsThatChangeNothing(t *testing.T) {
	div := &doc.Node{ID: "D", Type: "divider"}
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), div, para("B", "b")}), &fakeHost{})
	e.PlaceCaret("B", -1)
	e.InsertText("x")
	e.Undo()
	check := func(what string, caret Pos) {
		t.Helper()
		if len(e.undo) > 0 || len(e.redo) == 0 {
			t.Errorf("%s: undo %d, redo %d", what, len(e.undo), len(e.redo))
		}
		if e.Caret() != caret {
			t.Errorf("%s: caret %v, want %v", what, e.Caret(), caret)
		}
	}
	e.PlaceCaret("A", -1)
	e.Delete(false)
	check("Delete before a divider", Pos{"D", 0})
	e.PlaceCaret("A", 0)
	e.Backspace(false)
	check("Backspace at the start of the page", Pos{"A", 0})
	e.PlaceCaret("B", 0)
	e.Backspace(false)
	check("Backspace after a divider", Pos{"D", 0})
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:", "paragraph:b"}) {
		t.Errorf("blocks %v", got)
	}
}

func TestPasteOnABlockIsOneUndoStep(t *testing.T) {
	div := &doc.Node{ID: "D", Type: "divider"}
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), div, para("B", "b")}), &fakeHost{})
	e.PlaceCaret("D", 0)
	e.paste("x\ny")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:", "paragraph:x", "paragraph:y", "paragraph:b"}) {
		t.Fatalf("pasted %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:", "paragraph:b"}) || len(e.undo) > 0 {
		t.Errorf("after undo %v, %d steps", got, len(e.undo))
	}
}

func TestAgentOpsAfterAnEditOutliveItsUndo(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Hello")
	e.BreakUndo()
	first := e.Content.Children[0].ID
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("A", "agent A")), true)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("B", "agent B")), false)
	e.PlaceCaret(first, -1)
	tt.Type("!")
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("C", "agent C")), false)
	tt.Type("?")
	for _, want := range [][]string{
		{"paragraph:Hello!", "paragraph:agent A", "paragraph:agent B", "paragraph:agent C"},
		{"paragraph:Hello", "paragraph:agent A", "paragraph:agent B", "paragraph:agent C"},
		{"paragraph:Hello", "paragraph:agent C"},
	} {
		e.Undo()
		if got := texts(e); !slices.Equal(got, want) {
			t.Errorf("after undo %v, want %v", got, want)
		}
	}
}

func TestAgentOpsPastAnEditShareOneMark(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Hello")
	e.BreakUndo()
	first := e.Content.Children[0].ID
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("A", "agent A")), true)
	e.PlaceCaret(first, -1)
	tt.Type("!")
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("C", "agent C")), false)
	steps := len(e.undo)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("D", "agent D")), false)
	if len(e.undo) != steps {
		t.Errorf("%d steps of undo after one more op, want %d", len(e.undo), steps)
	}
	e.Undo()
	if got, want := texts(e), []string{"paragraph:Hello", "paragraph:agent A", "paragraph:agent C", "paragraph:agent D"}; !slices.Equal(got, want) {
		t.Errorf("after undo %v, want %v", got, want)
	}
}

func TestCopyAndCutABlockSelectedWhole(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Intro")
	e.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "divider"})
	tt.Frame()
	div := e.units()[1]
	r := boxOf(e, div.ID)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	if e.Caret() != (Pos{div.ID, 0}) {
		t.Fatalf("caret %v", e.Caret())
	}
	tt.Command("copy")
	if tt.Clipboard() == "" || len(clip.blocks) != 1 || clip.blocks[0].Type != "divider" {
		t.Fatalf("copied %q, %v", tt.Clipboard(), clip.blocks)
	}
	tt.SetClipboard("")
	tt.Command("cut")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:Intro", "paragraph:"}) || tt.Clipboard() == "" {
		t.Fatalf("after cut %v, clipboard %q", got, tt.Clipboard())
	}
	tt.Command("paste")
	if got := texts(e); len(got) < 3 || got[1] != "divider:" {
		t.Errorf("after paste %v", got)
	}
}

func TestPasteLinesInAnItemWithChildren(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"lines", "x\ny", []string{"bulleted_list:abx", "bulleted_list:ycd", "  bulleted_list:kid"}},
		{"code", "```\nz\n```", []string{"bulleted_list:ab", "code:z", "bulleted_list:cd", "  bulleted_list:kid"}},
	} {
		e, _, _ := setup(t)
		item := newText("bulleted_list", doc.FromMarkdown("abcd"))
		kid := newText("bulleted_list", doc.FromMarkdown("kid"))
		item.Children = []*doc.Node{kid}
		e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{item} })
		e.PlaceCaret(item.ID, 2)
		e.paste(tc.text)
		var got []string
		for _, n := range e.units() {
			got = append(got, strings.Repeat("  ", e.depth(n.ID))+n.Type+":"+n.Delta().Plain())
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
		if e.Caret().ID != e.units()[len(tc.want)-2].ID {
			t.Errorf("%s: caret in %v", tc.name, e.node(e.Caret().ID).Delta().Plain())
		}
	}
	e, _, _ := setup(t)
	item := newText("bulleted_list", doc.FromMarkdown("ab"))
	item.Children = []*doc.Node{newText("bulleted_list", doc.FromMarkdown("kid"))}
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{item} })
	e.PlaceCaret(item.ID, -1)
	e.paste("x\n- y")
	var got []string
	for _, n := range e.units() {
		got = append(got, strings.Repeat("  ", e.depth(n.ID))+n.Type+":"+n.Delta().Plain())
	}
	if want := []string{"bulleted_list:abx", "bulleted_list:y", "  bulleted_list:kid"}; !slices.Equal(got, want) {
		t.Errorf("at the end: %v, want %v", got, want)
	}
}

func TestInlineShortcutSkipsMentions(t *testing.T) {
	for _, tc := range []struct{ title, typed string }{{"A*b", "c*"}, {"A**b", "c**"}, {"x", "*y*"}, {"A`b", "c`"}} {
		n := &doc.Node{ID: "A", Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: "see "}, {Text: tc.title, Attrs: doc.PageMention("p1", tc.title)}})
		e := New(doc.FromBlocks([]*doc.Node{n}), &fakeHost{})
		e.PlaceCaret("A", -1)
		for _, r := range tc.typed {
			e.InsertText(string(r))
		}
		d := e.node("A").Delta()
		mentions := 0
		for _, r := range d {
			if r.Attrs.Mention != nil {
				mentions++
			}
		}
		if mentions != 1 {
			t.Errorf("%q then %q: the mention went, %+v", tc.title, tc.typed, d)
		}
		if tc.typed == "*y*" {
			if last := d[len(d)-1]; last.Text != "y" || !last.Attrs.Italic {
				t.Errorf("*y* after a mention: %+v", d)
			}
		} else if d.Plain() != "see "+tc.title+tc.typed {
			t.Errorf("%q then %q: %q", tc.title, tc.typed, d.Plain())
		}
	}
}

func TestUndoIndentFoldsTheToggleAgain(t *testing.T) {
	tg := &doc.Node{ID: "T", Type: "toggle_list", Children: []*doc.Node{para("K", "kid")}}
	tg.SetDelta(doc.Delta{{Text: "Toggle"}})
	tg.Set("collapsed", true)
	item := &doc.Node{ID: "B", Type: "bulleted_list"}
	item.SetDelta(doc.Delta{{Text: "b"}})
	e := New(doc.FromBlocks([]*doc.Node{tg, item}), &fakeHost{})
	e.PlaceCaret("B", 0)
	e.Indent()
	if collapsed(e.node("T")) {
		t.Fatal("Tab kept the toggle folded")
	}
	e.Undo()
	if !collapsed(e.node("T")) {
		t.Errorf("undo left the toggle unfolded: %v", texts(e))
	}
}

func TestOutdentUnindentedCodeMakesNoStep(t *testing.T) {
	c := doc.CodeNode("line", "")
	c.ID = "C"
	e := New(doc.FromBlocks([]*doc.Node{c}), &fakeHost{})
	e.PlaceCaret("C", -1)
	e.InsertText("x")
	e.Undo()
	e.PlaceCaret("C", 0)
	e.indentCode(e.node("C"), true)
	if len(e.undo) > 0 || len(e.redo) == 0 {
		t.Errorf("undo %d, redo %d", len(e.undo), len(e.redo))
	}
}

func TestTypingOnABlockIsOneUndoStep(t *testing.T) {
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), {ID: "D", Type: "divider"}}), &fakeHost{})
	e.PlaceCaret("D", 0)
	for _, r := range "hello" {
		e.InsertText(string(r))
	}
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:", "paragraph:hello"}) {
		t.Fatalf("typed %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:"}) {
		t.Errorf("after one undo %v", got)
	}
}

func TestOpsFromOutsideKeepTheView(t *testing.T) {
	p := &doc.Node{ID: "p1", Type: "paragraph"}
	p.SetDelta(doc.Delta{{Text: "Asked here"}})
	e := New(doc.FromBlocks([]*doc.Node{p}), &fakeHost{})
	e.PlaceCaret("p1", 3)
	e.reveal = ""
	n := &doc.Node{ID: "n1", Type: "paragraph"}
	n.SetDelta(doc.Delta{{Text: "Written by the agent"}})
	if err := e.ApplyOp(doc.InsertOp(doc.Position{After: "p1"}, n), true); err != nil {
		t.Fatal(err)
	}
	if err := e.ApplyOps([]doc.Op{e.Content.DeleteOp([]string{"n1"})}); err != nil {
		t.Fatal(err)
	}
	e.Replace(e.Content.Copy())
	if e.reveal != "" || e.FocusBlock() != "p1" {
		t.Errorf("reveal %q, caret in %q", e.reveal, e.FocusBlock())
	}
}

func TestTypingKeepsTheCaretOfATallBlockInView(t *testing.T) {
	freshClipboard(t)
	e := New(doc.Empty(), &fakeHost{})
	e.SetWidth(500)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(20).Children(func() { e.View(c) })
		})
	}, 600, 400)
	var lines []string
	for i := range 80 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	code := doc.CodeNode(strings.Join(lines, "\n"), "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{newText("paragraph", doc.Delta{{Text: "intro"}}), code} })
	tt.Frame()
	if err := tt.Click("Page"); err != nil {
		t.Fatal(err)
	}
	e.PlaceCaret(code.ID, -1)
	tt.Frame()
	tt.Type("x")
	tt.Frame()
	tt.Frame()
	if cr, _ := e.caretRect(e.focus); cr.Y < 0 || cr.Y+cr.H > 400 {
		t.Errorf("the caret at y %.0f, out of the 400 high window", cr.Y)
	}
}

func TestColoredTableCell(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"Name"}, {`<span style="color:#ef4444ff">Blocked</span> item`}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := boxOf(e, table.ID)
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	if want := `<span style="color:#ef4444ff">Blocked</span> item`; e.cellEdit != cellKey(table.ID, 1, 0) || e.cellText != want {
		t.Fatalf("editing %q: %q", e.cellEdit, e.cellText)
	}
	tt.Type("!")
	tt.Frame()
	d := doc.TableCells(table)[1][0].Children[0].Delta()
	if len(d) != 2 || d[0].Text != "Blocked" || d[0].Attrs.Color != "#ef4444ff" || !strings.Contains(d.Plain(), "!") {
		t.Fatalf("delta %+v", d)
	}
}

func spacePage(t *testing.T, readOnly bool) (*Editor, *ui.Tester, *ui.ScrollState, *doc.Node) {
	t.Helper()
	e := New(doc.Empty(), &fakeHost{})
	e.SetWidth(440)
	var scroll ui.ScrollState
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).TrackScroll(&scroll).Fill().Children(func() { e.View(c) })
	}, 600, 400)
	if err := tt.Click("Page"); err != nil {
		t.Fatal(err)
	}
	var mid *doc.Node
	e.Edit(func(c *doc.Content) {
		c.Children = nil
		for i := range 60 {
			n := newText("paragraph", doc.FromMarkdown(fmt.Sprintf("Line %d", i)))
			c.Children = append(c.Children, n)
			if i == 30 {
				mid = n
			}
		}
	})
	tt.Frame()
	e.Select(Pos{mid.ID, 4}, Pos{mid.ID, 4})
	tt.Frame()
	cr, ok := e.caretRect(e.focus)
	if !ok {
		t.Fatal("no caret")
	}
	scroll.Y += cr.Y - 150
	tt.Frame()
	e.ReadOnly = readOnly
	tt.Frame()
	return e, tt, &scroll, mid
}

func TestSpaceTypedLeavesThePageWhereItIs(t *testing.T) {
	for _, mods := range []ui.Modifiers{0, ui.Shift} {
		e, tt, scroll, mid := spacePage(t, false)
		before := scroll.Y
		tt.TypeKey(mods, ui.KeySpace, " ")
		tt.Frame()
		if got := mid.Delta().Plain(); got != "Line  30" {
			t.Fatalf("mods %v: text = %q", mods, got)
		}
		if !e.focused {
			t.Fatalf("mods %v: the editor lost the focus", mods)
		}
		if scroll.Y != before {
			t.Errorf("mods %v: scrolled from %v to %v", mods, before, scroll.Y)
		}
	}
}

func TestSpacePagesAPageThatCannotBeTypedIn(t *testing.T) {
	_, tt, scroll, _ := spacePage(t, true)
	before := scroll.Y
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if scroll.Y <= before {
		t.Errorf("stayed at %v, from %v", scroll.Y, before)
	}
}

func TestReadOnlyTextWrapsPretty(t *testing.T) {
	var pal *theme.Palette
	ui.NewTester(func(c *ui.Context) { pal = theme.Of(c) }, 10, 10).Frame()
	wrapped := func(e *Editor, n *doc.Node) (lines, last int) {
		ls := e.layout(n, pal).layout.Lines()
		end := ls[len(ls)-1]
		return len(ls), len(strings.Fields(n.Delta().Plain()[end.Start:end.End]))
	}
	for words := 2; words < 80; words++ {
		e := New(doc.Empty(), &fakeHost{})
		e.SetWidth(300)
		n := newText("paragraph", doc.Delta{{Text: strings.TrimSpace(strings.Repeat("word ", words))}})
		e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{n} })
		if lines, last := wrapped(e, n); lines < 2 || last != 1 {
			continue
		}
		e.ReadOnly = true
		if _, last := wrapped(e, n); last < 2 {
			t.Errorf("%d words read-only: %d on the last line", words, last)
		}
		code := doc.CodeNode("a := 1", "")
		e.Edit(func(c *doc.Content) { c.Children = append(c.Children, code) })
		if e.layout(code, pal).pretty {
			t.Error("code wraps pretty")
		}
		return
	}
	t.Fatal("no length left a word alone on its last line")
}
