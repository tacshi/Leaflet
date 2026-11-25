// Package maps finds places and routes for map blocks, from services that
// need no key and ask only fair use: Photon (komoot) for places, the
// FOSSGIS servers' OSRM for walking, cycling and driving, and Transitous
// for public transport. Each request names Leaflet, its version and its
// site, as Transitous and FOSSGIS ask, and is made only as a person or
// their agent asks for a place or a route.
package maps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/web"
)

// Site is Leaflet's site, which requests give as the way to reach it.
const Site = "https://leaflet.capyvibe.app"

var agent atomic.Pointer[string]

// SetVersion names the app's version in the User-Agent of its requests.
func SetVersion(version string) {
	s := "Leaflet/" + strings.TrimSpace(version) + " (+" + Site + ")"
	if strings.TrimSpace(version) == "" {
		s = "Leaflet (+" + Site + ")"
	}
	agent.Store(&s)
}

func userAgent() string {
	if s := agent.Load(); s != nil {
		return *s
	}
	return "Leaflet (+" + Site + ")"
}

// The services' addresses, which tests point elsewhere.
var (
	PhotonURL     = "https://photon.komoot.io/api/"
	OSRMURL       = "https://routing.openstreetmap.de"
	TransitousURL = "https://api.transitous.org/api/v1/plan"
)

var client = &http.Client{Transport: web.PublicTransport(), Timeout: 20 * time.Second}

// ErrNoRoute is the error of a search that found no way between two
// places.
var ErrNoRoute = errors.New("no route was found between these places")

// osrmEvery keeps requests to FOSSGIS's OSRM servers a second apart, as
// they ask: last is the time of the last turn given.
var osrmEvery = struct {
	sync.Mutex
	last time.Time
}{}

// osrmTurn waits for a request's turn at the OSRM servers, a second after
// the last turn given, or until ctx ends. The turn is given at once, so
// that the requests waiting each wait on their own, and each can end.
func osrmTurn(ctx context.Context) error {
	osrmEvery.Lock()
	turn := osrmEvery.last.Add(time.Second)
	if now := time.Now(); turn.Before(now) {
		turn = now
	}
	osrmEvery.last = turn
	osrmEvery.Unlock()
	wait := time.NewTimer(time.Until(turn))
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wait.C:
		return nil
	}
}

func get(ctx context.Context, link string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s is busy: try again in a minute", hostOf(link))
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %s", hostOf(link), res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func hostOf(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	return u.Host
}

