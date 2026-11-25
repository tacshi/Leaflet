package app

import (
	"net/http"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
)

// pubMaps puts two map blocks in the public page of pubTest, and a third
// with nothing on it, and serves the page again.
func pubMaps(t *testing.T) http.Handler {
	t.Helper()
	a, p, h, pg := pubTest(t)
	c := a.st.Content(pg.ID)
	nanjing := doc.Map{Title: "Nanjing", Places: []doc.MapPlace{{Name: "Nanjing Museum", Lat: 32.0423, Lng: 118.8199}, {Name: "Xinjiekou", Lat: 32.0461, Lng: 118.7922}}}
	east := doc.Map{Places: []doc.MapPlace{{Lat: 32.0602, Lng: 118.8485}, {Lat: 32.0573, Lng: 118.8503}, {Lat: 32.0623, Lng: 118.8347}, {Lat: 32.0228, Lng: 118.7835}}}
	c.Children = append(c.Children, &doc.Node{ID: "m1", Type: "map", Data: nanjing.Data()}, &doc.Node{ID: "m2", Type: "map", Data: east.Data()},
		&doc.Node{ID: "m3", Type: "map", Data: map[string]any{"title": "Nowhere"}})
	if _, _, err := a.st.SaveContent(pg.ID, c); err != nil {
		t.Fatal(err)
	}
	p.render(a.pubJob(p))
	return h
}

func TestPubShowsMapsOnTheirTiles(t *testing.T) {
	h := pubMaps(t)
	page := get(h, "/tok/").Body.String()
	for _, id := range []string{"m1", "m2"} {
		frame := `<iframe src="/tok/m/` + id + `"`
		at := strings.Index(page, `<figure id="b-`+id+`"`)
		if at < 0 || !strings.Contains(page[at:], `<div class="mapframe"><svg class="mapsvg"`) || !strings.Contains(page[at:], frame) {
			t.Fatalf("map %s is not framed over its drawing: %s", id, page)
		}
		w := get(h, "/tok/m/"+id)
		if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != rendered.WebMapCSP ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("map %s's page: %d %v", id, w.Code, w.Header())
		}
		body := w.Body.String()
		for _, name := range []string{"map.js", "map-worker.js", "map.css"} {
			if !strings.Contains(body, `"/tok/`+pubStatic().names[name]+`"`) {
				t.Errorf("map %s's page doesn't load %s from the link's server: %s", id, name, body)
			}
		}
	}
	if strings.Contains(page, `<iframe src="/tok/m/m3"`) || !strings.Contains(page, "<strong>Nowhere</strong>") {
		t.Error("a map with nothing on it framed, or not shown")
	}
	if !strings.Contains(page, `<iframe src="/tok/m/m1" title="Nanjing"`) || !strings.Contains(get(h, "/tok/m/m1").Body.String(), `"name":"Xinjiekou"`) {
		t.Error("a map's frame shows another's")
	}
	// Only the snapshot's maps are served.
	for _, path := range []string{"/tok/m/m3", "/tok/m/nope", "/tok/m/", "/tok/m/p1", "/tok/m/e1", "/tak/m/m1"} {
		if w := get(h, path); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
	if !strings.Contains(pubCSP, "frame-src 'self' ") {
		t.Error("the page's policy doesn't let it frame its maps")
	}
}

func TestPubKeepsMapLibre(t *testing.T) {
	h := pubMaps(t)
	files := rendered.WebMapFiles()
	for name, want := range map[string][2]string{
		"map.js": {files.Script, "text/javascript"}, "map-worker.js": {files.Worker, "text/javascript"}, "map.css": {files.Style, "text/css"},
	} {
		served := pubStatic().names[name]
		if served == name || !strings.HasPrefix(served, strings.TrimSuffix(strings.TrimSuffix(name, ".js"), ".css")+".") {
			t.Errorf("%s is served as %q", name, served)
		}
		w := get(h, "/tok/"+served)
		if w.Code != http.StatusOK || w.Body.String() != want[0] || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), want[1]) {
			t.Errorf("%s: %d %v", served, w.Code, w.Header())
		}
		if w := get(h, "/tok/"+name); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, w.Code)
		}
	}
}
