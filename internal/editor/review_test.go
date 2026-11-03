package editor

import (
	"fmt"
	"math"
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

// page sets the editor's page to paragraphs of texts.
func page(e *Editor, texts ...string) []*doc.Node {
	var nodes []*doc.Node
	for _, s := range texts {
		nodes = append(nodes, newText("paragraph", doc.Delta{{Text: s}}))
	}
	e.Edit(func(c *doc.Content) { c.Children = nodes })
	return nodes
}

// A page that has become read-only takes no undo or redo: the steps kept
// from before would put back its text as it was, for a later edit to save.
func TestUndoOnAReadOnlyPage(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello")
	tt.Key(0, ui.KeyEnter)
	tt.Type("world")
	before := texts(e)
	e.ReadOnly = true
	tt.Key(ui.Cmd, ui.KeyZ)
	e.Undo()
	e.Redo()
	tt.Command("undo")
	if got := texts(e); !slices.Equal(got, before) {
		t.Errorf("read-only page went to %v", got)
	}
	e.ReadOnly = false
	e.Undo()
	if got := texts(e); slices.Equal(got, before) {
		t.Error("the steps were lost")
	}
}

// A line break that replaces text, as an input method or a text
// replacement commits one, is one step with the text it replaced.
func TestReplaceTextWithALineBreakIsOneStep(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello")
	e.BreakUndo()
	e.ReplaceText(&ui.TextInputRange{Start: 1, End: 3}, "\n")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:h", "paragraph:lo"}) {
		t.Fatalf("replaced as %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:hello"}) {
		t.Errorf("undone as %v", got)
	}
}

// Text that replaces a selection, as autocorrect or an input method's
// commit puts it, is one step of undo, however many runes it has.
func TestReplacingASelectionIsOneStep(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("hello world")
	e.BreakUndo()
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 6}, Pos{n.ID, 11})
	e.InsertText("there")
	if got := n.Delta().Plain(); got != "hello there" {
		t.Fatalf("replaced as %q", got)
	}
	e.Undo()
	if got := e.Content.Children[0].Delta().Plain(); got != "hello world" {
		t.Errorf("undone as %q", got)
	}
}

// A block pasted on an empty line gives it its kind and none of what was
// the line's own kind's: an empty heading's level, a folded toggle's fold.
func TestPasteOnAnEmptyLineTakesOnlyItsKind(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		data map[string]any
		text string
		want string
	}{
		{"heading", map[string]any{"level": 1}, "- a\n- b", "bulleted_list"},
		{"toggle_list", map[string]any{"collapsed": true}, "# T\nnext", "heading"},
		{"todo_list", map[string]any{"checked": true, "due": "2026-11-02"}, "1. a\n2. b", "numbered_list"},
	} {
		e, _, _ := setup(t)
		n := newText(tc.typ, nil)
		for k, v := range tc.data {
			n.Set(k, v)
		}
		e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{n} })
		e.PlaceCaret(n.ID, 0)
		e.paste(tc.text)
		first := e.Content.Children[0]
		if first.Type != tc.want {
			t.Fatalf("%s: pasted as %v", tc.typ, texts(e))
		}
		for _, k := range doc.TypedKeys {
			if _, has := first.Data[k]; has != (tc.want == "heading" && k == "level") {
				t.Errorf("%s: pasted as %s with data %v", tc.typ, first.Type, first.Data)
			}
		}
		if len(e.units()) < 2 {
			t.Errorf("%s: what followed is hidden: %v", tc.typ, texts(e))
		}
	}
}

