package webpage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/locales"
)

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
	if !strings.HasPrefix(PrintCSP, "default-src 'none'; script-src 'sha256-") || !strings.HasSuffix(PrintCSP, "; img-src data:") {
		t.Errorf("policy %q", PrintCSP)
	}
}

func TestPrintedPageDrawsItsMapsOnTheirTiles(t *testing.T) {
	nanjing := doc.Map{Title: "Nanjing", Places: []doc.MapPlace{{Name: `</script><b>x</b>`, Lat: 32.0423, Lng: 118.8199}, {Name: "新街口", Lat: 32.0461, Lng: 118.7922}}}
	c := doc.FromBlocks([]*doc.Node{{ID: "m1", Type: "map", Data: nanjing.Data()}, {ID: "m2", Type: "map", Data: map[string]any{"title": "Nowhere"}}})
	page := HTML(c, Options{Title: "Trip", Lang: "en", Serve: Inline, Print: true, Map: func(*doc.Node) string { return "/tok/m/m1" }})
	data := html.EscapeString(rendered.PrintMapData(locales.In("en"), nanjing))
	for _, want := range []string{
		`<meta http-equiv="Content-Security-Policy" content="` + rendered.PrintMapCSP() + `">`,
		`<figure id="b-m1" data-block="m1" class="map"><div class="mapshot" data-map="` + data + `"><svg class="mapsvg"`,
		`<figure id="b-m2" data-block="m2" class="map"><figcaption><strong>Nowhere</strong>`,
		rendered.PrintMapWorker() + "<script type=\"module\">" + rendered.PrintMapJS() + "</script>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %.200q", want)
		}
	}
	// It loads nothing but its tiles, and no frame; its scripts are its own.
	if strings.Contains(page, "<iframe") || strings.Count(page, "<script") != 2 || strings.Contains(page, "</script><b>") {
		t.Errorf("the page frames a map, or runs what it shouldn't: %.300s", page)
	}
	csp := rendered.PrintMapCSP()
	for _, want := range []string{"default-src 'none'; script-src 'sha256-", "connect-src blob: data: " + rendered.TileHost + ";", "worker-src blob: data:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "https:") && strings.Count(csp, "https:") != strings.Count(csp, rendered.TileHost) {
		t.Errorf("policy reaches more than the tile server: %q", csp)
	}

	// A page without a map to draw loads nothing.
	for _, c := range []*doc.Content{doc.FromBlocks([]*doc.Node{{ID: "m2", Type: "map", Data: map[string]any{"title": "Nowhere"}}}), doc.Empty()} {
		page := HTML(c, Options{Title: "Trip", Serve: Inline, Print: true})
		if !strings.Contains(page, `content="`+PrintCSP+`"`) || strings.Contains(page, "map-worker") || strings.Contains(page, "<script") {
			t.Errorf("a page without a map to draw: %.300s", page)
		}
	}
	// A web page frames its maps instead.
	if page := HTML(c, Options{Title: "Trip", Serve: Inline, Map: func(*doc.Node) string { return "/tok/m/m1" }}); !strings.Contains(page, `<iframe src="/tok/m/m1"`) ||
		strings.Contains(page, `class="mapshot"`) || strings.Contains(page, "map-worker") {
		t.Error("a web page's maps")
	}
	if !strings.HasPrefix(PrintOrigin, "https://") || !strings.HasSuffix(PrintOrigin, "/") || !strings.Contains(rendered.PrintMapJS(), "window.leafletMapsDrawn = ") ||
		MapsDrawn != "await window.leafletMapsDrawn" {
		t.Error("the app's wait for the maps")
	}
}

