package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
)

// Tests leave the user's own pictures on disk alone.
func init() { forgetAllPictures = func() {} }

// load waits for the picture at link, as views ask for it again and again.
func load(t *testing.T, ic *imageCache, link string) ui.ImageSource {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		pic, failed := ic.get(nil, link, 0)
		if pic != nil {
			return pic
		}
		if failed {
			t.Fatalf("%s failed", link)
		}
	}
	t.Fatalf("%s did not come", link)
	return nil
}

// failed waits for the picture at link to fail.
func failed(t *testing.T, ic *imageCache, link string) bool {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		pic, failed := ic.get(nil, link, 0)
		if pic != nil {
			return false
		}
		if failed {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return fileURL(path)
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func movingGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{Config: image.Config{Width: 4, Height: 2}}
	for i := range frames {
		f := image.NewPaletted(image.Rect(0, 0, 4, 2), color.Palette{color.Black, color.White})
		f.Set(0, 0, color.Gray{Y: uint8(255 * (i % 2))})
		g.Image, g.Delay = append(g.Image, f), append(g.Delay, 5)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImagesReadOnlyTheirOwnFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	own := filepath.Join(root, "files", "f1", "a b#1.png")
	secret := filepath.Join(outside, "id_rsa")
	link := filepath.Join(root, "files", "link.png")
	writeFile(t, own, pngOf(t, 2, 2))
	writeFile(t, secret, []byte("x"))
	if err := os.Symlink(secret, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Windows makes links only for administrators and in Developer Mode")
		}
		t.Fatal(err)
	}
	ic := newImageCache(root)
	tr := pictureTransport{ic: ic}
	get := func(u string) error {
		r, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return err
		}
		res, err := tr.RoundTrip(r)
		if err == nil {
			_ = res.Body.Close()
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: the library's file is kept again on disk", u)
			}
		}
		return err
	}
	if err := get(fileURL(own)); err != nil {
		t.Errorf("own file: %v", err)
	}
	for _, p := range []string{secret, link, filepath.Join(root, "..", filepath.Base(outside), "id_rsa")} {
		if err := get(fileURL(p)); !errors.Is(err, errNotOwnFile) {
			t.Errorf("read %s: %v", p, err)
		}
	}
	// A page on the web redirecting to the library's file is refused.
	r, _ := http.NewRequest("GET", fileURL(own), nil)
	r.Response = &http.Response{Request: &http.Request{}}
	if _, err := tr.RoundTrip(r); !errors.Is(err, errNotOwnFile) {
		t.Errorf("followed a redirect to a file: %v", err)
	}
	if load(t, ic, fileURL(own)) == nil {
		t.Error("the own picture did not show")
	}
}

func TestImageFailure(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root)
	if !failed(t, ic, fileURL(filepath.Join(root, "missing.png"))) {
		t.Error("a missing picture has not failed")
	}
	if pic, failed := ic.get(nil, "", 0); pic != nil || failed {
		t.Errorf("no link: %v, failed %v", pic, failed)
	}
}

func TestAnimatedGIFPlays(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root)
	moving := writeFile(t, filepath.Join(root, "moving.gif"), movingGIF(t, 3))
	still := writeFile(t, filepath.Join(root, "still.gif"), movingGIF(t, 1))
	load(t, ic, moving)
	load(t, ic, still)
	if ic.player(still, "b1", func() { t.Error("a player for a still GIF") }) != nil {
		t.Error("a still GIF has a player")
	}
	made := make(chan struct{})
	if ic.player(moving, "b1", func() { close(made) }) != nil {
		t.Fatal("a player made at once, on the main thread")
	}
	<-made
	p := ic.player(moving, "b1", nil)
	if p == nil || !p.Loop() {
		t.Fatalf("player %v", p)
	}
	if w, h := p.Size(); w != 4 || h != 2 {
		t.Errorf("player size %d×%d", w, h)
	}
	other := make(chan struct{})
	if ic.player(moving, "b2", func() { close(other) }) != nil {
		t.Fatal("another block shares the first one's player")
	}
	<-other
	if q := ic.player(moving, "b2", nil); q == nil || q == p {
		t.Errorf("second block's player %p, first's %p", q, p)
	}
	ic.release(time.Now().Add(playerIdle / 2))
	if ic.player(moving, "b1", nil) != p {
		t.Fatal("a player asked for recently was released")
	}
	ic.release(time.Now().Add(2 * playerIdle))
	if p.Playing() || len(ic.players) != 0 {
		t.Errorf("after its block went: playing %v, %d players kept", p.Playing(), len(ic.players))
	}
	again := make(chan struct{})
	if ic.player(moving, "b1", func() { close(again) }) != nil {
		t.Fatal("a released player came back at once")
	}
	<-again
	if q := ic.player(moving, "b1", nil); q == nil || q == p {
		t.Errorf("the block's new player %p, the released one %p", q, p)
	}
}

