package editor

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/richtext"

	"leaflet/internal/doc"
)

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
	n := newText("paragraph", doc.Delta{{Text: "Plan", Attrs: doc.PageMention("p1", "Plan")}})
	e := New(doc.FromBlocks([]*doc.Node{n}), &fakeHost{})
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 4})
	e.SetLink(doc.PageLink("p1"))
	if d := e.Content.Children[0].Delta(); d[0].Attrs.Mention == nil {
		t.Errorf("relinked to its page: %+v", d)
	}
}

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

func TestMentionMenuCountsCharacters(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("会議は@")
	tt.Type("山田 太郎さんと鈴木 ")
	if e.Trigger() == nil {
		t.Errorf("the menu closed after %q", texts(e))
	}
}

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

func TestLinkInCodeIsPlain(t *testing.T) {
	code := doc.CodeNode("x", "")
	e := New(doc.FromBlocks([]*doc.Node{code}), &fakeHost{})
	e.PlaceCaret(code.ID, -1)
	e.insertInline("https://x.dev", "https://x.dev")
	if d := e.Content.Children[0].Delta(); len(d) != 1 || !d[0].Attrs.Zero() || d.Plain() != "xhttps://x.dev" {
		t.Errorf("code is %+v", d)
	}
}

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

func TestPastedParagraphBreaksAreLines(t *testing.T) {
	for _, brk := range []string{"\r", "\r\n", "\u2029", "\u0085"} {
		e, _, _ := setup(t)
		e.paste("one" + brk + "two")
		if got := texts(e); len(got) != 2 || got[0] != "paragraph:one" || got[1] != "paragraph:two" {
			t.Errorf("%q: %v", brk, got)
		}
	}
}

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

func mentionLine(t *testing.T) (*Editor, *ui.Tester) {
	t.Helper()
	e, tt, _ := setup(t)
	tt.Type("hi @")
	e.InsertMention(*e.Trigger(), doc.Mention{Kind: "user", ID: "u", Name: "Ann Lee"})
	return e, tt
}

func TestReplacingAWordThatReachesIntoAMentionTakesTheMention(t *testing.T) {
	e, tt := mentionLine(t)
	tt.Key(0, ui.KeyBackspace)
	tt.Type("s")
	e.ReplaceText(&ui.TextInputRange{Start: 8, End: 12}, "Less")
	n := e.Content.Children[0]
	if got := n.Delta().Plain(); got != "hi Less" {
		t.Fatalf("replaced as %q", got)
	}
	if want := (Pos{n.ID, 7}); e.focus != want || e.anchor != want {
		t.Fatalf("caret at %+v..%+v, want %+v", e.anchor, e.focus, want)
	}
	tt.Type(" ")
	tt.Type("/")
	if got := n.Delta().Plain(); got != "hi Less /" {
		t.Errorf("typed on as %q", got)
	}
}

func TestComposingOverPartOfAMentionReplacesIt(t *testing.T) {
	e, tt := mentionLine(t)
	n := e.Content.Children[0]
	e.SetMarkedText(&ui.TextInputRange{Start: 8, End: 11}, "り", ui.TextInputRange{Start: 1, End: 1})
	if got := n.Delta().Plain(); got != "hi り " {
		t.Fatalf("marked as %q", got)
	}
	if r, ok := e.MarkedRange(); !ok || r != (ui.TextInputRange{Start: 3, End: 4}) {
		t.Fatalf("marked range %+v, %v", r, ok)
	}
	tt.Frame()
	e.ReplaceText(nil, "李")
	if got := n.Delta().Plain(); got != "hi 李 " {
		t.Fatalf("committed as %q", got)
	}
	tt.Type("@")
	if got := n.Delta().Plain(); got != "hi 李@ " {
		t.Errorf("typed on as %q", got)
	}
}

