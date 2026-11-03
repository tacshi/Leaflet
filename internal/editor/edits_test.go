package editor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// Deleting a selection from a code block into a formatted one leaves the
// code plain: the text after the selection joins it without its formats
// or mentions.
func TestDeleteIntoCodeKeepsItPlain(t *testing.T) {
	code := doc.CodeNode("let x", "js")
	rich := newText("paragraph", doc.Delta{
		{Text: "ab"},
		{Text: " bold", Attrs: doc.Attrs{Bold: true}},
		{Text: "@Ann", Attrs: doc.Attrs{Mention: &doc.Mention{Kind: "user", ID: "u1", Name: "Ann"}}},
	})
	e := New(doc.FromBlocks([]*doc.Node{code, rich}), &fakeHost{})
	e.Select(Pos{code.ID, 3}, Pos{rich.ID, 2})
	e.Backspace(false)
	d := e.Content.Children[0].Delta()
	if len(e.Content.Children) != 1 || d.Plain() != "let bold@Ann" || len(d) != 1 || !d[0].Attrs.Zero() {
		t.Errorf("code is %+v", d)
	}
}

// A line pasted on a block selected whole goes in a paragraph after it, as
// typing does, in one step; Shift-Enter there, with no line to break, does
// nothing and makes no step.
func TestPasteAndSoftBreakOnABlockSelectedWhole(t *testing.T) {
	div := &doc.Node{ID: "D", Type: "divider"}
	e := New(doc.FromBlocks([]*doc.Node{para("A", "a"), div}), &fakeHost{})
	e.PlaceCaret("D", 0)
	e.SoftBreak()
	if len(e.undo) > 0 {
		t.Fatal("Shift-Enter on a divider made a step of undo")
	}
	e.paste("pasted")
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:", "paragraph:pasted"}) {
		t.Fatalf("pasted %v", got)
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:a", "divider:"}) || len(e.undo) > 0 {
		t.Errorf("after undo %v, %d steps", got, len(e.undo))
	}
}

// A page mention linked elsewhere, or unlinked, is its text: no mention
// of the page is left linking to something else.
func TestLinkOverAPageMention(t *testing.T) {
	for _, url := range []string{"https://x.dev", ""} {
		n := newText("paragraph", doc.Delta{{Text: "see "}, {Text: "Plan", Attrs: doc.PageMention("p1", "Plan")}})
		e := New(doc.FromBlocks([]*doc.Node{n}), &fakeHost{})
		e.Select(Pos{n.ID, 4}, Pos{n.ID, 8})
		e.SetLink(url)
		for _, r := range e.Content.Children[0].Delta() {
			if r.Attrs.Mention != nil || r.Text == "Plan" && r.Attrs.Href != url {
				t.Errorf("linked to %q: %+v", url, r)
			}
		}
	}
	// Linked to its own page, it stays a mention.
	n := newText("paragraph", doc.Delta{{Text: "Plan", Attrs: doc.PageMention("p1", "Plan")}})
	e := New(doc.FromBlocks([]*doc.Node{n}), &fakeHost{})
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 4})
	e.SetLink(doc.PageLink("p1"))
	if d := e.Content.Children[0].Delta(); d[0].Attrs.Mention == nil {
		t.Errorf("relinked to its page: %+v", d)
	}
}

// Control-Y puts back what Control-K killed as it was, in one step: no
// Markdown is read in it, nor a shortcut typed.
func TestYankPutsBackTextAsItWas(t *testing.T) {
	e, _, _ := setup(t)
	nodes := page(e, "x")
	e.PlaceCaret(nodes[0].ID, -1)
	killed = "# not a heading\n- nor a list /menu"
	e.emacsKey(ui.KeyY, false)
	if got := texts(e); !slices.Equal(got, []string{"paragraph:x" + killed}) || e.Trigger() != nil {
		t.Fatalf("yanked %v, menu %v", got, e.Trigger())
	}
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:x"}) {
		t.Errorf("after undo %v", got)
	}
}

