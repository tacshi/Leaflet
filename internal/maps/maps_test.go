package maps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
)

// serve answers every request with the file's JSON, keeping the requests
// it got.
func serve(t *testing.T, file string, edit func(map[string]any)) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		edit(v)
		raw, _ = json.Marshal(v)
	}
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// local lets the package's client reach test servers on this machine,
// which its public transport refuses.
func local(t *testing.T) {
	was := client
	client = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { client = was })
}

func TestFindPlaces(t *testing.T) {
	local(t)
	srv, got := serve(t, "photon.json", nil)
	was := PhotonURL
	PhotonURL = srv.URL + "/api/"
	t.Cleanup(func() { PhotonURL = was })
	SetVersion("1.2.3")
	places, err := FindPlaces(context.Background(), "Heung Yuen Wai", &doc.LatLng{Lat: 22.38, Lng: 114.19}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 2 || !strings.Contains(places[0].Name, "Heung Yuen Wai") || places[0].Lat < 22.5 || places[0].Lng < 114.1 {
		t.Fatalf("places %+v", places)
	}
	r := (*got)[0]
	if q := r.URL.Query(); q.Get("q") != "Heung Yuen Wai" || q.Get("limit") != "3" || q.Get("lat") != "22.38000" {
		t.Errorf("query %v", r.URL.RawQuery)
	}
	if ua := r.Header.Get("User-Agent"); ua != "Leaflet/1.2.3 (+https://leaflet.capyvibe.app)" {
		t.Errorf("User-Agent %q", ua)
	}
}

func TestFindRoutesByTransit(t *testing.T) {
	local(t)
	srv, got := serve(t, "transitous.json", nil)
	was := TransitousURL
	TransitousURL = srv.URL + "/api/v1/plan"
	t.Cleanup(func() { TransitousURL = was })
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.FixedZone("HKT", 8*3600))
	routes, err := FindRoutes(context.Background(), doc.LatLng{Lat: 22.3825, Lng: 114.1873}, doc.LatLng{Lat: 22.55206, Lng: 114.15382}, doc.RouteTransit, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("%d routes", len(routes))
	}
	b8 := routes[0]
	if b8.Source != doc.RouteFromTransitous || b8.Mode != doc.RouteTransit || len(b8.Legs) != 3 {
		t.Fatalf("route %+v", b8)
	}
	bus := b8.Legs[1]
	if bus.Mode != "bus" || bus.Line != "B8" || bus.Agency != "Citybus" || bus.Color != "#0053b9" || bus.To != "Heung Yuen Wai Port" || bus.DurationS != 2400 {
		t.Errorf("bus leg %+v", bus)
	}
	if b8.Legs[0].From != "" {
		t.Errorf("the start is named %q", b8.Legs[0].From)
	}
	if bus.DistanceM < 20_000 || bus.DistanceM > 40_000 {
		t.Errorf("the bus goes %d m", bus.DistanceM)
	}
	path, err := doc.DecodePath(bus.Path, doc.PathPrecision)
	if err != nil || len(path) < 10 || path[len(path)-1].Lat < 22.5 {
		t.Errorf("path %d points, %v", len(path), err)
	}
	if q := (*got)[0].URL.Query(); q.Get("fromPlace") != "22.382500,114.187300" || q.Get("time") != "2026-11-26T01:00:00Z" {
		t.Errorf("query %v", (*got)[0].URL.RawQuery)
	}
	if data, err := doc.MapData(map[string]any{"route": b8}); err != nil || doc.MapOf(&doc.Node{Type: "map", Data: data}).Route == nil {
		t.Errorf("a map does not take the route: %v", err)
	}
}