func TestACaretTheSystemPutsInsideAMentionLeavesIt(t *testing.T) {
	e, _ := mentionLine(t)
	n := e.Content.Children[0]
	e.ReplaceText(&ui.TextInputRange{Start: 9, End: 9}, "x")
	if got := n.Delta().Plain(); got != "hi @Ann Leex " {
		t.Fatalf("typed as %q", got)
	}
	if want := (Pos{n.ID, 12}); e.focus != want {
		t.Errorf("caret at %+v, want %+v", e.focus, want)
	}
}

func TestSelectionKeepsItsSurvivingEndWhenABlockGoes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		anchor, focus Pos
		gone          []string
		want          Pos
	}{
		{"start", Pos{"A", 1}, Pos{"B", 2}, []string{"A"}, Pos{"B", 2}},
		{"end", Pos{"A", 1}, Pos{"B", 2}, []string{"B"}, Pos{"A", 1}},
		{"backwards", Pos{"C", 3}, Pos{"B", 2}, []string{"B"}, Pos{"C", 3}},
		{"both", Pos{"B", 1}, Pos{"C", 2}, []string{"B", "C"}, Pos{"A", 5}},
		{"first block", Pos{"A", 1}, Pos{"A", 1}, []string{"A"}, Pos{"B", 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, tt, _ := setup(t)
			e.Edit(func(c *doc.Content) {
				c.Children = []*doc.Node{para("A", "alpha"), para("B", "bravo"), para("C", "charlie"), para("D", "delta"), para("E", "echo")}
			})
			tt.Frame()
			e.Select(tc.anchor, tc.focus)
			if err := e.ApplyOp(doc.Op{Op: "delete", IDs: tc.gone}, true); err != nil {
				t.Fatal(err)
			}
			if e.anchor != tc.want || e.focus != tc.want {
				t.Fatalf("selection %+v..%+v, want a caret at %+v", e.anchor, e.focus, tc.want)
			}
			before := len(e.units())
			tt.Type("x")
			if got := len(e.units()); got != before {
				t.Errorf("typing left %d blocks of %d", got, before)
			}
		})
	}
}

func TestSelectionKeepsItsSurvivingEndWhenAnotherDeviceDropsABlock(t *testing.T) {
	e, tt, _ := setup(t)
	e.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{para("A", "alpha"), para("B", "bravo"), para("C", "charlie")}
	})
	tt.Frame()
	e.Select(Pos{"A", 1}, Pos{"B", 2})
	c := e.Content.Copy()
	c.Children = c.Children[1:]
	e.Replace(c)
	if want := (Pos{"B", 2}); e.anchor != want || e.focus != want {
		t.Errorf("selection %+v..%+v, want a caret at %+v", e.anchor, e.focus, want)
	}
}

func linked(t *testing.T) (*Editor, *ui.Tester, *doc.Node) {
	t.Helper()
	e, tt, _ := setup(t)
	n := e.Content.Children[0]
	n.SetDelta(doc.Delta{{Text: "see "}, {Text: "exmple", Attrs: doc.Attrs{Href: "https://example.com"}}, {Text: " now"}})
	tt.Frame()
	return e, tt, n
}

func linkText(n *doc.Node) string {
	var b strings.Builder
	for _, r := range n.Delta() {
		if r.Attrs.Href != "" {
			b.WriteString("[" + r.Text + "]")
		} else {
			b.WriteString(r.Text)
		}
	}
	return b.String()
}