// With no layout, as a second key before the next frame finds a block,
// the caret and deletes still step a grapheme cluster: an emoji of
// several, a flag, a letter and its mark.
func TestStepsWithoutALayoutKeepClusters(t *testing.T) {
	for _, cluster := range []string{"👩‍👩‍👧", "🇯🇵", "e\u0301", "👍🏽"} {
		e, _, _ := setup(t)
		n := page(e, "a"+cluster+"b")[0]
		end := textLen(n)
		e.PlaceCaret(n.ID, end-1)
		e.Backspace(false)
		if got := n.Delta().Plain(); got != "ab" {
			t.Errorf("backspace over %q left %q", cluster, got)
		}
		n = page(e, "a"+cluster+"b")[0]
		e.PlaceCaret(n.ID, 1)
		e.Delete(false)
		if got := n.Delta().Plain(); got != "ab" {
			t.Errorf("delete of %q left %q", cluster, got)
		}
		n = page(e, "a"+cluster+"b")[0]
		e.PlaceCaret(n.ID, 1)
		e.layouts = map[string]*blockLayout{}
		e.horizontal(1, false, false, false)
		if p := e.Caret(); p.Off != end-1 {
			t.Errorf("right over %q went to %d, want %d", cluster, p.Off, end-1)
		}
		e.layouts = map[string]*blockLayout{}
		e.horizontal(-1, false, false, false)
		if p := e.Caret(); p.Off != 1 {
			t.Errorf("left over %q went to %d", cluster, p.Off)
		}
	}
}

// Text another app copies, the same as Leaflet copied, pastes as its own
// text, not as the blocks Leaflet copied.
func TestPasteOfTheSameTextFromAnotherApp(t *testing.T) {
	e, tt, _ := setup(t)
	nodes := page(e, "one", "two")
	nodes[1].Type = "quote"
	e.Select(Pos{nodes[0].ID, 0}, Pos{nodes[1].ID, 3})
	tt.Command("copy")
	text := tt.Clipboard()
	end := newText("paragraph", nil)
	e.Edit(func(c *doc.Content) { c.Children = append(c.Children, end) })
	e.PlaceCaret(end.ID, 0)
	tt.Command("paste")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:one", "quote:two", "paragraph:one", "quote:two"}) {
		t.Fatalf("own copy pasted as %v", got)
	}
	// Another app copies the same text, as plain text.
	tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte(text)))))
	clip.blocks[0].SetDelta(doc.Delta{{Text: "stale"}})
	end = newText("paragraph", nil)
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{end} })
	e.PlaceCaret(end.ID, 0)
	tt.Command("paste")
	if got := texts(e); slices.Contains(got, "paragraph:stale") {
		t.Errorf("another app's copy pasted Leaflet's blocks: %v", got)
	}
}

// An edit that takes out the page's only block, as removing its one
// subpage's card does, leaves the page an empty line to type in.
func TestEditThatEmptiesThePage(t *testing.T) {
	e, tt, _ := setup(t)
	sub := &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": "P"}}
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{sub} })
	e.PlaceCaret(sub.ID, 0)
	e.Edit(func(c *doc.Content) { c.Remove(sub.ID) })
	tt.Frame()
	tt.Type("x")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:x"}) {
		t.Errorf("page %v", got)
	}
}

// bigPage is an editor of a page of n paragraphs, every fourth with an
// item nested under it, and all selected.
func bigPage(b testing.TB, n int) *Editor {
	b.Helper()
	e := New(doc.Empty(), &fakeHost{})
	var nodes []*doc.Node
	for i := range n {
		p := newText("bulleted_list", doc.Delta{{Text: fmt.Sprintf("line %d", i)}})
		if i%4 == 0 {
			p.Children = []*doc.Node{newText("bulleted_list", doc.Delta{{Text: "nested"}})}
		}
		nodes = append(nodes, p)
	}
	e.Content.Children = nodes
	e.SelectAll()
	return e
}

// What the selection toolbar's Comment and Ask agent read of a selection
// over a long page takes one walk of it, not one for each block.
func BenchmarkSelectedText(b *testing.B) {
	for _, n := range []int{2000, 8000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			e := bigPage(b, n)
			for b.Loop() {
				e.SelectedText()
				e.SelectedBlocks()
			}
		})
	}
}

