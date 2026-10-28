package app

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
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
		"notes2": doc.FromBlocks([]*doc.Node{{ID: "s", Type: "image", Data: map[string]any{
			"url": "file:///elsewhere/files/sun/sun.jpg", "alt": "sun", "file_id": "sun"}}}),
	}
	var buf bytes.Buffer
	err := exportZip(&buf, pages, map[string]string{"f": "Travel"}, func(id string) *doc.Content { return contents[id] },
		func(id string) string {
			if id == "sun" {
				return "/data/files/sun/sun.jpg"
			}
			return ""
		},
		func(p string) ([]byte, error) {
			if p == "/data/files/x/dawn.jpg" || p == "/data/files/sun/sun.jpg" {
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
	for _, name := range []string{"Travel/Trip planning.md", "Travel/Trip planning/Hotels.md", "Notes.md", "Notes (2).md", "files/dawn.jpg", "files/sun.jpg"} {
		if _, ok := got[name]; !ok {
			t.Errorf("no %s in %v", name, keys(got))
		}
	}
	if len(got) != 6 {
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
	if !strings.Contains(got["Notes (2).md"], "](files/sun.jpg)") {
		t.Errorf("notes 2:\n%s", got["Notes (2).md"])
	}
}

// Pages and folders named "." or ".." stay inside the zip.
func TestExportZipDotNames(t *testing.T) {
	pages := []store.Page{
		{ID: "up", Title: ".."},
		{ID: "notes", Title: "Notes", ParentID: "up"},
		{ID: "here", Title: ".", FolderID: "f"},
	}
	var buf bytes.Buffer
	err := exportZip(&buf, pages, map[string]string{"f": ".."}, func(string) *doc.Content { return doc.Empty() },
		func(string) string { return "" }, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	if want := []string{"--.md", "--/-.md", "--/Notes.md"}; !slices.Equal(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
}

// Export All takes the app's own files, and no file elsewhere that a
// block links to.
func TestExportOnlyOwnFiles(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret, pic := filepath.Join(outside, "id_rsa"), filepath.Join(outside, "pic.png")
	for _, p := range []string{secret, pic} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := st.AddFile(pic, "", "", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	blocks := []*doc.Node{
		{ID: "a", Type: "image", Data: map[string]any{"url": "file://" + f.LocalPath, "file_id": f.ID}},
		{ID: "b", Type: "file", Data: map[string]any{"url": "file://" + secret, "name": "id_rsa"}},
	}
	if _, err := st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks(blocks)}); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Config(), st)
	a := &App{st: st, backend: be, sync: syncsvc.New(st, be), session: st.Session()}
	a.reloadAll()
	out := filepath.Join(t.TempDir(), "export.zip")
	if _, err := a.writeExport(out, a.folders, a.files); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, "files/pic.png") || slices.Contains(names, "files/id_rsa") {
		t.Errorf("exported %q", names)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