func TestFindRoutesByCar(t *testing.T) {
	local(t)
	srv, got := serve(t, "osrm-polyline5.json", func(v map[string]any) {
		r := v["routes"].([]any)[0].(map[string]any)
		path, err := doc.DecodePath(r["geometry"].(string), 5)
		if err != nil {
			t.Fatal(err)
		}
		r["geometry"] = doc.EncodePath(path, 6)
	})
	was := OSRMURL
	OSRMURL = srv.URL
	t.Cleanup(func() { OSRMURL = was })
	routes, err := FindRoutes(context.Background(), doc.LatLng{Lat: 22.3825, Lng: 114.1873}, doc.LatLng{Lat: 22.5464, Lng: 114.1453}, doc.RouteDrive, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Source != doc.RouteFromOSRM || len(routes[0].Legs) != 1 || routes[0].DistanceM < 25_000 {
		t.Fatalf("routes %+v", routes)
	}
	if p := (*got)[0].URL.Path; p != "/routed-car/route/v1/driving/114.187300,22.382500;114.145300,22.546400" {
		t.Errorf("path %s", p)
	}
	if _, err := FindRoutes(context.Background(), doc.LatLng{Lat: 99}, doc.LatLng{}, doc.RouteDrive, time.Time{}); err == nil {
		t.Error("a route from off the map")
	}
	if _, err := FindRoutes(context.Background(), doc.LatLng{Lat: 1}, doc.LatLng{Lat: 2}, "teleport", time.Time{}); err == nil {
		t.Error("a route by teleport")
	}
}

func TestARouteWaitingItsTurnEndsWithItsContext(t *testing.T) {
	local(t)
	srv, got := serve(t, "osrm-polyline5.json", nil)
	was := OSRMURL
	OSRMURL = srv.URL
	osrmEvery.Lock()
	last := osrmEvery.last
	// The last turn given is a while off, as when others wait.
	osrmEvery.last = time.Now().Add(3 * time.Second)
	osrmEvery.Unlock()
	first, cancel := context.WithCancel(context.Background())
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		_, _ = FindRoutes(first, doc.LatLng{Lat: 1}, doc.LatLng{Lat: 2}, doc.RouteDrive, time.Time{})
	}()
	t.Cleanup(func() {
		cancel()
		<-waited
		OSRMURL = was
		osrmEvery.Lock()
		osrmEvery.last = last
		osrmEvery.Unlock()
	})
	time.Sleep(50 * time.Millisecond)
	ctx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	start := time.Now()
	_, err := FindRoutes(ctx, doc.LatLng{Lat: 1}, doc.LatLng{Lat: 2}, doc.RouteDrive, time.Time{})
	if took := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || took > time.Second {
		t.Errorf("a route given up after %v: %v", took, err)
	}
	if len(*got) != 0 {
		t.Errorf("asked %d times before its turn", len(*got))
	}
}

func TestNoRoute(t *testing.T) {
	local(t)
	srv, _ := serve(t, "transitous.json", func(v map[string]any) { v["itineraries"] = []any{} })
	was := TransitousURL
	TransitousURL = srv.URL
	t.Cleanup(func() { TransitousURL = was })
	if _, err := FindRoutes(context.Background(), doc.LatLng{Lat: 1}, doc.LatLng{Lat: 2}, doc.RouteTransit, time.Time{}); err != ErrNoRoute {
		t.Errorf("err %v", err)
	}
}

func TestPathLength(t *testing.T) {
	// A degree of latitude is about 111.2 km.
	if got := PathLength([]doc.LatLng{{Lat: 0, Lng: 0}, {Lat: 1, Lng: 0}}); got < 111_000 || got > 111_400 {
		t.Errorf("a degree of latitude is %.0f m", got)
	}
}