// Copying and deleting all of a long page walk it once.
func BenchmarkCopyAndDeleteSelection(b *testing.B) {
	for _, n := range []int{2000, 8000} {
		b.Run(fmt.Sprint("copy/", n), func(b *testing.B) {
			e := bigPage(b, n)
			for b.Loop() {
				e.copyText()
				e.copyBlocks()
			}
		})
		b.Run(fmt.Sprint("delete/", n), func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				e := bigPage(b, n)
				b.StartTimer()
				e.deleteSelection()
			}
		})
	}
}

// A key typed on a long page lays out only the block it changed: the
// others keep their layouts without reading their text again.
func BenchmarkTypeOnLongPage(b *testing.B) {
	for _, n := range []int{1000, 3000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			e := New(doc.Empty(), &fakeHost{})
			e.SetWidth(500)
			var nodes []*doc.Node
			for i := range n {
				nodes = append(nodes, newText("paragraph", doc.Delta{{Text: fmt.Sprintf("%d ", i) + strings.Repeat("words of a line ", 12)}}))
			}
			e.Content.Children = nodes
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Fill().Padding(20).Children(func() { e.View(c) })
			}, 600, 400)
			tt.Frame()
			e.PlaceCaret(nodes[n/2].ID, 3)
			tt.Frame()
			for b.Loop() {
				e.InsertText("x")
				tt.Frame()
			}
		})
	}
}

// A change made as the blocks build, as typing in a table's cell, leaves
// the blocks after it their places and depths, found again once, not a
// walk of the page for each.
func TestChangeWhileBuildingKeepsOrder(t *testing.T) {
	e := bigPage(t, 8)
	e.building = true
	e.order, e.depths = unitOrder(e.walkUnits(e.hid)), e.blockDepths()
	item := newText("bulleted_list", doc.Delta{{Text: "new"}})
	last := e.Content.Children[len(e.Content.Children)-1]
	last.Children = append(last.Children, item)
	e.changed()
	if e.order == nil || e.depths == nil {
		t.Fatal("the change dropped the blocks' places")
	}
	if d := e.depth(item.ID); d != 1 || e.index(item.ID) != len(e.units())-1 {
		t.Errorf("the new item at depth %d, place %d", d, e.index(item.ID))
	}
	e.building = false
	e.changed()
	if e.order != nil {
		t.Error("a change after the build kept the places of the one before")
	}
}

// The page's text takes a new accent or sepia paper as it is chosen, a
// link's accent too: its layouts are of their palette, and made again in
// another, not only once each block is edited.
func TestLookChangesTheLayout(t *testing.T) {
	t.Cleanup(func() { theme.Look(false, "") })
	e, tt, _ := setup(t)
	tt.Type("a link")
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 2}, Pos{n.ID, 6})
	e.SetLink("https://example.com")
	tt.Frame()
	before := e.layoutOf(n)
	for _, look := range []struct {
		sepia  bool
		accent string
	}{{false, "Green"}, {true, "Green"}} {
		theme.Look(look.sepia, look.accent)
		tt.Frame()
		after := e.layoutOf(n)
		if after == before || after.pal == before.pal || after.pal.Accent != theme.Accents[5].Light {
			t.Errorf("sepia %v, %s: the layout is of the palette before", look.sepia, look.accent)
		}
		before = after
	}
}

