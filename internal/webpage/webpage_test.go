package webpage

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A printed page holds its own pictures, and no other page's; it names
// its files, which paper can't download, and shows no player for its
// recordings; it runs only its own scripts.
func TestPrintedPage(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) string {
		path := filepath.Join(st.Dir, "files", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	pic, theirs := write("dawn.png", "\x89PNG\r\n\x1a\nown"), write("dusk.png", "\x89PNG\r\n\x1a\ntheirs")
	notes, wav := write("notes.pdf", "0123456789"), write("r1.wav", "RIFF")
	other, err := st.CreatePage(store.NewPageOptions{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	c := doc.FromBlocks([]*doc.Node{
		{ID: "i1", Type: "image", Data: map[string]any{"url": "file://" + pic, "file_id": "dawn"}},
		{ID: "i2", Type: "image", Data: map[string]any{"url": "file://" + theirs, "file_id": "dusk"}},
		{ID: "fl", Type: "file", Data: map[string]any{"file_id": "f1", "name": "Notes.pdf"}},
		{ID: "rec", Type: "recording", Data: map[string]any{"recording_id": "r1"}},
		{ID: "m", Type: "math_equation", Data: map[string]any{"formula": "x^2"}},
	})
	pg, err := st.CreatePage(store.NewPageOptions{Title: "Trip", Content: c})
	if err != nil {
		t.Fatal(err)
	}
	files := []store.File{
		{ID: "dawn", PageID: pg.ID, Name: "dawn.png", LocalPath: pic, MimeType: "image/png"},
		{ID: "dusk", PageID: other.ID, Name: "dusk.png", LocalPath: theirs, MimeType: "image/png"},
		{ID: "f1", PageID: pg.ID, Name: "Notes.pdf", LocalPath: notes, MimeType: "application/pdf", SizeBytes: 2048},
	}
	for _, f := range files {
		if err := st.PutFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutRecording(store.Recording{ID: "r1", PageID: pg.ID, BlockID: "rec", LocalPath: wav, DurationMs: 65000, Status: "ready",
		Transcript: []store.Segment{{StartMs: 1500, EndMs: 4000, Text: "Hello there."}}}); err != nil {
		t.Fatal(err)
	}
	page := HTML(c, Options{Store: st, Page: pg.ID, Title: "Trip", Files: files, Serve: Inline, Print: true})
	own := `<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nown")) + `"`
	for _, want := range []string{
		own, `<div id="b-fl" data-block="fl" class="card file">`, locales.CardFileDetail("PDF", FileSize(2048)),
		`<span class="muted">1:05</span>`, "Hello there.",
		`<meta http-equiv="Content-Security-Policy" content="` + PrintCSP + `">`,
		"<script>" + rendered.PageMathJS + "</script>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\ntheirs")), "<audio", "href=\"\"", "download=", st.Dir} {
		if strings.Contains(page, bad) {
			t.Errorf("has %q", bad)
		}
	}
	if !strings.HasPrefix(PrintCSP, "default-src 'none'; script-src 'sha256-") || !strings.HasSuffix(PrintCSP, "; img-src data: https:") {
		t.Errorf("policy %q", PrintCSP)
	}
}

// The Americas' Letter regions print on Letter, the rest of the world on
// A4, as does a locale that names no region.
func TestPaper(t *testing.T) {
	for locale, width := range map[string]float64{"en-US": 8.5, "fr-CA": 8.5, "es-MX": 8.5, "en-GB": 8.27, "zh-Hans-CN": 8.27, "ja-JP": 8.27, "en": 8.27, "": 8.27} {
		if w, h := Paper(RegionOf(locale)); w != width || (w == 8.5) != (h == 11) {
			t.Errorf("%q prints on %v×%v", locale, w, h)
		}
	}
	if w, _ := Paper("us"); w != 8.5 {
		t.Error("a region in lower case prints on A4")
	}
}
