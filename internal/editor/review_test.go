package editor

import (
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
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
	if got := texts(e); !slices.Equal(got, before) || e.CanUndo() || e.CanRedo() {
		t.Errorf("read-only page went to %v, undo %v, redo %v", got, e.CanUndo(), e.CanRedo())
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
func bigPage(b *testing.B, n int) *Editor {
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