// Home and End, Cmd-Left and Cmd-Right, and Cmd-Backspace take the line
// the caret is on as the block wraps, not the whole block; at the end of
// a line that wraps, the caret shows there, not at the next line's start.
// Alt-Up and Alt-Down go to the block's ends.
func TestLineKeysTakeTheLineShown(t *testing.T) {
	e, tt, _ := setup(t)
	e.SetWidth(120)
	tt.Type("alpha beta gamma delta epsilon")
	tt.Frame()
	n := e.Content.Children[0]
	lines := e.layoutOf(n).layout.Lines()
	if len(lines) != 3 || lines[1].Start != 11 || lines[1].End != 23 {
		t.Fatalf("lines %+v", lines)
	}
	at := func(want int, line int) {
		t.Helper()
		if e.focus.Off != want {
			t.Errorf("caret at %d, want %d", e.focus.Off, want)
		}
		cr, _ := e.caretRect(e.focus)
		box, _ := e.box(n.ID)
		if y := cr.Y - box.Y - e.layoutOf(n).y; y != lines[line].Rect.Y {
			t.Errorf("caret at %d shows on the line at %v, want %v", want, y, lines[line].Rect.Y)
		}
	}
	tt.Key(0, ui.KeyHome)
	at(23, 2)
	e.PlaceCaret(n.ID, 14)
	tt.Key(0, ui.KeyEnd)
	at(23, 1)
	tt.Key(0, ui.KeyHome)
	at(11, 1)
	tt.Key(ui.Alt, ui.KeyDown)
	at(30, 2)
	tt.Key(ui.Alt, ui.KeyUp)
	at(0, 0)
	if runtime.GOOS != "darwin" {
		return // Cmd is Ctrl, a word's key
	}
	e.PlaceCaret(n.ID, 14)
	tt.Key(ui.Cmd, ui.KeyRight)
	at(23, 1)
	tt.Key(ui.Cmd, ui.KeyLeft)
	at(11, 1)
	e.PlaceCaret(n.ID, 14)
	tt.Key(ui.Cmd, ui.KeyBackspace)
	if got := n.Delta().Plain(); got != "alpha beta ma delta epsilon" {
		t.Errorf("after Cmd-Backspace %q", got)
	}
}

// The arrows go the way they point in a paragraph reading right to left:
// Left goes on in its text, and Right back.
func TestArrowsInRightToLeftText(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("שלום עולם")
	tt.Frame()
	n := e.Content.Children[0]
	if !e.rtl(n) {
		t.Fatal("the paragraph reads left to right")
	}
	e.PlaceCaret(n.ID, 0)
	tt.Key(0, ui.KeyLeft)
	tt.Key(0, ui.KeyLeft)
	if e.focus.Off != 2 {
		t.Errorf("after Left twice, the caret is at %d", e.focus.Off)
	}
	tt.Key(0, ui.KeyRight)
	if e.focus.Off != 1 {
		t.Errorf("after Right, the caret is at %d", e.focus.Off)
	}
	tt.Key(ui.Alt, ui.KeyLeft)
	if e.focus.Off != 4 {
		t.Errorf("after Alt-Left, the caret is at %d, the end of the word", e.focus.Off)
	}
}

// An input method composing on a block selected whole, or on a selection
// ending on one, starts a paragraph, as typing does, and what it commits
// goes there.
func TestCompositionOnABlockSelectedWhole(t *testing.T) {
	e, tt, _ := setup(t)
	nodes := page(e, "first")
	div := &doc.Node{ID: doc.NewBlockID(), Type: "divider"}
	e.Edit(func(c *doc.Content) { c.Children = append(c.Children, div) })
	e.PlaceCaret(div.ID, 0)
	tt.Frame()
	tt.Compose("ni", 2)
	if got := texts(e); !e.marked || !slices.Equal(got, []string{"paragraph:first", "divider:", "paragraph:ni"}) {
		t.Errorf("composing on the divider: %v, marked %v", got, e.marked)
	}
	tt.Type("你")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:first", "divider:", "paragraph:你"}) {
		t.Errorf("composed on the divider: %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:first", "divider:"}) {
		t.Errorf("after undo %v", got)
	}
	e.Select(Pos{nodes[0].ID, 2}, Pos{div.ID, 0})
	tt.Frame()
	tt.Compose("a", 1)
	if got := texts(e); !e.marked || !slices.Equal(got, []string{"paragraph:fia"}) {
		t.Errorf("composing over the selection: %v, marked %v", got, e.marked)
	}
	tt.Type("啊")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:fi啊"}) {
		t.Errorf("composed over a selection ending on the divider: %v", got)
	}
}

