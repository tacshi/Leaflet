package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/maps"
	"leaflet/locales"
)

type mapLegView struct {
	Walk   bool         `json:"walk"`
	Color  string       `json:"color"`
	Coords [][2]float64 `json:"coords"`
}

type mapView struct {
	Title   string         `json:"title"`
	Places  []doc.MapPlace `json:"places"`
	Legs    []mapLegView   `json:"legs"`
	Summary string         `json:"summary"`
	Steps   string         `json:"steps"`
	Credit  string         `json:"credit"`
	Sources string         `json:"sources"`
	Apple   string         `json:"apple"`
	South   float64        `json:"south"`
	West    float64        `json:"west"`
	North   float64        `json:"north"`
	East    float64        `json:"east"`
}

// MapView is the map block node as the app draws it, in locale's
// language: its places, its legs' points as [lat, lng], each leg's color
// ("" for the app's own), its route in words, whose data it shows, the
// link that opens it in Maps, and the box around it all.
func MapView(node, locale string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	if n.Type != "map" {
		return "", fmt.Errorf("block %s is a %s, not a map", n.ID, n.Type)
	}
	m := doc.MapOf(&n)
	msgs := locales.In(locale)
	v := mapView{Title: m.Title, Places: m.Markers(), Legs: []mapLegView{}, Credit: doc.MapCredit(m), Sources: doc.MapCreditLink(m), Apple: maps.AppleMapsURL(m)}
	if v.Places == nil {
		v.Places = []doc.MapPlace{}
	}
	if r := m.Route; r != nil {
		v.Summary, v.Steps = doc.RouteSummary(msgs, r), doc.RouteSteps(msgs, r)
	}
	for i, path := range m.Paths() {
		leg := m.Route.Legs[i]
		coords := make([][2]float64, len(path))
		for k, p := range path {
			coords[k] = [2]float64{p.Lat, p.Lng}
		}
		v.Legs = append(v.Legs, mapLegView{Walk: leg.Mode == doc.RouteWalk, Color: leg.Color, Coords: coords})
	}
	if sw, ne, ok := m.Bounds(); ok {
		v.South, v.West, v.North, v.East = sw.Lat, sw.Lng, ne.Lat, ne.Lng
	}
	return encode(v)
}

type mapFields struct {
	Title  string `json:"title"`
	Places string `json:"places"`
	Mode   string `json:"mode"`
}

// MapFields is a map block's data as the map editor shows it: its title,
// its places a line each, as "Name | lat, lng", and how its route goes,
// "" for none.
func MapFields(data string) (_ string, err error) {
	defer catch(&err)
	m := mapOfData(data)
	return encode(mapFields{Title: m.Title, Places: maps.PlacesText(m.Places), Mode: maps.RouteMode(m)})
}

func mapOfData(data string) doc.Map {
	var d map[string]any
	if strings.TrimSpace(data) != "" {
		_ = json.Unmarshal([]byte(data), &d)
	}
	return doc.MapOf(&doc.Node{Type: "map", Data: d})
}

// mapBuildWait is the longest a map editor's search may take, a few
// places and a route found one after another.
const mapBuildWait = 2 * time.Minute

// BuildMap makes a map block's data from the map editor's fields, as JSON
// ({title, places, mode}), and was, the data of the map it edits ("" for
// a new one): it asks the map services for the places it does not have
// and the route, so it takes a while, and must not be called on the main
// thread. Cancel with token stops it, as the editor is let go of. Its
// error says, in locale's language, why the map could not be made.
func (c *Core) BuildMap(fields, was, locale string, token int64) (_ string, err error) {
	defer catch(&err)
	var f mapFields
	if err := json.Unmarshal([]byte(fields), &f); err != nil {
		return "", err
	}
	ctx, done := c.cancellable(token, mapBuildWait)
	defer done()
	m, err := maps.Build(ctx, f.Title, f.Places, f.Mode, mapOfData(was))
	if err != nil {
		return "", errors.New(maps.Message(locales.In(locale), err))
	}
	return encode(m.Data())
}
