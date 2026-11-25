package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/maps"
)

// MapToolDefinitions are the tools that find places and routes for map
// blocks, offered where agents may use the web, as they ask services on
// it.
func MapToolDefinitions() []map[string]any {
	place := map[string]any{"description": "A place: its name or address, as find_place takes it, or {name, lat, lng} as it found one.",
		"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"}, "lat": map[string]any{"type": "number"}, "lng": map[string]any{"type": "number"},
		}, "required": []string{"lat", "lng"}}}}
	return []map[string]any{
		tool("find_place", "Find a place by its name or address: where it is, its lat and lng, for a map block or find_route. Gives up to 5 places, the likeliest first; near favors those around a point.",
			map[string]any{
				"query": map[string]any{"type": "string", "description": "The place's name or address, with its city when it helps, as \"Heung Yuen Wai Control Point, Hong Kong\"."},
				"near":  map[string]any{"type": "object", "properties": map[string]any{"lat": map[string]any{"type": "number"}, "lng": map[string]any{"type": "number"}}},
			}, "query"),
		tool("find_route", "Find ways from one place to another: walking, cycling, driving, or by public transport, leaving now or at depart_at. Gives each route an id, its time, its length and its legs (for public transport, each line and the stops it goes between). Show a route on the page with a map block whose route is its id, and in your reply with a ```map fence {\"title\", \"route\": id}. Take the times from it; never guess one.",
			map[string]any{
				"from":      place,
				"to":        place,
				"mode":      map[string]any{"type": "string", "enum": doc.RouteModes},
				"depart_at": map[string]any{"type": "string", "description": "When to leave, for public transport: YYYY-MM-DDTHH:MM in the user's own time. Now when not given."},
			}, "from", "to", "mode"),
	}
}

// foundRoute is a route find_route gave, and the places it goes between.
type foundRoute struct {
	route    doc.MapRoute
	from, to doc.MapPlace
}

// RouteBook keeps the routes find_route found, by the ids it gave them, for
// a map to name: a run's, or those of the calls of one leaflet mcp session.
type RouteBook struct {
	mu     sync.Mutex
	routes []foundRoute
}

// maxBookRoutes is how many routes a book keeps: a session's oldest go.
const maxBookRoutes = 40

// add keeps r and gives its id.
func (b *RouteBook) add(r foundRoute) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.routes = append(b.routes, r)
	if gone := len(b.routes) - maxBookRoutes - 1; gone >= 0 {
		b.routes[gone] = foundRoute{}
	}
	return fmt.Sprintf("r%d", len(b.routes))
}

// get is the route of id, as "r2", while the book keeps it.
func (b *RouteBook) get(id string) (foundRoute, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var k int
	id = strings.TrimSpace(id)
	if _, err := fmt.Sscanf(id, "r%d", &k); err != nil || k < 1 || k > len(b.routes) || fmt.Sprintf("r%d", k) != id || k <= len(b.routes)-maxBookRoutes {
		return foundRoute{}, false
	}
	return b.routes[k-1], true
}

// UseRoutes has the executor keep the routes it finds in book, and find
// there those its calls name: as the calls of a leaflet mcp session share
// theirs.
func (x *Executor) UseRoutes(book *RouteBook) { x.book = book }

func (x *Executor) routes() *RouteBook {
	if x.book == nil {
		x.book = &RouteBook{}
	}
	return x.book
}

func (x *Executor) findPlace(args map[string]any) (map[string]any, error) {
	if x.Ctx.FindPlaces == nil {
		return nil, terr("Finding places is off: the user does not let agents use the web.")
	}
	if x.armed {
		return nil, terr(errWebClosed)
	}
	q, err := str(args, "query")
	if err != nil {
		return nil, err
	}
	if addressRe.MatchString(q) {
		return nil, terr("Find a place by its name or address, not a web address.")
	}
	var near *doc.LatLng
	if m, ok := args["near"].(map[string]any); ok {
		if p, ok := latLng(m); ok {
			near = &p
		}
	}
	// What a map service gives is the web's writing, as a page's is: a
	// place's name is anyone's to edit in OpenStreetMap.
	x.saw()
	places, err := x.Ctx.FindPlaces(strings.TrimSpace(q), near)
	if err != nil {
		return nil, terr("The search for places failed: %v", err)
	}
	if places == nil {
		places = []maps.Place{}
	}
	return map[string]any{"places": places}, nil
}

