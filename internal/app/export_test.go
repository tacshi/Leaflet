package app

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func TestExportZip(t *testing.T) {
	at := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	pages := []store.Page{
		{ID: "trip", Title: "Trip planning", FolderID: "f", UpdatedAt: at},
		{ID: "hotels", Title: "Hotels", ParentID: "trip", UpdatedAt: at},
		{ID: "notes", Title: "Notes", UpdatedAt: at},
		{ID: "notes2", Title: "Notes", UpdatedAt: at},
	}
	img := &doc.Node{ID: "i", Type: "image", Data: map[string]any{"url": "file:///data/files/x/dawn.jpg", "alt": "dawn"}}
	link := &doc.Node{ID: "s", Type: "subpage", Data: map[string]any{"page_id": "hotels"}}
	mention := &doc.Node{ID: "m", Type: "paragraph"}
	mention.SetDelta(doc.Delta{{Text: "See "}, {Text: "Notes", Attrs: doc.PageMention("notes", "Notes")},
		{Text: " and "}, {Text: "its end", Attrs: doc.Attrs{Href: doc.BlockLink("notes", "b1")}}})
	contents := map[string]*doc.Content{
		"trip":   doc.FromBlocks([]*doc.Node{img, link, mention}),
		"hotels": doc.FromBlocks([]*doc.Node{img.Clone()}),
		"notes":  doc.Empty(),
		"notes2": doc.Empty(),
	}
	var buf bytes.Buffer
	err := exportZip(&buf, pages, map[string]string{"f": "Travel"}, func(id string) *doc.Content { return contents[id] },
		func(p string) ([]byte, error) {
			if p == "/data/files/x/dawn.jpg" {
				return []byte("jpeg"), nil
			}
			return nil, os.ErrNotExist
		})
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
		if !f.Modified.Equal(at) {
			t.Errorf("%s modified %v", f.Name, f.Modified)
		}
	}
	for _, name := range []string{"Travel/Trip planning.md", "Travel/Trip planning/Hotels.md", "Notes.md", "Notes (2).md", "files/dawn.jpg"} {
		if _, ok := got[name]; !ok {
			t.Errorf("no %s in %v", name, keys(got))
		}
	}
	if len(got) != 5 {
		t.Errorf("files %v", keys(got))
	}
	trip := got["Travel/Trip planning.md"]
	if !strings.Contains(trip, "](../files/dawn.jpg)") || !strings.Contains(trip, "](Trip%20planning/Hotels.md)") ||
		!strings.Contains(trip, "See [Notes](../Notes.md) and [its end](../Notes.md#b1)") {
		t.Errorf("trip links:\n%s", trip)
	}
	if !strings.Contains(got["Travel/Trip planning/Hotels.md"], "](../../files/dawn.jpg)") {
		t.Errorf("hotels:\n%s", got["Travel/Trip planning/Hotels.md"])
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