func TestTypingInsideALinkKeepsItWhole(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   int
		do   func(e *Editor, tt *ui.Tester)
		want string
	}{
		{"typed", 6, func(e *Editor, tt *ui.Tester) { tt.Type("a") }, "see [example] now"},
		{"composed", 6, func(e *Editor, tt *ui.Tester) {
			tt.Compose("a", 1)
			tt.Type("a")
		}, "see [example] now"},
		{"pasted", 6, func(e *Editor, tt *ui.Tester) { e.paste("a") }, "see [example] now"},
		{"line break", 6, func(e *Editor, tt *ui.Tester) { e.SoftBreak() }, "see [ex\nmple] now"},
		{"before", 4, func(e *Editor, tt *ui.Tester) { tt.Type("a") }, "see a[exmple] now"},
		{"after", 10, func(e *Editor, tt *ui.Tester) { tt.Type("a") }, "see [exmple]a now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, tt, n := linked(t)
			e.PlaceCaret(n.ID, tc.at)
			tc.do(e, tt)
			if got := linkText(n); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestALinkPastedInsideALinkKeepsItsOwnAddress(t *testing.T) {
	e, _, n := linked(t)
	e.PlaceCaret(n.ID, 6)
	e.insertInline("x.dev", "https://x.dev")
	var hrefs []string
	for _, r := range n.Delta() {
		hrefs = append(hrefs, r.Text+"="+r.Attrs.Href)
	}
	want := []string{"see =", "ex=https://example.com", "x.dev=https://x.dev", "mple=https://example.com", " now="}
	if !slices.Equal(hrefs, want) {
		t.Errorf("runs %v, want %v", hrefs, want)
	}
}

func TestARunSplitByAnotherDevicesChangeIsStillOneStep(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("Hello")
	e.BreakUndo()
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("A", "agent A")), true)
	c := e.Content.Copy()
	c.Children = append(c.Children, para("R", "remote"))
	e.Replace(c)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("B", "agent B")), false)
	c = e.Content.Copy()
	c.Children = append(c.Children, para("S", "second"))
	e.Replace(c)
	_ = e.ApplyOp(doc.InsertOp(doc.Position{At: "end"}, para("C", "agent C")), false)
	all := texts(e)
	e.Undo()
	if got, want := texts(e), []string{"paragraph:Hello", "paragraph:remote", "paragraph:second"}; !slices.Equal(got, want) {
		t.Fatalf("after undo %v, want %v", got, want)
	}
	e.Redo()
	if got := texts(e); !slices.Equal(got, all) {
		t.Fatalf("after redo %v, want %v", got, all)
	}
	e.Undo()
	e.Undo()
	if got, want := texts(e), []string{"paragraph:", "paragraph:remote", "paragraph:second"}; !slices.Equal(got, want) {
		t.Errorf("after undoing the typing %v, want %v", got, want)
	}
}