func (x *Executor) findRoute(args map[string]any) (map[string]any, error) {
	if x.Ctx.FindRoutes == nil {
		return nil, terr("Finding routes is off: the user does not let agents use the web.")
	}
	if x.armed {
		return nil, terr(errWebClosed)
	}
	mode, err := str(args, "mode")
	if err != nil {
		return nil, err
	}
	if mode = strings.ToLower(strings.TrimSpace(mode)); !slices.Contains(doc.RouteModes, mode) {
		return nil, terr("mode is one of %s.", strings.Join(doc.RouteModes, ", "))
	}
	var depart time.Time
	if s, _ := args["depart_at"].(string); strings.TrimSpace(s) != "" {
		if depart, err = departAt(strings.TrimSpace(s)); err != nil {
			return nil, err
		}
	}
	from, err := x.routeEnd(args["from"], "from")
	if err != nil {
		return nil, err
	}
	to, err := x.routeEnd(args["to"], "to")
	if err != nil {
		return nil, err
	}
	x.saw()
	routes, err := x.Ctx.FindRoutes(from.At(), to.At(), mode, depart)
	if errors.Is(err, maps.ErrNoRoute) {
		return nil, terr("No route %s was found from %s to %s: try another way of going, another time, or places nearer a road or a stop.", mode, from.Name, to.Name)
	}
	if err != nil {
		return nil, terr("The search for a route failed: %v", err)
	}
	out := []map[string]any{}
	for _, r := range routes {
		view := doc.Map{Route: &r}.View()["route"].(map[string]any)
		view["id"] = x.routes().add(foundRoute{route: r, from: from, to: to})
		out = append(out, view)
	}
	return map[string]any{"from": from, "to": to, "routes": out}, nil
}

// routeEnd is a place find_route goes from or to: one given by its lat and
// lng, or the first place a search for its name finds.
func (x *Executor) routeEnd(v any, key string) (doc.MapPlace, error) {
	switch p := v.(type) {
	case map[string]any:
		at, ok := latLng(p)
		if !ok {
			return doc.MapPlace{}, terr("%s needs lat and lng, in degrees, or a place's name.", key)
		}
		name, _ := p["name"].(string)
		return doc.MapPlace{Name: strings.TrimSpace(name), Lat: at.Lat, Lng: at.Lng}, nil
	case string:
		if strings.TrimSpace(p) == "" {
			break
		}
		found, err := x.findPlace(map[string]any{"query": p})
		if err != nil {
			return doc.MapPlace{}, err
		}
		places, _ := found["places"].([]maps.Place)
		if len(places) == 0 {
			return doc.MapPlace{}, terr("No place named %q was found: find it with find_place, or give its lat and lng.", p)
		}
		return doc.MapPlace{Name: places[0].Name, Lat: places[0].Lat, Lng: places[0].Lng}, nil
	}
	return doc.MapPlace{}, terr("%s is required: a place's name, or {name, lat, lng}.", key)
}

func latLng(m map[string]any) (doc.LatLng, bool) {
	lat, ok1 := m["lat"].(float64)
	lng, ok2 := m["lng"].(float64)
	p := doc.LatLng{Lat: lat, Lng: lng}
	return p, ok1 && ok2 && p.Valid()
}

// departAt reads when a route leaves: a time in the user's own zone, or
// one with its zone.
func departAt(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, terr("depart_at is YYYY-MM-DDTHH:MM, in the user's own time.")
}

// route is the route find_route gave the id, as "r2".
func (x *Executor) route(id string) (foundRoute, bool) { return x.routes().get(id) }