// A double click takes a word whole, a letter with its marks, as an "e"
// with its accent, and between words a cluster whole, as a flag.
func TestWordAtTakesClusters(t *testing.T) {
	rs := []rune("café au lait 🇯🇵!")
	for _, tc := range []struct{ off, from, to int }{
		{2, 0, 5}, {5, 0, 5}, {6, 6, 8}, {13, 9, 13}, {14, 14, 16}, {16, 16, 17},
	} {
		if a, b := wordAt(rs, tc.off); a != tc.from || b != tc.to {
			t.Errorf("at %d: %q, want %q", tc.off, string(rs[a:b]), string(rs[tc.from:tc.to]))
		}
	}
}

// Copy writes the blocks as CommonMark nests them, as an export does: a
// bullet under "1. " three columns in, which pastes back nested, and a
// toggle with its children in its <details>.
func TestCopyNestsAsMarkdown(t *testing.T) {
	e := New(doc.Empty(), &fakeHost{})
	parent := newText("numbered_list", doc.Delta{{Text: "Parent"}})
	child := newText("bulleted_list", doc.Delta{{Text: "child"}})
	parent.Children = []*doc.Node{child}
	tg := newText("toggle_list", doc.Delta{{Text: "More"}})
	inner := newText("paragraph", doc.Delta{{Text: "inside"}})
	tg.Children = []*doc.Node{inner}
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{parent, tg} })
	e.Select(Pos{parent.ID, 0}, Pos{inner.ID, 6})
	got := e.copyText()
	if !strings.Contains(got, "1. Parent\n   - child") || !strings.Contains(got, "<summary>More</summary>") {
		t.Errorf("copied %q", got)
	}
	back := doc.MarkdownBlocks(got)
	if len(back) != 2 || len(back[0].Children) != 1 || back[0].Children[0].Type != "bulleted_list" ||
		back[1].Type != "toggle_list" || len(back[1].Children) != 1 || back[1].Children[0].Delta().Plain() != "inside" {
		t.Errorf("%q reads back as %+v", got, back)
	}
}

// The link editor makes links that open: a web address without its
// scheme over https, a mail address to mail, nothing to unlink; what no
// link opens it doesn't take.
func TestLinkEditorMakesLinksThatOpen(t *testing.T) {
	for typed, want := range map[string]string{
		" example.com/a ": "https://example.com/a", "https://x.dev": "https://x.dev", "localhost:3000": "https://localhost:3000",
		"ann@example.com": "mailto:ann@example.com", "mailto:ann@example.com": "mailto:ann@example.com",
		doc.PageLink("p1"): doc.PageLink("p1"), "   ": "",
	} {
		if got, ok := linkHref(typed); !ok || got != want {
			t.Errorf("%q links %q, %v; want %q", typed, got, ok, want)
		}
	}
	for _, typed := range []string{"two words", "file:///etc/hosts", "javascript:alert(1)", "word", "mailto:"} {
		if got, ok := linkHref(typed); ok {
			t.Errorf("%q links %q", typed, got)
		}
	}
	e, tt, _ := setup(t)
	nodes := page(e, "see the site")
	e.Select(Pos{nodes[0].ID, 8}, Pos{nodes[0].ID, 12})
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Type("example.com")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if d := nodes[0].Delta(); len(d) != 2 || d[1].Attrs.Href != "https://example.com" || e.linkOpen {
		t.Errorf("linked %+v, editor open %v", d, e.linkOpen)
	}
}

// Keys in a menu just opened, before a frame has shown it, choose among
// its own choices, not those of the menu before.
func TestMenuKeysChooseFromTheMenuOpen(t *testing.T) {
	e, tt, host := setup(t)
	tt.Type("/")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Type(" ")
	e.InsertText("@")
	e.HandleInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyEnter})
	if len(host.picked) != 0 {
		t.Errorf("Enter in the @ menu picked %+v, of the / menu", host.picked)
	}
}

