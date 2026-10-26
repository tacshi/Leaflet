package editor

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
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
	spans := e.spans(n, nil, ui.Font{})
	if spans[1].Color != ui.Hex("#ef4444") || spans[1].Background != ui.Hex("#eab30840") {
		t.Errorf("span %+v", spans[1])
	}
	// Remove color, from the context menu, takes both away.
	r := e.geom[n.ID]
	tt.RightClickAt(r.X+20, r.Y+r.H/2)
	if err := tt.ChooseMenuItem("Remove color"); err != nil {
		t.Fatalf("%v; menu %v", err, tt.Menu())
	}
	if d := n.Delta(); len(d) != 1 || !d[0].Attrs.Zero() {
		t.Errorf("after Remove color %+v", d)
	}
	if !doc.ValidColor(colorHex(ui.Hex("#123456"))) || colorHex(ui.Color{}) != "" {
		t.Error("colorHex")
	}
}