// withRoutes is a call's arguments with each map's route that names the
// id find_route gave it made that route, in its blocks and in the ```map
// fences of its markdown; a map that names no places marks the route's
// ends. A route that names one this run did not find is an error.
func (x *Executor) withRoutes(name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "write_markdown", "insert_blocks", "replace_block", "create_subpage", "create_page":
	default:
		return args, nil
	}
	if !mentionsMap(args) {
		return args, nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	var err error
	if md, ok := out["markdown"].(string); ok {
		if out["markdown"], err = x.mapFences(md); err != nil {
			return nil, err
		}
	}
	if blocks, ok := out["blocks"].([]any); ok {
		list := make([]any, len(blocks))
		for i, b := range blocks {
			if list[i], err = x.mapSpec(b); err != nil {
				return nil, err
			}
		}
		out["blocks"] = list
	}
	if b, ok := out["block"]; ok {
		if out["block"], err = x.mapSpec(b); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ResolveRoutes is a call's arguments with each map's route that names an
// id in book made that route, as the calls of a run's are: for a call that
// writes blocks outside an Executor, as leaflet mcp's create_page.
func ResolveRoutes(book *RouteBook, name string, args map[string]any) (map[string]any, error) {
	x := &Executor{book: book}
	return x.withRoutes(name, args)
}

func mentionsMap(args map[string]any) bool {
	raw, _ := json.Marshal(args)
	return strings.Contains(string(raw), "map")
}

// mapSpec is a block spec with its map's route resolved, and the specs
// nested in it.
func (x *Executor) mapSpec(v any) (any, error) {
	spec, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	out := make(map[string]any, len(spec))
	for k, val := range spec {
		out[k] = val
	}
	if kids, ok := spec["children"].([]any); ok {
		list := make([]any, len(kids))
		for i, k := range kids {
			var err error
			if list[i], err = x.mapSpec(k); err != nil {
				return nil, err
			}
		}
		out["children"] = list
	}
	if t, _ := spec["type"].(string); t != "map" {
		return out, nil
	}
	m, err := x.resolvedMap(asObject(spec["map"]))
	if err != nil {
		return nil, err
	}
	out["map"] = m
	return out, nil
}

func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// resolvedMap is a spec's map with its route resolved: kept as it is
// when it is not an id ("none" takes it off), and its places the route's
// ends when it names none.
func (x *Executor) resolvedMap(m map[string]any) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	id, isID := m["route"].(string)
	switch {
	case !isID:
		return out, nil
	case strings.TrimSpace(id) == "":
		delete(out, "route")
		return out, nil
	case strings.EqualFold(strings.TrimSpace(id), "none"):
		out["route"] = nil
		return out, nil
	}
	found, ok := x.route(id)
	if !ok {
		return nil, terr("No route %q: give a map's route as the id find_route gave it, as \"r1\".", id)
	}
	var route map[string]any
	raw, _ := json.Marshal(found.route)
	_ = json.Unmarshal(raw, &route)
	out["route"] = route
	if places, _ := m["places"].([]any); len(places) == 0 {
		out["places"] = []any{placeSpec(found.from), placeSpec(found.to)}
	}
	return out, nil
}

func placeSpec(p doc.MapPlace) map[string]any {
	return map[string]any{"name": p.Name, "lat": p.Lat, "lng": p.Lng}
}

// mapFences is md with each ```map fence's route resolved; a fence that
// is not JSON stays as it is, as code.
func (x *Executor) mapFences(md string) (string, error) {
	var err error
	out := doc.EditFences(md, "map", func(src string) string {
		var m map[string]any
		if json.Unmarshal([]byte(src), &m) != nil || err != nil {
			return src
		}
		resolved, e := x.resolvedMap(m)
		if e != nil {
			err = e
			return src
		}
		raw, _ := json.Marshal(resolved)
		return string(raw)
	})
	return out, err
}

// Answer is an answer of this run as it is kept: its ```map fences' routes
// resolved, as the chat shows them. A route it names that this run did
// not find stays an id, which the chat shows as code.
func (x *Executor) Answer(reply string) string { return doc.EditFences(reply, "map", x.answerFence) }

// answerFence is the body of an answer's ```map fence as the answer keeps
// it: its route resolved, or as it is.
func (x *Executor) answerFence(src string) string {
	var m map[string]any
	if json.Unmarshal([]byte(src), &m) != nil {
		return src
	}
	resolved, err := x.resolvedMap(m)
	if err != nil {
		return src
	}
	raw, _ := json.Marshal(resolved)
	return string(raw)
}

// streamedAnswer is Answer for an answer as it streams in, again with each
// piece: a fence's route is resolved once, as the pieces repeat it, and one
// not found yet is looked for again, as a later call may find it.
func (x *Executor) streamedAnswer() func(text string) string {
	made := map[string]string{}
	return func(text string) string {
		if !strings.Contains(text, "```") {
			return text
		}
		return doc.EditFences(text, "map", func(src string) string {
			if out, ok := made[src]; ok {
				return out
			}
			out := x.answerFence(src)
			if out != src {
				made[src] = out
			}
			return out
		})
	}
}