// Place is a place a search found.
type Place struct {
	Name    string  `json:"name"`
	Address string  `json:"address,omitempty"`
	Kind    string  `json:"kind,omitempty"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// FindPlaces asks Photon for the places query names, at most limit, those
// near near first when it is given.
func FindPlaces(ctx context.Context, query string, near *doc.LatLng, limit int) ([]Place, error) {
	q := url.Values{"q": {strings.TrimSpace(query)}, "limit": {fmt.Sprint(min(max(limit, 1), 10))}}
	if near != nil && near.Valid() {
		q.Set("lat", fmt.Sprintf("%.5f", near.Lat))
		q.Set("lon", fmt.Sprintf("%.5f", near.Lng))
	}
	var res struct {
		Features []struct {
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := get(ctx, PhotonURL+"?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	var out []Place
	for _, f := range res.Features {
		if len(f.Geometry.Coordinates) < 2 {
			continue
		}
		p := Place{Lng: f.Geometry.Coordinates[0], Lat: f.Geometry.Coordinates[1]}
		str := func(k string) string { s, _ := f.Properties[k].(string); return strings.TrimSpace(s) }
		p.Name = str("name")
		var addr []string
		if street := strings.TrimSpace(str("housenumber") + " " + str("street")); street != "" {
			addr = append(addr, street)
		}
		for _, k := range []string{"district", "city", "state", "country"} {
			if s := str(k); s != "" && s != p.Name {
				addr = append(addr, s)
			}
		}
		p.Address = strings.Join(addr, ", ")
		if p.Name == "" {
			p.Name = p.Address
		}
		p.Kind = strings.TrimSpace(str("osm_value"))
		if p.Name != "" && (doc.LatLng{Lat: p.Lat, Lng: p.Lng}).Valid() {
			out = append(out, p)
		}
	}
	return out, nil
}

// osrmProfile is the FOSSGIS server and profile of a way of going.
var osrmProfile = map[string]string{
	doc.RouteDrive: "routed-car/route/v1/driving",
	doc.RouteCycle: "routed-bike/route/v1/bike",
	doc.RouteWalk:  "routed-foot/route/v1/foot",
}

// FindRoutes finds ways from one place to another, going as mode says: a
// few for public transport, leaving at depart or now, and one for the
// others.
func FindRoutes(ctx context.Context, from, to doc.LatLng, mode string, depart time.Time) ([]doc.MapRoute, error) {
	if !from.Valid() || !to.Valid() {
		return nil, errors.New("a route needs two places on the map")
	}
	if mode == doc.RouteTransit {
		return transitRoutes(ctx, from, to, depart)
	}
	profile, ok := osrmProfile[mode]
	if !ok {
		return nil, fmt.Errorf("a route goes by %s", strings.Join(doc.RouteModes, ", "))
	}
	if err := osrmTurn(ctx); err != nil {
		return nil, err
	}
	link := fmt.Sprintf("%s/%s/%.6f,%.6f;%.6f,%.6f?overview=full&geometries=polyline6&steps=false&alternatives=false",
		OSRMURL, profile, from.Lng, from.Lat, to.Lng, to.Lat)
	var res struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry string  `json:"geometry"`
		} `json:"routes"`
	}
	if err := get(ctx, link, &res); err != nil {
		return nil, err
	}
	if res.Code != "Ok" || len(res.Routes) == 0 {
		return nil, ErrNoRoute
	}
	r := res.Routes[0]
	leg := doc.MapLeg{Mode: mode, DurationS: int(r.Duration), DistanceM: int(r.Distance), Path: r.Geometry}
	route := doc.MapRoute{Mode: mode, Source: doc.RouteFromOSRM, DurationS: leg.DurationS, DistanceM: leg.DistanceM, Legs: []doc.MapLeg{leg}}
	if err := checked(&route); err != nil {
		return nil, err
	}
	return []doc.MapRoute{route}, nil
}

// transitModes are the legs' modes of Transitous (MOTIS) as a map's.
var transitModes = map[string]string{
	"WALK": "walk", "BIKE": "cycle", "CAR": "drive", "BUS": "bus", "COACH": "coach", "TRAM": "tram",
	"SUBWAY": "subway", "METRO": "subway", "RAIL": "rail", "REGIONAL_RAIL": "rail", "REGIONAL_FAST_RAIL": "rail",
	"HIGHSPEED_RAIL": "rail", "LONG_DISTANCE": "rail", "NIGHT_RAIL": "rail", "SUBURBAN": "rail", "FERRY": "ferry",
	"AIRPLANE": "air", "FUNICULAR": "cable", "AERIAL_LIFT": "cable", "CABLE_CAR": "cable",
}

// maxTransitRoutes is how many ways by public transport a search gives.
const maxTransitRoutes = 4

func transitRoutes(ctx context.Context, from, to doc.LatLng, depart time.Time) ([]doc.MapRoute, error) {
	if depart.IsZero() {
		depart = time.Now()
	}
	q := url.Values{
		"fromPlace": {fmt.Sprintf("%.6f,%.6f", from.Lat, from.Lng)},
		"toPlace":   {fmt.Sprintf("%.6f,%.6f", to.Lat, to.Lng)},
		"time":      {depart.UTC().Format(time.RFC3339)},
		"arriveBy":  {"false"},
	}
	var res struct {
		Itineraries []struct {
			Duration float64 `json:"duration"`
			Legs     []struct {
				Mode           string  `json:"mode"`
				Duration       float64 `json:"duration"`
				Distance       float64 `json:"distance"`
				RouteShortName string  `json:"routeShortName"`
				AgencyName     string  `json:"agencyName"`
				RouteColor     string  `json:"routeColor"`
				From           struct {
					Name string `json:"name"`
				} `json:"from"`
				To struct {
					Name string `json:"name"`
				} `json:"to"`
				LegGeometry struct {
					Points    string `json:"points"`
					Precision int    `json:"precision"`
				} `json:"legGeometry"`
			} `json:"legs"`
		} `json:"itineraries"`
	}
	if err := get(ctx, TransitousURL+"?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	var out []doc.MapRoute
	for _, it := range res.Itineraries {
		route := doc.MapRoute{Mode: doc.RouteTransit, Source: doc.RouteFromTransitous, DurationS: int(it.Duration)}
		whole := true
		for _, l := range it.Legs {
			precision := l.LegGeometry.Precision
			if precision == 0 {
				precision = 7
			}
			path, err := doc.DecodePath(l.LegGeometry.Points, precision)
			if err != nil {
				// A leg whose path cannot be read would leave a gap in
				// the route, a bus ride missing from its steps.
				whole = false
				break
			}
			if len(path) < 2 {
				continue
			}
			mode, ok := transitModes[strings.ToUpper(l.Mode)]
			if !ok {
				mode = "transit"
			}
			distance := l.Distance
			if distance <= 0 {
				distance = PathLength(path)
			}
			leg := doc.MapLeg{Mode: mode, Line: strings.TrimSpace(l.RouteShortName), Agency: strings.TrimSpace(l.AgencyName),
				From: strings.TrimSpace(l.From.Name), To: strings.TrimSpace(l.To.Name),
				DurationS: int(l.Duration), DistanceM: int(distance), Path: doc.EncodePath(path, doc.PathPrecision)}
			if c := strings.TrimPrefix(strings.TrimSpace(l.RouteColor), "#"); len(c) == 6 && doc.ValidColor("#"+c) {
				leg.Color = "#" + strings.ToLower(c)
			}
			if leg.From == "START" {
				leg.From = ""
			}
			if leg.To == "END" {
				leg.To = ""
			}
			route.DistanceM += leg.DistanceM
			route.Legs = append(route.Legs, leg)
		}
		if !whole || len(route.Legs) == 0 || checked(&route) != nil {
			continue
		}
		out = append(out, route)
		if len(out) == maxTransitRoutes {
			break
		}
	}
	if len(out) == 0 {
		return nil, ErrNoRoute
	}
	return out, nil
}

// PathLength is how long a path is, in meters, along the Earth's surface.
func PathLength(path []doc.LatLng) float64 {
	const radius = 6_371_008.8
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	var total float64
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		dLat, dLng := rad(b.Lat-a.Lat), rad(b.Lng-a.Lng)
		h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLng/2)*math.Sin(dLng/2)
		total += 2 * radius * math.Asin(math.Min(1, math.Sqrt(h)))
	}
	return total
}

// checked is the route a map takes, or why a map cannot take it.
func checked(r *doc.MapRoute) error {
	data, err := doc.MapData(map[string]any{"route": r})
	if err != nil {
		return err
	}
	*r = *doc.MapOf(&doc.Node{Type: "map", Data: data}).Route
	return nil
}
