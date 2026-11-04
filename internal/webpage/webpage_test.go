package webpage

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

// photo is a w×h picture of noise, as a photo is to compress, its top
// left corner red, and alpha, if not 255, its pixels' opacity.
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
	// An EXIF orientation in an APP1 segment after the start of the image.
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

// A printed page's pictures are as wide as paper prints them sharp, and
// no wider: a photo as a JPEG, one that is not opaque as a PNG, each
// turned as its EXIF says to see it; a picture already that small, or
// one that can't be read, is as it was.
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
	// Turned a quarter to see, a picture 3000 by 2000 is 2000 wide, and
	// its top left corner is at the top right.
	small, _, ok = forPrint(encodeJPEG(t, photo(3000, 2000, 255), 6))
	img := decoded(t, small)
	if !ok || img.Bounds().Dx() != printPixels || img.Bounds().Dy() != 2100 {
		t.Fatalf("a turned photo printed %v", img.Bounds())
	}
	if r, g, _, _ := img.At(printPixels-20, 20).RGBA(); r < 0xc000 || g > 0x4000 {
		t.Errorf("a turned photo's corner is %v", img.At(printPixels-20, 20))
	}
	// One turned to be 1000 wide is narrow enough, and keeps its EXIF.
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