// The full-width forms Chinese and Japanese input methods type take as
// their ASCII does: "／" and "＠" open the menus, as "、" does where it is
// no comma, "＃　" makes a heading and "－－－" a divider.
func TestFullWidthShortcuts(t *testing.T) {
	for _, tc := range []struct {
		typed string
		opens bool
	}{
		{"／", true}, {"＠", true}, {"会議は＠", true}, {"、", true}, {"苹果、", false}, {"a／", false},
	} {
		e, tt, _ := setup(t)
		tt.Type(tc.typed)
		if got := e.Trigger() != nil; got != tc.opens {
			t.Errorf("%q opens a menu: %v", tc.typed, got)
		}
	}
	e, tt, host := setup(t)
	tt.Type("、he")
	if tr := e.Trigger(); tr == nil || tr.Kind != '/' || tr.Query != "he" {
		t.Fatalf("trigger %+v", tr)
	}
	tt.Key(0, ui.KeyEnter)
	if len(host.picked) != 1 {
		t.Errorf("picked %v", host.picked)
	}
	for typed, want := range map[string]string{"＃　标题": "heading:标题", "１．　一": "numbered_list:一", "－－－": "divider:"} {
		e, tt, _ := setup(t)
		tt.Type(typed)
		if got := texts(e); got[0] != want {
			t.Errorf("%q made %v", typed, got)
		}
	}
}

// The table's buttons add a row under the cell being edited and a column
// right of it, and take its own row or column away, not the last.
func TestTableButtonsActOnTheEditedCell(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"A", "B", "C"}, {"a1", "b1", "c1"}, {"a2", "b2", "c2"}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table, newText("paragraph", nil)} })
	tt.Frame()
	if err := tt.Click("b1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.cellEdit != cellKey(table.ID, 1, 1) {
		t.Fatalf("editing %q", e.cellEdit)
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("%v in %v", err, tt.Texts())
		}
		tt.Frame()
	}
	click("Add a row below")
	click("Add a column to the right")
	want := [][]string{{"A", "B", "", "C"}, {"a1", "b1", "", "c1"}, {"", "", "", ""}, {"a2", "b2", "", "c2"}}
	if rows := doc.TableRows(table); !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("after adding, rows %q", rows)
	}
	if e.cellEdit != cellKey(table.ID, 1, 1) {
		t.Errorf("after adding, editing %q", e.cellEdit)
	}
	click("Remove this row")
	click("Remove this column")
	want = [][]string{{"A", "", "C"}, {"", "", ""}, {"a2", "", "c2"}}
	if rows := doc.TableRows(table); !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("after removing, rows %q", rows)
	}
}

// Enter on the last row of a table that ends the page leaves it for a
// new paragraph after it.
func TestEnterLeavesATableThatEndsThePage(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"A"}, {"a1"}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	if err := tt.Click("a1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if got := texts(e); len(got) != 2 || got[1] != "paragraph:" || e.cellEdit != "" || e.Caret().ID != e.Content.Children[1].ID {
		t.Errorf("after Enter %v, editing %q, caret in %s", got, e.cellEdit, e.Caret().ID)
	}
}

// Typing makes a step of undo for each word, the space after it with it,
// and for each run of maxTyped characters without one.
func TestTypingStepsEndAtWords(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("one two three")
	e.Undo()
	if got := texts(e); got[0] != "paragraph:one two " {
		t.Errorf("after undo %v", got)
	}
	e.Undo()
	if got := texts(e); got[0] != "paragraph:one " {
		t.Errorf("after undo twice %v", got)
	}
	f, ft, _ := setup(t)
	ft.Type(strings.Repeat("字", maxTyped+5))
	f.Undo()
	if got := texts(f); got[0] != "paragraph:"+strings.Repeat("字", maxTyped) {
		t.Errorf("after undo %v", got)
	}
}