// Text copied for another app keeps a block's line breaks as hard breaks,
// escaping a line that would start a block, so it reads back as it was.
func TestCopiedTextKeepsLineBreaks(t *testing.T) {
	e := New(doc.FromBlocks(nil), &fakeHost{})
	item := newText("bulleted_list", doc.Delta{{Text: "one\ntwo"}})
	p := newText("paragraph", doc.Delta{{Text: "a\n- b"}})
	q := newText("quote", doc.Delta{{Text: "q\nr"}})
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{item, p, q} })
	e.Select(Pos{item.ID, 0}, Pos{q.ID, 3})
	text := e.copyText()
	got := doc.ParseLines(text)
	var read []string
	for _, n := range got {
		read = append(read, n.Type+":"+n.Delta().Plain())
	}
	if want := []string{"bulleted_list:one\ntwo", "paragraph:a\n- b", "quote:q\nr"}; !slices.Equal(read, want) {
		t.Errorf("copied %q, read back as %q", text, read)
	}
}

// Cmd-K on a read-only page opens no link editor.
func TestReadOnlyOpensNoLinkEditor(t *testing.T) {
	e, tt, _ := setup(t)
	nodes := page(e, "some text")
	e.ReadOnly = true
	e.Select(Pos{nodes[0].ID, 0}, Pos{nodes[0].ID, 4})
	tt.Key(ui.Cmd, ui.KeyK)
	if e.linkOpen {
		t.Error("the link editor opened on a read-only page")
	}
}

// The @ menu counts what is typed after it in characters, not bytes: a
// name of Chinese or Japanese with a space in it keeps the menu open.
func TestMentionMenuCountsCharacters(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("会議は@")
	tt.Type("山田 太郎さんと鈴木 ")
	if e.Trigger() == nil {
		t.Errorf("the menu closed after %q", texts(e))
	}
}

// Typing after the caret is put elsewhere in the same block, by the app
// as by a click, is an undo step of its own.
func TestPlaceCaretBreaksTyping(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("abc")
	n := e.Content.Children[0]
	e.PlaceCaret(n.ID, 0)
	tt.Type("x")
	e.Select(Pos{n.ID, 2}, Pos{n.ID, 2})
	tt.Type("y")
	for _, want := range []string{"paragraph:xabc", "paragraph:abc", ""} {
		e.Undo()
		if got := texts(e); want != "" && !slices.Equal(got, []string{want}) {
			t.Errorf("after undo %v, want %v", got, want)
		}
	}
	// Backspaces in a row, which move the caret, are still one step.
	e, tt, _ = setup(t)
	tt.Type("abc")
	e.BreakUndo()
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	e.Undo()
	if got := texts(e); !slices.Equal(got, []string{"paragraph:abc"}) {
		t.Errorf("after undoing the backspaces %v", got)
	}
}

// Shift-Tab goes back a cell, from a row's first to the last of the row
// before, and stays in the first cell of all.
func TestTableCellsShiftTab(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID", "Name"}, {"", ""}}, "")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := boxOf(e, table.ID)
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	for _, want := range []string{cellKey(table.ID, 0, 1), cellKey(table.ID, 0, 0), cellKey(table.ID, 0, 0)} {
		tt.Key(ui.Shift, ui.KeyTab)
		tt.Frame()
		if e.cellEdit != want {
			t.Fatalf("after Shift-Tab, editing %q, want %q", e.cellEdit, want)
		}
	}
}

// A table missing a cell, as another client can write one, shows the
// cell empty and makes it as it is typed in; one whose rows are of other
// lengths has the cells of its longest.
func TestTableMissingACell(t *testing.T) {
	e, tt, _ := setup(t)
	table, _ := doc.TableNode([][]string{{"ID", "Name"}, {"1"}}, "")
	if rows := doc.TableRows(table); len(rows) != 2 || len(rows[1]) != 2 || rows[1][1] != "" {
		t.Fatalf("rows of other lengths made %q", rows)
	}
	table.Children = slices.DeleteFunc(table.Children, func(c *doc.Node) bool {
		return c.Int("rowPosition", 0) == 1 && c.Int("colPosition", 0) == 1
	})
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{table} })
	tt.Frame()
	box := boxOf(e, table.ID)
	tt.ClickAt(box.X+20, box.Y+box.H*0.75)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if e.cellEdit != cellKey(table.ID, 1, 1) {
		t.Fatalf("editing %q", e.cellEdit)
	}
	tt.Type("Ann")
	tt.Frame()
	if rows := doc.TableRows(table); rows[1][1] != "Ann" || len(table.Children) != 4 {
		t.Errorf("rows %q, %d cells", rows, len(table.Children))
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "ID") {
		t.Errorf("shown %v", tt.Texts())
	}
}