func TestPrintedPageHasItsWebPictures(t *testing.T) {
	var png1, png2 bytes.Buffer
	if err := png.Encode(&png1, photo(40, 30, 128)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&png2, photo(20, 20, 255)); err != nil {
		t.Fatal(err)
	}
	jpg := encodeJPEG(t, photo(60, 40, 255), 0)
	svg := []byte(`<?xml version="1.0"?>` + "\n" + `<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"/>`)
	web := map[string][]byte{
		"https://pics.example/a.png":                       png1.Bytes(),
		"https://pics.example/b.jpg":                       jpg,
		"http://old.example/c.png":                         png2.Bytes(),
		"https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg": jpg,
		"https://pics.example/d.svg":                       svg,
		"https://pics.example/page":                        []byte("<!doctype html><html><body>not a picture</body></html>"),
	}
	var mu sync.Mutex
	asked := map[string]int{}
	saved := fetchPicture
	t.Cleanup(func() { fetchPicture = saved })
	fetchPicture = func(_ context.Context, link string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		asked[link]++
		if data, ok := web[link]; ok {
			return data, nil
		}
		return nil, errors.New("not found")
	}
	c := doc.FromBlocks([]*doc.Node{
		{ID: "i1", Type: "image", Data: map[string]any{"url": "https://pics.example/a.png"}},
		{ID: "i2", Type: "image", Data: map[string]any{"url": "https://pics.example/a.png"}},
		{ID: "i3", Type: "image", Data: map[string]any{"url": "http://old.example/c.png"}},
		{ID: "i4", Type: "image", Data: map[string]any{"url": "https://pics.example/page"}},
		{ID: "i5", Type: "image", Data: map[string]any{"url": "https://pics.example/gone.png"}},
		{ID: "l1", Type: "link_preview", Data: map[string]any{"url": "https://site.example/post", "title": "Post", "image": "https://pics.example/b.jpg"}},
		{ID: "v1", Type: "video", Data: map[string]any{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "title": "Song"}},
		{ID: "c1", Type: "card", Data: map[string]any{"layout": "details", "title": "Hotel", "image": "https://pics.example/d.svg",
			"facts": []any{map[string]any{"label": "Phone", "value": "123"}}}},
	})
	page := HTML(c, Options{Title: "Trip", Serve: Inline, Print: true})
	inlined := func(typ string, data []byte) string {
		return `src="data:` + typ + `;base64,` + base64.StdEncoding.EncodeToString(data) + `"`
	}
	for _, want := range []string{
		`<figure id="b-i1" data-block="i1" class="image"><img ` + inlined("image/png", png1.Bytes()),
		`<figure id="b-i2" data-block="i2" class="image"><img ` + inlined("image/png", png1.Bytes()),
		`<figure id="b-i3" data-block="i3" class="image"><img ` + inlined("image/png", png2.Bytes()),
		`<img ` + inlined("image/jpeg", jpg) + ` alt=""></a>`,
		`<img class="fc-pic" ` + inlined("image/svg+xml", svg),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	if n := strings.Count(page, inlined("image/jpeg", jpg)); n != 2 {
		t.Errorf("the link card and the video card show %d pictures", n)
	}
	for _, bad := range []string{`src="http`, `id="b-i4"`, `id="b-i5"`} {
		if strings.Contains(page, bad) {
			t.Errorf("has %q", bad)
		}
	}
	for link, n := range asked {
		if n != 1 {
			t.Errorf("%s fetched %d times", link, n)
		}
	}
	if len(asked) != 7 {
		t.Errorf("fetched %v", asked)
	}
}

func TestPrintedPageLeavesOutPrivatePictures(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, photo(8, 8, 255)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()
	c := doc.FromBlocks([]*doc.Node{
		{ID: "i1", Type: "image", Data: map[string]any{"url": srv.URL + "/router.png"}},
		{ID: "l1", Type: "link_preview", Data: map[string]any{"url": "https://site.example/post", "title": "Post", "image": srv.URL + "/card.png"}},
	})
	page := HTML(c, Options{Title: "Home", Serve: Inline, Print: true})
	if strings.Contains(page, "<img") || strings.Contains(page, srv.URL) {
		t.Errorf("a picture at a private address printed:\n%s", page)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("the private address was asked %d times", hits)
	}
}

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

func photo(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			c := color.NRGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(x * 255 / w), alpha}
			if x < w/10 && y < h/10 {
				c = color.NRGBA{255, 0, 0, alpha}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t testing.TB, img image.Image, turn uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if turn == 0 {
		return buf.Bytes()
	}
	exif := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01")
	exif = append(exif, byte(turn>>8), byte(turn), 0, 0, 0, 0, 0, 0)
	app1 := append([]byte{0xff, 0xe1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	data := buf.Bytes()
	return append(append([]byte{0xff, 0xd8}, app1...), data[2:]...)
}

func decoded(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestThinPicturesPrint(t *testing.T) {
	line := image.NewNRGBA(image.Rect(0, 0, 3000, 1))
	for x := range 3000 {
		line.Set(x, 0, color.NRGBA{uint8(x * x * 31 >> 3), uint8(x * 7 * x >> 5), uint8(x * 113), 255})
	}
	var data bytes.Buffer
	if err := png.Encode(&data, line); err != nil {
		t.Fatal(err)
	}
	small, _, ok := forPrint(data.Bytes())
	if !ok {
		t.Fatal("kept as it was")
	}
	got, _, err := image.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != printPixels || b.Dy() != 1 {
		t.Errorf("printed %v", b)
	}
	for x := range printPixels {
		if _, _, _, a := got.At(x, 0).RGBA(); a == 0 {
			t.Fatalf("clear at %d", x)
		}
	}
}

func TestPicturesPrintSmall(t *testing.T) {
	big := encodeJPEG(t, photo(3000, 2000, 255), 0)
	small, typ, ok := forPrint(big)
	if img := decoded(t, small); !ok || typ != "image/jpeg" || img.Bounds().Dx() != printPixels || img.Bounds().Dy() != 933 || len(small) >= len(big) {
		t.Errorf("a photo printed as %s %v, %d bytes of %d", typ, img.Bounds(), len(small), len(big))
	}
	var see bytes.Buffer
	if err := png.Encode(&see, photo(2000, 600, 128)); err != nil {
		t.Fatal(err)
	}
	small, typ, ok = forPrint(see.Bytes())
	if img := decoded(t, small); !ok || typ != "image/png" || img.Bounds().Dx() != printPixels {
		t.Errorf("a picture to see through printed as %s %v", typ, img.Bounds())
	} else if _, _, _, a := img.At(700, 300).RGBA(); a == 0xffff {
		t.Error("a picture to see through printed opaque")
	}
	small, _, ok = forPrint(encodeJPEG(t, photo(3000, 2000, 255), 6))
	img := decoded(t, small)
	if !ok || img.Bounds().Dx() != printPixels || img.Bounds().Dy() != 2100 {
		t.Fatalf("a turned photo printed %v", img.Bounds())
	}
	if r, g, _, _ := img.At(printPixels-20, 20).RGBA(); r < 0xc000 || g > 0x4000 {
		t.Errorf("a turned photo's corner is %v", img.At(printPixels-20, 20))
	}
	for _, kept := range [][]byte{encodeJPEG(t, photo(3000, 1000, 255), 6), encodeJPEG(t, photo(800, 600, 255), 0), []byte("\x89PNG\r\n\x1a\nnot a picture")} {
		if _, _, ok := forPrint(kept); ok {
			t.Errorf("a picture of %d bytes was made anew", len(kept))
		}
	}
	path := filepath.Join(t.TempDir(), "big.jpg")
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if src := Inline.Image(path, "image/jpeg"); !strings.HasPrefix(src, "data:image/jpeg;base64,") || len(src) > base64.StdEncoding.EncodedLen(len(big))/2 {
		t.Errorf("a photo is inlined in %d bytes, of %d", len(src), len(big))
	}
}

func BenchmarkPrintAPhoto(b *testing.B) {
	data := encodeJPEG(b, photo(4032, 3024, 255), 6)
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, _, ok := forPrint(data); !ok {
			b.Fatal("not made smaller")
		}
	}
}

func TestDecodedBytesOfAHugeHeaderDoNotOverflow(t *testing.T) {
	for _, m := range []color.Model{color.NRGBAModel, color.NRGBA64Model, color.GrayModel} {
		cfg := image.Config{ColorModel: m, Width: 1<<31 - 1, Height: 1<<31 - 1}
		if n := decodedBytes(cfg); n <= maxDecodedBytes {
			t.Errorf("%T of %dx%d reads as %d bytes", m, cfg.Width, cfg.Height, n)
		}
	}
	if n := decodedBytes(image.Config{ColorModel: color.NRGBAModel, Width: 3, Height: 2}); n != 24 {
		t.Errorf("3x2 NRGBA reads as %d bytes", n)
	}
}