// A GIF not shown for a while lets go of its bytes, and netimage of its
// bitmap, so that it plays again when it shows again.
func TestGIFLetGoPlaysAgain(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root)
	moving := writeFile(t, filepath.Join(root, "moving.gif"), movingGIF(t, 3))
	load(t, ic, moving)
	ic.release(time.Now().Add(gifIdle / 2))
	if len(ic.gifs) != 1 {
		t.Fatal("a GIF shown lately was let go")
	}
	ic.release(time.Now().Add(2 * gifIdle))
	if len(ic.gifs) != 0 {
		t.Fatal("a GIF not shown for long was kept")
	}
	load(t, ic, moving)
	made := make(chan struct{})
	ic.player(moving, "b1", func() { close(made) })
	select {
	case <-made:
	case <-time.After(5 * time.Second):
		t.Fatal("the GIF shown again has no player")
	}
	if ic.player(moving, "b1", nil) == nil {
		t.Error("the GIF shown again does not play")
	}
}

func TestHugePictureRefused(t *testing.T) {
	pngHeader := func(w, h uint32) []byte {
		ihdr := make([]byte, 17)
		copy(ihdr, "IHDR")
		binary.BigEndian.PutUint32(ihdr[4:], w)
		binary.BigEndian.PutUint32(ihdr[8:], h)
		ihdr[12], ihdr[13] = 8, 6
		b := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d")
		b = append(b, ihdr...)
		return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(ihdr))
	}
	gifOf := func(w, h, frames int) []byte {
		b := []byte("GIF89a")
		b = append(b, byte(w), byte(w>>8), byte(h), byte(h>>8), 0, 0, 0)
		for range frames {
			b = append(b, 0x2C, 0, 0, 0, 0, byte(w), byte(w>>8), byte(h), byte(h>>8), 0, 2, 0)
		}
		return append(b, 0x3B)
	}
	for name, c := range map[string]struct {
		data []byte
		ok   bool
	}{
		"small png":                       {pngHeader(64, 64), true},
		"huge png":                        {pngHeader(50000, 50000), false},
		"gif of the most a header claims": {gifOf(65535, 65535, 1), false},
		"gif":                             {gifOf(5000, 5000, 2), true},
		"gif of 9 big frames":             {gifOf(5000, 5000, 9), false},
		"svg":                             {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="9" height="9"/>`), true},
	} {
		if err := checkPixels(c.data); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n, area := gifFrames(gifOf(4, 2, 3)); n != 3 || area != 24 {
		t.Errorf("gifFrames = %d, %d", n, area)
	}
	root := t.TempDir()
	ic := newImageCache(root)
	if !failed(t, ic, writeFile(t, filepath.Join(root, "bomb.png"), pngHeader(50000, 50000))) {
		t.Error("the huge picture did not fail")
	}
}

func TestClearedImagesClosePlayers(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root)
	url := writeFile(t, filepath.Join(root, "moving.gif"), movingGIF(t, 3))
	load(t, ic, url)
	made := make(chan struct{})
	ic.player(url, "b1", func() { close(made) })
	<-made
	p := ic.player(url, "b1", nil)
	late := make(chan struct{})
	ic.player(url, "b2", func() { close(late) })
	ic.clear()
	<-late
	if p.Playing() || len(ic.players) != 0 || len(ic.gifs) != 0 {
		t.Errorf("after clearing: playing %v, %d players and %d GIFs kept", p.Playing(), len(ic.players), len(ic.gifs))
	}
}

// A block's file:// link is read as a path, though it holds a # or a space.
func TestImageBlockOfAFileLink(t *testing.T) {
	a := templatesApp(t)
	a.images = newImageCache(a.st.Dir)
	path := filepath.Join(a.st.Dir, "files", "x", "plan #2 v1.png")
	writeFile(t, path, pngOf(t, 3, 2))
	n := &doc.Node{ID: "img", Type: "image", Data: map[string]any{"url": "file://" + path}}
	h := &host{a: a}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		pic, _, failed := h.Image(nil, n)
		if failed {
			t.Fatal("the picture failed")
		}
		if pic != nil {
			if w, hgt := ui.ImageSize(pic); w != 3 || hgt != 2 {
				t.Errorf("size %v×%v", w, hgt)
			}
			return
		}
	}
	t.Fatal("the picture did not come")
}