func TestAHostEditKeepsACompositionGoing(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("a")
	tt.Compose("k", 1)
	tt.Compose("ka", 2)
	e.Edit(func(c *doc.Content) { _ = c.Insert(doc.Position{At: "end"}, para("X", "card")) })
	if !e.marked {
		t.Fatal("the composition ended")
	}
	tt.Compose("kan", 3)
	tt.Type("かん")
	if got, want := texts(e), []string{"paragraph:aかん", "paragraph:card"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	e.Undo()
	if got, want := texts(e), []string{"paragraph:a", "paragraph:card"}; !slices.Equal(got, want) {
		t.Errorf("after undo %v, want %v", got, want)
	}
}

func hiddenPage(t *testing.T) (*Editor, *ui.Tester) {
	t.Helper()
	e, tt, _ := setup(t)
	heading := func(id, text string, folded bool) *doc.Node {
		n := &doc.Node{ID: id, Type: "heading"}
		n.SetDelta(doc.Delta{{Text: text}})
		n.Set("level", 1)
		if folded {
			n.Set("collapsed", true)
		}
		return n
	}
	e.Edit(func(c *doc.Content) {
		c.Children = []*doc.Node{heading("H", "Head", true), para("S", "in section"), heading("N", "Next", false),
			para("P", "plain"), foldedToggle("T", "Toggle", "kid"), para("Z", "end")}
	})
	tt.Frame()
	e.Select(Pos{"H", 0}, Pos{"Z", 3})
	return e, tt
}

func TestFormattingReachesWhatASelectionHides(t *testing.T) {
	e, _ := hiddenPage(t)
	bold := func(a doc.Attrs) bool { return a.Bold }
	if e.Has(bold) {
		t.Fatal("bold before any formatting")
	}
	e.ToggleBold()
	e.Content.Walk(func(n, _ *doc.Node) {
		if !n.Delta().All(0, textLen(n), bold) {
			t.Errorf("%s %q is not bold", n.ID, n.Delta().Plain())
		}
	})
	if !e.Has(bold) {
		t.Error("the selection does not read as bold")
	}
	e.node("S").SetDelta(doc.Delta{{Text: "in section"}})
	if !e.Has(bold) {
		t.Error("a plain hidden block makes text that shows as bold read as plain")
	}
	e.ToggleBold()
	e.Content.Walk(func(n, _ *doc.Node) {
		for _, run := range n.Delta() {
			if run.Attrs.Bold {
				t.Errorf("%s %q is still bold", n.ID, n.Delta().Plain())
			}
		}
	})
}

func TestFetchedDataLeavesACompositionItsStep(t *testing.T) {
	e, tt, _ := setup(t)
	video := &doc.Node{ID: "V", Type: "video", Data: map[string]any{"video": map[string]any{"url": "https://example.com/v"}}}
	if err := e.Content.Insert(doc.Position{At: "end"}, video); err != nil {
		t.Fatal(err)
	}
	tt.Type("a")
	e.BreakUndo()
	steps := len(e.undo)
	tt.Compose("k", 1)
	e.SetData("V", "title", "Fetched")
	tt.Compose("", 0)
	if got := len(e.undo); got != steps+1 {
		t.Fatalf("%d steps after a composition given up, want %d and the fetched data's", got, steps)
	}
	e.Undo()
	if got := e.node("V").Str("title"); got != "Fetched" {
		t.Errorf("title %q after undoing past the composition", got)
	}
}

func TestSelectedTextAndBlocksTakeWhatCopyTakes(t *testing.T) {
	e, _ := hiddenPage(t)
	if _, got := e.SelectedText(); got != "Head\nin section\nNext\nplain\nToggle\nkid\nend" {
		t.Errorf("text %q", got)
	}
	if got, want := e.SelectedBlocks(), []string{"H", "S", "N", "P", "T", "TK", "Z"}; !slices.Equal(got, want) {
		t.Errorf("blocks %v, want %v", got, want)
	}
	e.Select(Pos{"H", 2}, Pos{"P", 0})
	if _, got := e.SelectedText(); got != "ad\nNext" {
		t.Errorf("text from inside a folded heading %q", got)
	}
	if got, want := e.SelectedBlocks(), []string{"H", "N"}; !slices.Equal(got, want) {
		t.Errorf("blocks from inside a folded heading %v, want %v", got, want)
	}
}

func TestSetTypeReachesWhatASelectionHides(t *testing.T) {
	e, _ := hiddenPage(t)
	e.SetType("quote", 0)
	e.Content.Walk(func(n, _ *doc.Node) {
		if n.Type != "quote" {
			t.Errorf("%s is a %s", n.ID, n.Type)
		}
	})
	if got := count(e.Content, "in section") + count(e.Content, "kid"); got != 2 {
		t.Errorf("%d of the two hidden blocks are left", got)
	}
}

func TestRichPasteWithNoTextOfItsOwnFallsBackToTheText(t *testing.T) {
	for _, html := range []string{
		`<h2><img src="https://example.com/a.png" alt="Logo"></h2>`,
		`<ul><li><img src="https://example.com/a.png"></li></ul>`,
		`<b><img src="https://example.com/a.png"></b>`,
	} {
		e, tt, _ := setup(t)
		tt.Type("before ")
		tt.SetClipboardData(transfer.New(transfer.NewItem(
			transfer.Bytes(transfer.HTML, []byte(html)),
			transfer.Bytes(transfer.Text, []byte("the text")))))
		tt.Command("paste")
		if got, want := texts(e), []string{"paragraph:before the text"}; !slices.Equal(got, want) {
			t.Errorf("%s pasted as %v, want %v", html, got, want)
		}
	}
}

func TestFetchedDataOutlivesTheUndoOfAnEarlierStep(t *testing.T) {
	e, tt, _ := setup(t)
	v, err := doc.VideoNode("https://youtu.be/dQw4w9WgXcQ", "")
	if err != nil {
		t.Fatal(err)
	}
	e.InsertBlocks(v)
	tt.Frame()
	tt.Type("note")
	e.SetData(v.ID, "title", "Fetched title")
	tt.Type("s")
	typed := texts(e)
	tt.Key(ui.Cmd, ui.KeyZ)
	if got := e.node(v.ID).Str("title"); got != "Fetched title" {
		t.Errorf("title %q after undoing what was typed since", got)
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	if got := e.node(v.ID).Str("title"); got != "Fetched title" {
		t.Errorf("title %q after undoing the typing", got)
	}
	if got := texts(e); strings.Contains(strings.Join(got, "\n"), "note") {
		t.Errorf("the typing was not undone: %v", got)
	}
	e.Redo()
	e.Redo()
	if got := texts(e); !slices.Equal(got, typed) {
		t.Errorf("after redo %v, want %v", got, typed)
	}
	if got := e.node(v.ID).Str("title"); got != "Fetched title" {
		t.Errorf("title %q after redo", got)
	}
}

func TestALongCodeBlockPastesInLinearTime(t *testing.T) {
	const lines = 4000
	paras := make([]richtext.Paragraph, lines)
	for i := range paras {
		paras[i] = richtext.Paragraph{Style: richtext.ParagraphStyle{CodeBlock: true}, Runs: []richtext.Run{{Text: "    a line of some source code here;"}}}
	}
	text := richtext.TextOf(paras...)
	var blocks []*doc.Node
	perLine := testing.AllocsPerRun(1, func() { blocks = blocksOf(text) }) / lines
	if len(blocks) != 1 || blocks[0].Type != "code" || strings.Count(blocks[0].Delta().Plain(), "\n") != lines-1 {
		t.Fatalf("pasted as %d blocks", len(blocks))
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	before := m.TotalAlloc
	blocksOf(text)
	runtime.ReadMemStats(&m)
	if perLine > 20 || m.TotalAlloc-before > lines*4096 {
		t.Errorf("%.0f allocations and %d bytes a line", perLine, (m.TotalAlloc-before)/lines)
	}
}

func TestCodeLinesBetweenOtherBlocksStayTheirOwnBlocks(t *testing.T) {
	codeLine := func(s string) richtext.Paragraph {
		return richtext.Paragraph{Style: richtext.ParagraphStyle{CodeBlock: true}, Runs: []richtext.Run{{Text: s}}}
	}
	plain := richtext.Paragraph{Runs: []richtext.Run{{Text: "between"}}}
	var got []string
	for _, n := range blocksOf(richtext.TextOf(codeLine("a"), codeLine(""), codeLine("b"), plain, codeLine("c"))) {
		got = append(got, n.Type+":"+n.Delta().Plain())
	}
	if want := []string{"code:a\n\nb", "paragraph:between", "code:c"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestACompositionThatEndsEmptyIsAChange(t *testing.T) {
	e, _ := mentionLine(t)
	changes := 0
	e.OnChange = func() { changes++ }
	e.SetMarkedText(&ui.TextInputRange{Start: 0, End: 2}, "り", ui.TextInputRange{Start: 1, End: 1})
	e.SetMarkedText(nil, "", ui.TextInputRange{})
	if _, marked := e.MarkedRange(); marked || changes == 0 {
		t.Errorf("marked %v, %d changes told", marked, changes)
	}
}
