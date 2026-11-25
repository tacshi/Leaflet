package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	mapsvc "leaflet/internal/maps"
	"leaflet/locales"
)

func testMap() doc.Map {
	walk := doc.EncodePath([]doc.LatLng{{Lat: 22.3825, Lng: 114.1873}, {Lat: 22.3784, Lng: 114.1918}}, doc.PathPrecision)
	ride := doc.EncodePath([]doc.LatLng{{Lat: 22.3784, Lng: 114.1918}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	return doc.Map{Title: "To the border", Places: []doc.MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}, {Name: "Heung Yuen Wai", Lat: 22.55206, Lng: 114.15382}},
		Route: &doc.MapRoute{Mode: doc.RouteTransit, Source: doc.RouteFromTransitous, DurationS: 3240, DistanceM: 27_400, Legs: []doc.MapLeg{
			{Mode: "walk", DurationS: 780, Path: walk}, {Mode: "bus", Line: "B8", DurationS: 2400, Path: ride}}}}
}

func TestMapsInAnswers(t *testing.T) {
	a := &App{}
	var o markdown.Options
	o.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"map": a.chatMap}
	raw, _ := json.Marshal(testMap())
	md := fmt.Sprintf("Take the B8:\n\n```map\n%s\n```\n\n```map\n{\"route\": \"r1\"}\n```\n\nAgain:\n\n```map\n%s\n```\n", raw, raw)
	var built []string
	tt := ui.NewTester(func(c *ui.Context) {
		// As the app's view does, the maps a pass did not build go after it.
		defer a.dropMaps()
		ui.Column(c).Fill().Padding(20).Children(func() { markdownView(c, md, &o).FontSize(13.5) })
		built = slices.Sorted(maps.Keys(a.mapViews.built))
	}, 600, 900)
	tt.Frame()
	tt.Frame()
	for _, text := range []string{"To the border", "Public transport · 54 min · 27.4 km", "Walk 13 min → Bus B8 40 min", `{"route": "r1"}`} {
		if _, ok := tt.Find(text); !ok {
			t.Errorf("no %q in %v", text, tt.Texts())
		}
	}
	if len(built) != 2 || built[1] != built[0]+"#2" {
		t.Errorf("the same map twice built %v", built)
	}
	if len(a.mapViews.built) != 0 {
		t.Errorf("built after the pass: %v", a.mapViews.built)
	}
}

func TestMapErrors(t *testing.T) {
	for err, want := range map[error]string{
		&mapsvc.NotFoundError{Name: "Atlantis"}: "No place named Atlantis was found.",
		mapsvc.ErrNoPlaces:                      "Name at least one place.",
		mapsvc.ErrNoRoute:                       "No route was found that way from the first place to the last.",
		errors.New("photon.komoot.io is busy"):  "The map could not be made: photon.komoot.io is busy",
	} {
		if got := mapError(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}

func TestMapDialogMakesAMapOfPlacesPinned(t *testing.T) {
	a := templatesApp(t)
	a.session.DisplayName = "Rocky"
	para := &doc.Node{ID: "p", Type: "paragraph"}
	ed := editor.New(doc.FromBlocks([]*doc.Node{para}), &host{a: a})
	d := &a.dialogs
	d.openMap("", ed)
	d.mapTitle, d.mapPlaces = "Walk", "Sha Tin | 22.3825, 114.1873\n\nTai Wai | 22.3727, 114.1786"
	a.saveMap()
	if d.mapOpen || d.mapError != "" {
		t.Fatalf("open %v, error %q", d.mapOpen, d.mapError)
	}
	var made *doc.Node
	ed.Content.Walk(func(n, _ *doc.Node) {
		if n.Type == "map" {
			made = n
		}
	})
	if made == nil {
		t.Fatal("no map in the page")
	}
	if m := doc.MapOf(made); m.Title != "Walk" || len(m.Places) != 2 || m.Places[1].Name != "Tai Wai" || m.Route != nil || made.Str("author") != "Rocky" {
		t.Errorf("map %+v by %q", m, made.Str("author"))
	}
	d.openMap(made.ID, ed)
	if d.mapTitle != "Walk" || d.mapPlaces != "Sha Tin | 22.3825, 114.1873\nTai Wai | 22.3727, 114.1786" || d.mapMode != 0 {
		t.Errorf("reopened on %q, %q, mode %d", d.mapTitle, d.mapPlaces, d.mapMode)
	}
	d.mapPlaces = ""
	a.saveMap()
	if !d.mapOpen || d.mapError != "Name at least one place." {
		t.Errorf("no places: open %v, error %q", d.mapOpen, d.mapError)
	}
}

func TestMapDialogLetsGoOfItsSearch(t *testing.T) {
	a := templatesApp(t)
	ed := editor.New(doc.FromBlocks([]*doc.Node{{ID: "p", Type: "paragraph"}}), &host{a: a})
	d := &a.dialogs
	stopped := 0
	searching := func() int {
		d.mapBusy, d.mapCancel = true, func() { stopped++ }
		return d.mapRun
	}
	// Closed by its ✕, Escape or a click outside while it searches: the
	// next frame stops the search.
	d.openMap("", ed)
	run := searching()
	d.mapOpen = false
	tt := ui.NewTester(func(c *ui.Context) { d.view(c, a) }, 600, 400)
	tt.Frame()
	if stopped != 1 || d.mapBusy || d.mapCancel != nil || d.mapRun == run {
		t.Errorf("closed: stopped %d, busy %v, run %d after %d", stopped, d.mapBusy, d.mapRun, run)
	}
	// Opened again while it searches, as for another map.
	d.openMap("", ed)
	run = searching()
	d.openMap("", ed)
	if stopped != 2 || d.mapBusy || d.mapRun == run {
		t.Errorf("reopened: stopped %d, busy %v", stopped, d.mapBusy)
	}
	// Its Cancel button.
	run = searching()
	tt = ui.NewTester(func(c *ui.Context) { d.view(c, a) }, 600, 600)
	tt.Frame()
	if err := tt.Click(locales.CommonCancel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if stopped != 3 || d.mapOpen || d.mapRun == run {
		t.Errorf("cancelled: stopped %d, open %v", stopped, d.mapOpen)
	}
}