func TestPlaceLine(t *testing.T) {
	for line, want := range map[string]struct {
		name string
		at   *doc.LatLng
	}{
		"Sha Tin Station":                 {"Sha Tin Station", nil},
		"22.3825, 114.1873":               {"", &doc.LatLng{Lat: 22.3825, Lng: 114.1873}},
		"Sha Tin | 22.3825,114.1873":      {"Sha Tin", &doc.LatLng{Lat: 22.3825, Lng: 114.1873}},
		"Bad | 95, 0":                     {"Bad | 95, 0", nil},
		"Pier 9 | Ferry | 22.28, 114.16":  {"Pier 9 | Ferry", &doc.LatLng{Lat: 22.28, Lng: 114.16}},
		"A | B":                           {"A | B", nil},
		"Tsim Sha Tsui, Kowloon":          {"Tsim Sha Tsui, Kowloon", nil},
		"  Heung Yuen Wai Control Point ": {"Heung Yuen Wai Control Point", nil},
	} {
		name, at := PlaceLine(line)
		if name != want.name || (at == nil) != (want.at == nil) || at != nil && *at != *want.at {
			t.Errorf("%q: %q %v, want %q %v", line, name, at, want.name, want.at)
		}
	}
	places := []doc.MapPlace{{Name: "Sha Tin", Lat: 22.3825, Lng: 114.1873}, {Lat: 22.55206, Lng: 114.15382}}
	if got := PlacesText(places); got != "Sha Tin | 22.3825, 114.1873\n22.55206, 114.15382" {
		t.Errorf("text %q", got)
	}
	// A name holding a "|" reads back as it was written.
	if name, at := PlaceLine(PlacesText([]doc.MapPlace{{Name: "Pier 9 | Ferry", Lat: 22.28, Lng: 114.16}})); name != "Pier 9 | Ferry" || at == nil {
		t.Errorf("read back as %q %v", name, at)
	}
}

func TestTransitRouteWithALegItCannotDrawIsLeftOut(t *testing.T) {
	local(t)
	srv, _ := serve(t, "transitous.json", func(v map[string]any) {
		legs := v["itineraries"].([]any)[0].(map[string]any)["legs"].([]any)
		legs[1].(map[string]any)["legGeometry"].(map[string]any)["precision"] = 400
	})
	was := TransitousURL
	TransitousURL = srv.URL
	t.Cleanup(func() { TransitousURL = was })
	routes, err := FindRoutes(context.Background(), doc.LatLng{Lat: 22.3825, Lng: 114.1873}, doc.LatLng{Lat: 22.55206, Lng: 114.15382}, doc.RouteTransit, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || len(routes[0].Legs) != 5 {
		t.Errorf("routes %+v", routes)
	}
}

func TestServiceErrorsSayTheStatus(t *testing.T) {
	local(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 1<<20), http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	was := PhotonURL
	PhotonURL = srv.URL + "/api/"
	t.Cleanup(func() { PhotonURL = was })
	if _, err := FindPlaces(context.Background(), "Sha Tin", nil, 1); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err %v", err)
	}
}

func TestBuild(t *testing.T) {
	local(t)
	places, asked := serve(t, "photon.json", nil)
	routes, routed := serve(t, "osrm-polyline5.json", func(v map[string]any) {
		r := v["routes"].([]any)[0].(map[string]any)
		path, _ := doc.DecodePath(r["geometry"].(string), 5)
		r["geometry"] = doc.EncodePath(path, 6)
	})
	wasPhoton, wasOSRM := PhotonURL, OSRMURL
	PhotonURL, OSRMURL = places.URL, routes.URL
	t.Cleanup(func() { PhotonURL, OSRMURL = wasPhoton, wasOSRM })
	ctx := context.Background()
	m, err := Build(ctx, " Trip ", "Sha Tin | 22.3825, 114.1873\n\nHeung Yuen Wai\n", doc.RouteDrive, doc.Map{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Trip" || len(m.Places) != 2 || m.Places[1].Name != "Heung Yuen Wai" || m.Route == nil || len(*asked) != 1 || len(*routed) != 1 {
		t.Fatalf("map %+v, asked %d, routed %d", m, len(*asked), len(*routed))
	}
	again, err := Build(ctx, "Trip", PlacesText(m.Places), doc.RouteDrive, m)
	if err != nil || again.Route != m.Route || len(*asked) != 1 || len(*routed) != 1 {
		t.Errorf("the same map asked again: %v, asked %d, routed %d", err, len(*asked), len(*routed))
	}
	if off, err := Build(ctx, "Trip", PlacesText(m.Places), "", m); err != nil || off.Route != nil {
		t.Errorf("no route: %+v, %v", off, err)
	}
	if _, err := Build(ctx, "Trip", "\n \n", doc.RouteDrive, doc.Map{}); !errors.Is(err, ErrNoPlaces) {
		t.Errorf("no places: %v", err)
	}
}