// Undoing an edit leaves a toggle or a heading folded as the user left it
// since: folding is no edit, and undo keeps it. A fold an edit made, as
// indenting under a folded toggle unfolds it, goes back with the edit.
func TestUndoKeepsLaterFolds(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("# Head")
	tt.Key(0, ui.KeyEnter)
	tt.Type("body")
	e.ToggleFold(e.Content.Children[0].ID)
	e.Undo()
	if !foldedHeading(e.Content.Children[0]) {
		t.Errorf("undoing the typing unfolded the heading: %v", texts(e))
	}
	e.Redo()
	if h := e.Content.Children[0]; !foldedHeading(h) {
		t.Error("redo unfolded the heading")
	}
}

// What the editor keeps of the blocks it shows goes with the blocks: a
// block deleted leaves no box, layout or look behind.
func TestDeletedBlocksLeaveNothingBehind(t *testing.T) {
	e, tt, _ := setup(t)
	nodes := page(e, "one", "two", "three")
	tt.Frame()
	if _, ok := e.box(nodes[1].ID); !ok || e.layouts[nodes[1].ID] == nil {
		t.Fatal("the block was not drawn")
	}
	e.Edit(func(c *doc.Content) { c.Remove(nodes[1].ID) })
	tt.Frame()
	_, drawn := e.box(nodes[1].ID)
	if _, laid := e.laid[nodes[1].ID]; drawn || laid || e.layouts[nodes[1].ID] != nil || e.looks[nodes[1].ID] != nil {
		t.Errorf("the deleted block kept: box %v, laid %v, layout %v, look %v", drawn, laid, e.layouts[nodes[1].ID] != nil, e.looks[nodes[1].ID] != nil)
	}
}

