package mobile

import (
	"encoding/json"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/maps"
)

func TestMapViewAndFields(t *testing.T) {
	path := doc.EncodePath([]doc.LatLng{{Lat: 22.3825, Lng: 114.1873}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	m := doc.Map{Title: "Trip", Places: []doc.MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}, {Name: "Border", Lat: 22.55206, Lng: 114.15382}},
		Route: &doc.MapRoute{Mode: doc.RouteDrive, Source: doc.RouteFromOSRM, DurationS: 1620, DistanceM: 29_900, Legs: []doc.MapLeg{{Mode: "drive", DurationS: 1620, Path: path}}}}
	node, _ := json.Marshal(&doc.Node{ID: "m", Type: "map", Data: m.Data()})
	out, err := MapView(string(node), "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	var v mapView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Summary != "驾车 · 27 分钟 · 29.9 公里" || len(v.Legs) != 1 || len(v.Legs[0].Coords) != 2 || v.Legs[0].Coords[1][0] != 22.55206 || v.North < 22.55 {
		t.Errorf("view %+v", v)
	}
	if !strings.HasPrefix(v.Apple, "https://maps.apple.com/?") || !strings.Contains(v.Apple, "dirflg=d") {
		t.Errorf("apple %q", v.Apple)
	}
	data, _ := json.Marshal(m.Data())
	fields, err := MapFields(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if fields != `{"title":"Trip","places":"Sha Tin | 22.3825, 114.1873\nBorder | 22.55206, 114.15382","mode":"drive"}` {
		t.Errorf("fields %s", fields)
	}
	c := &Core{}
	built, err := c.BuildMap(`{"title":"Trip","places":"Sha Tin | 22.3825, 114.1873\nBorder | 22.55206, 114.15382","mode":"drive"}`, string(data), "en", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built, `"title":"Trip"`) || !strings.Contains(built, `"route"`) {
		t.Errorf("built %s", built)
	}
	if _, err := c.BuildMap(`{"title":"x","places":"","mode":""}`, "", "ja", 0); err == nil || err.Error() != "場所を1つ以上入れてください。" {
		t.Errorf("no places: %v", err)
	}
}

func TestBuildMapStopsWhenCancelled(t *testing.T) {
	was := maps.PhotonURL
	maps.PhotonURL = "https://photon.invalid/api/"
	t.Cleanup(func() { maps.PhotonURL = was })
	c := &Core{}
	// Given up on before it begins, as a map editor closed at once: it
	// asks no service and fails.
	c.Cancel(7)
	_, err := c.BuildMap(`{"title":"x","places":"Sha Tin","mode":""}`, "", "en", 7)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("cancelled: %v", err)
	}
}
