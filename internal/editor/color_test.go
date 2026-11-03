package editor

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/doc"
	"leaflet/internal/theme"
)

func TestTextColorAndHighlight(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship it now")
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 5}, Pos{n.ID, 7})
	tt.Frame()
	// The toolbar's Color button opens dots of colors, and the toolbar
	// stays while they show.
	if err := tt.Click("Color"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("Ask agent"); !ok || !e.colorOpen {
		t.Fatal("no colors, or the toolbar went away")
	}
	if err := tt.Click("Red"); err != nil { // the text row's
		t.Fatal(err)
	}
	tt.Frame()
	d := n.Delta()
	if len(d) != 3 || d[1].Text != "it" || d[1].Attrs.Color != "#ef4444ff" || d[0].Attrs.Color != "" {
		t.Fatalf("delta %+v", d)
	}
	e.SetHighlight("#eab30840")
	spans := e.spans(n, theme.For(false), ui.Font{})
	if spans[1].Color != ui.Hex("#ef4444") || spans[1].Background != ui.Hex("#eab30840") {
		t.Errorf("span %+v", spans[1])
	}
	// In dark, the text shows in the color's lighter shade, which reads
	// on dark paper; the highlight is as it was.
	spans = e.spans(n, theme.For(true), ui.Font{})
	if spans[1].Color != ui.Hex("#f87171") || spans[1].Background != ui.Hex("#eab30840") {
		t.Errorf("dark span %+v", spans[1])
	}
	// Remove color, from the context menu, takes both away.
	r := boxOf(e, n.ID)
	tt.RightClickAt(r.X+20, r.Y+r.H/2)
	if err := chooseDrawn(tt, "Remove color"); err != nil {
		t.Fatalf("%v; shown %v", err, tt.Texts())
	}
	if d := n.Delta(); len(d) != 1 || !d[0].Attrs.Zero() {
		t.Errorf("after Remove color %+v", d)
	}
}

// Code is colored by MyGo's highlighting, in its code theme's colors, and
// the spans keep the text exactly.
func TestCodeSpansUseMyGo(t *testing.T) {
	pal := theme.For(false)
	code := "func main() {\n\t// hi\n}"
	spans := codeSpans(code, "golang", ui.Font{Family: "monospace", Size: 14}, pal)
	text, keyword, comment := "", false, false
	ct := CodeTheme(pal)
	for _, s := range spans {
		text += s.Text
		if s.Text == "func" && s.Color == ct.TokenColor(codeeditor.TokenKeyword) {
			keyword = true
		}
		if s.Text == "// hi" && s.Color == ct.TokenColor(codeeditor.TokenComment) {
			comment = true
		}
	}
	if text != code || !keyword || !comment {
		t.Errorf("text %q keyword %v comment %v: %+v", text, keyword, comment, spans)
	}
	if got := codeSpans("x", "brainfuck", ui.Font{}, pal); len(got) != 1 || got[0].Color != (ui.Color{}) {
		t.Errorf("an unknown language is not plain: %+v", got)
	}
}

// The selection toolbar is MyGo's: its Bold and Italic toggles format the
// selected text and show pressed while it is.
func TestSelectionToolbar(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship it now")
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 5}, Pos{n.ID, 7})
	tt.Frame()
	for _, label := range []string{"Bold", "Italic"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	d := n.Delta()
	if len(d) != 3 || !d[1].Attrs.Bold || !d[1].Attrs.Italic || d[0].Attrs.Bold {
		t.Fatalf("delta %+v", d)
	}
	if !e.Has(func(a doc.Attrs) bool { return a.Bold }) {
		t.Error("Bold does not show pressed")
	}
	// The keys stayed in the page.
	if !e.Focused() {
		t.Error("the toolbar took the focus")
	}
}

// The selection toolbar's first button copies the selection as plain
// text, says it did, and leaves the selection and the focus in the page;
// in code, which takes no formats, it is the toolbar's one copy button.
func TestSelectionToolbarCopiesPlainText(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship **it** now")
	n := e.Content.Children[0]
	e.Select(Pos{n.ID, 0}, Pos{n.ID, 7})
	tt.Frame()
	if err := tt.Click("Copy as Plain Text"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got, formats := tt.Clipboard(), tt.ClipboardData().Formats(); got != "ship it" || len(formats) != 1 {
		t.Errorf("copied %q as %v", got, formats)
	}
	if _, ok := tt.Find("Copied"); !ok {
		t.Error("the button does not say it copied")
	}
	if e.Collapsed() || !e.Focused() {
		t.Error("copying lost the selection or the focus")
	}

	code := doc.CodeNode("a := 1", "go")
	e.Edit(func(c *doc.Content) { c.Children = []*doc.Node{code} })
	e.Select(Pos{code.ID, 0}, Pos{code.ID, 1})
	e.plainCopiedAt = time.Time{}
	tt.Frame()
	if _, ok := tt.Find("Bold"); ok {
		t.Error("code offers formats")
	}
	if err := tt.Click("Copy as Plain Text"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "a" {
		t.Errorf("copied %q from code", got)
	}
}