// Cmd-Enter checks the to-dos selected, or unchecks them when all are, and
// folds the toggle or heading at the caret, or unfolds it.
func TestCmdEnterChecksAndFolds(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("[] one")
	tt.Key(0, ui.KeyEnter)
	tt.Type("two")
	one, two := e.Content.Children[0], e.Content.Children[1]
	tt.Key(ui.Cmd, ui.KeyEnter)
	if one.Bool("checked") || !two.Bool("checked") {
		t.Errorf("checked %v, %v", one.Bool("checked"), two.Bool("checked"))
	}
	e.Select(Pos{one.ID, 1}, Pos{two.ID, 1})
	tt.Key(ui.Cmd, ui.KeyEnter)
	if !one.Bool("checked") || !two.Bool("checked") {
		t.Errorf("checked both: %v, %v", one.Bool("checked"), two.Bool("checked"))
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	if one.Bool("checked") || two.Bool("checked") {
		t.Errorf("unchecked both: %v, %v", one.Bool("checked"), two.Bool("checked"))
	}
	e.Undo()
	if top := e.Content.Children; !top[0].Bool("checked") || !top[1].Bool("checked") {
		t.Error("checking was no step of undo")
	}
	f, ft, _ := setup(t)
	f.SetType("toggle_list", 0)
	ft.Type("More")
	tg := f.Content.Children[0]
	ft.Key(ui.Cmd, ui.KeyEnter)
	if !collapsed(tg) {
		t.Error("Cmd-Enter left the toggle open")
	}
	ft.Key(ui.Cmd, ui.KeyEnter)
	if collapsed(tg) {
		t.Error("Cmd-Enter left the toggle folded")
	}
}

// A to-do's box is a check box to assistive technology, named as the
// to-do, and a click on it checks the to-do once.
func TestTodoBoxIsACheckBox(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("[] Call Ann")
	tt.Frame()
	if _, ok := tt.Find("Call Ann"); !ok {
		t.Fatalf("no control named for the to-do in %v", tt.Texts())
	}
	n := e.Content.Children[0]
	r, _ := e.box(n.ID)
	tt.ClickAt(r.X+11, r.Y+r.H/2)
	tt.Frame()
	if !n.Bool("checked") {
		t.Error("the click left the to-do unchecked")
	}
}

// The page's context menu cuts, copies and pastes, as the keys do.
func TestContextMenuCutsCopiesAndPastes(t *testing.T) {
	e, tt, _ := setup(t)
	// Below the room the selection's toolbar takes over it.
	nodes := page(e, "", "", "hello world")
	e.Select(Pos{nodes[2].ID, 0}, Pos{nodes[2].ID, 6})
	tt.Frame()
	r, _ := e.box(nodes[2].ID)
	tt.RightClickAt(r.X+100, r.Y+r.H/2)
	if err := chooseDrawn(tt, "Cut"); err != nil {
		t.Fatalf("%v; shown %v", err, tt.Texts())
	}
	if got := texts(e); got[2] != "paragraph:world" || tt.Clipboard() != "hello " {
		t.Errorf("after Cut %v, clipboard %q", got, tt.Clipboard())
	}
	e.PlaceCaret(nodes[2].ID, -1)
	tt.RightClickAt(r.X+100, r.Y+r.H/2)
	if err := chooseDrawn(tt, "Paste"); err != nil {
		t.Fatalf("%v; shown %v", err, tt.Texts())
	}
	if got := texts(e); got[2] != "paragraph:worldhello " {
		t.Errorf("after Paste %v", got)
	}
}

// Backspace at the start of a paragraph last in a toggle takes it out,
// after the toggle, as it outdents a nested item, and Enter on one that
// is empty leaves the toggle for the line after it.
func TestBackspaceAndEnterLeaveAToggle(t *testing.T) {
	e, _, _ := setup(t)
	e.SetType("toggle_list", 0)
	e.InsertText("More")
	tg := e.Content.Children[0]
	e.Enter()
	e.InsertText("inside")
	e.Enter()
	e.Enter()
	if len(tg.Children) != 1 || len(e.Content.Children) != 2 || e.Caret().ID != e.Content.Children[1].ID {
		t.Fatalf("after Enter on an empty last line, %v", texts(e))
	}
	e.InsertText("after")
	kid := tg.Children[0]
	e.PlaceCaret(kid.ID, 0)
	e.Backspace(false)
	if len(tg.Children) != 0 || len(e.Content.Children) != 3 || e.Content.Children[1].ID != kid.ID {
		t.Errorf("after Backspace, %v, %d in the toggle", texts(e), len(tg.Children))
	}
}

// A pie's last slice, beside its first, never takes the first's color.
func TestPieSlicesBesideDiffer(t *testing.T) {
	pal := theme.For(false)
	for n := 2; n <= 20; n++ {
		colors := sliceColors(pal, n)
		for i := range colors {
			if next := colors[(i+1)%n]; next == colors[i] {
				t.Errorf("a pie of %d: slices %d and %d share a color", n, i, (i+1)%n)
			}
		}
	}
}

// A frame in which an edit is made as the blocks build, as typing in a
// table's cell, costs as much more for a longer page, not the square.
func TestEditWhileBuildingIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("times frames")
	}
	frame := func(n int) time.Duration {
		table, _ := doc.TableNode([][]string{{"A"}, {"a1"}}, "")
		kids := []*doc.Node{table}
		for i := range n {
			kids = append(kids, newText("paragraph", doc.Delta{{Text: fmt.Sprintf("line %d with some words", i)}}))
		}
		e := New(doc.FromBlocks(kids), &fakeHost{})
		e.SetWidth(500)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Padding(20).Children(func() { e.View(c) })
		}, 600, 400)
		if err := tt.Click("a1"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		best := time.Duration(math.MaxInt64)
		for range 5 {
			runtime.GC()
			start := time.Now()
			tt.Type("x")
			best = min(best, time.Since(start))
		}
		if got := doc.TableRows(table)[1][0]; strings.Count(got, "x") != 5 {
			t.Fatalf("the cell took %q", got)
		}
		return best
	}
	small, large := frame(1000), frame(8000)
	if large > 24*small {
		t.Errorf("a key in a cell over 1000 blocks takes %v, over 8000 %v", small, large)
	}
}
