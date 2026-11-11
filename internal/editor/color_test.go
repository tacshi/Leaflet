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
	if err := tt.Click("Color"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("Ask agent"); !ok || !e.colorOpen {
		t.Fatal("no colors, or the toolbar went away")
	}
	if err := tt.Click("Red"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	d := n.Delta()
	if len(d) != 3 || d[1].Text != "it" || d[1].Attrs.Color != "#ef4444ff" || d[0].Attrs.Color != "" {
		t.Fatalf("delta %+v", d)
	}
	e.SetHighlight("#eab30840")
	spans, _ := e.spans(n, theme.For(false), ui.Font{})
	if spans[1].Color != ui.Hex("#ef4444") || spans[1].Background != ui.Hex("#eab30840") {
		t.Errorf("span %+v", spans[1])
	}
	spans, _ = e.spans(n, theme.For(true), ui.Font{})
	if spans[1].Color != ui.Hex("#f87171") || spans[1].Background != ui.Hex("#eab30840") {
		t.Errorf("dark span %+v", spans[1])
	}
	r := boxOf(e, n.ID)
	tt.RightClickAt(r.X+20, r.Y+r.H/2)
	if err := chooseDrawn(tt, "Remove color"); err != nil {
		t.Fatalf("%v; shown %v", err, tt.Texts())
	}
	if d := n.Delta(); len(d) != 1 || !d[0].Attrs.Zero() {
		t.Errorf("after Remove color %+v", d)
	}
}

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
	if !e.Focused() {
		t.Error("the toolbar took the focus")
	}
}

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

func TestSelectionView(t *testing.T) {
	e, tt, _ := setup(t)
	tt.Type("ship it now")
	n := e.Content.Children[0]
	calls := 0
	e.OnSelect = func() { calls++ }

	e.Select(Pos{n.ID, 5}, Pos{n.ID, 7})
	if calls == 0 {
		t.Error("the selection changed unheard")
	}
	e.SetColor("#ef4444ff")
	v := e.SelectionView()
	if v.Text != "it" || v.Start != (Pos{n.ID, 5}) || v.End != (Pos{n.ID, 7}) || v.Color != "#ef4444ff" || v.Mixed {
		t.Fatalf("selection %+v", v)
	}

	e.Select(Pos{n.ID, 7}, Pos{n.ID, 2})
	if v := e.SelectionView(); v.Text != "ip it" || v.Start.Off != 2 || v.End.Off != 7 || v.Color != "" || !v.Mixed {
		t.Fatalf("mixed selection %+v", v)
	}

	e.Select(Pos{n.ID, 11}, Pos{n.ID, 11})
	calls = 0
	e.SetColor("#3b82f6ff")
	if v := e.SelectionView(); v.Text != "" || v.Color != "#3b82f6ff" || v.Mixed || calls == 0 {
		t.Fatalf("caret %+v, heard %d", v, calls)
	}
}