// A web link put in a code block is its text, not a link: code holds no
// formats.
func TestLinkInCodeIsPlain(t *testing.T) {
	code := doc.CodeNode("x", "")
	e := New(doc.FromBlocks([]*doc.Node{code}), &fakeHost{})
	e.PlaceCaret(code.ID, -1)
	e.insertInline("https://x.dev", "https://x.dev")
	if d := e.Content.Children[0].Delta(); len(d) != 1 || !d[0].Attrs.Zero() || d.Plain() != "xhttps://x.dev" {
		t.Errorf("code is %+v", d)
	}
}

// Leaflet's own blocks pasted into code go in as text, where the caret is:
// as blocks they split the code, and took their formats into it.
func TestOwnBlocksPasteIntoCodeAsText(t *testing.T) {
	freshClipboard(t)
	src := New(doc.FromBlocks([]*doc.Node{newText("paragraph", doc.FromMarkdown("**bold** one")), newText("paragraph", doc.FromMarkdown("two"))}), &fakeHost{})
	us := src.units()
	src.Select(Pos{us[0].ID, 0}, Pos{us[1].ID, 3})
	text := src.copyText()
	clip.text, clip.blocks, clip.delta = text, src.copyBlocks(), nil

	code := doc.CodeNode("x = 1", "")
	e := New(doc.FromBlocks([]*doc.Node{code}), &fakeHost{})
	e.PlaceCaret(code.ID, 2)
	e.paste(text)
	if got := e.Content.Children; len(got) != 1 || got[0].Type != "code" {
		t.Fatalf("blocks %v", texts(e))
	}
	d := code.Delta()
	if want := "x " + text + "= 1"; d.Plain() != want || len(d) != 1 || !d[0].Attrs.Zero() {
		t.Errorf("code %q (%d runs), want %q in one plain run", d.Plain(), len(d), want)
	}
}

// Text pasted with the breaks other apps end a paragraph at, as a lone
// "\r", goes in as lines, a block each: none stays in a block's text.
func TestPastedParagraphBreaksAreLines(t *testing.T) {
	for _, brk := range []string{"\r", "\r\n", "\u2029", "\u0085"} {
		e, _, _ := setup(t)
		e.paste("one" + brk + "two")
		if got := texts(e); len(got) != 2 || got[0] != "paragraph:one" || got[1] != "paragraph:two" {
			t.Errorf("%q: %v", brk, got)
		}
	}
}

// A click on a page link's letters opens the page, its last letter too; a
// click past its end, beside it, places the caret.
func TestClickOnALinkOpensIt(t *testing.T) {
	e, tt, host := setup(t)
	n := e.units()[0]
	e.Edit(func(c *doc.Content) {
		c.Find(n.ID).SetDelta(doc.Delta{{Text: "Plan", Attrs: doc.Attrs{Href: doc.PageLink("p1")}}})
	})
	tt.Frame()
	l, box := e.layouts[n.ID], boxOf(e, n.ID)
	last := l.layout.SelectionRects(ui.TextInputRange{Start: 3, End: 4})[0]
	y := box.Y + l.y + last.Y + last.H/2
	tt.ClickAt(box.X+l.x+last.X+last.W*0.8, y)
	tt.Frame()
	if len(host.opened) != 1 || host.opened[0] != "p1" {
		t.Fatalf("a click on its last letter opened %v", host.opened)
	}
	tt.Advance(time.Second)
	tt.ClickAt(box.X+l.x+last.X+last.W+6, y)
	tt.Frame()
	if len(host.opened) != 1 {
		t.Errorf("a click beside it opened %v", host.opened)
	}
}

// Paste and Match Style takes Leaflet's own copy without its formats, as
// it takes another app's: bold copied pastes plain.
func TestPasteAndMatchStyleOwnCopy(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("v1 and more")
	n := e.units()[0]
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 2})
	e.ToggleBold()
	tt.Command("copy")
	e.Select(Pos{n.ID, textLen(n)}, Pos{n.ID, textLen(n)})
	tt.Command("pasteAndMatchStyle")
	d := e.node(n.ID).Delta()
	if d.Plain() != "v1 and morev1" {
		t.Fatalf("pasted %q", d.Plain())
	}
	if last := d[len(d)-1]; last.Attrs.Bold {
		t.Errorf("pasted bold: %+v", d)
	}
	tt.Command("paste")
	if d := e.node(n.ID).Delta(); !d[len(d)-1].Attrs.Bold {
		t.Errorf("a plain paste lost its bold: %+v", d)
	}
}
