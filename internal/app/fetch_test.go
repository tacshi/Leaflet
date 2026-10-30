package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Links from shared pages reach only the web: not files on this device,
// nor services on it or its network.
func TestFetchOnlyThePublicWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<title>secret</title>"))
	}))
	defer srv.Close()
	if _, err := fetch(srv.URL); !errors.Is(err, errNotPublic) {
		t.Errorf("fetched a service on this machine: %v", err)
	}
	if _, err := fetch("file:///etc/hosts"); err == nil {
		t.Error("fetched a file")
	}
	for addr, public := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "172.16.0.1": false, "169.254.169.254": false,
		"100.100.1.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false,
		// IPv6 addresses standing for IPv4 ones are as those are.
		"64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false, "64:ff9b::5db8:d822": true, "64:ff9b:1::a00:1": false,
		"2002:7f00:1::1": false, "2002:c0a8:101::1": false, "2002:5db8:d822::1": true,
		"::127.0.0.1": false, "::10.0.0.1": false, "fec0::1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != public {
			t.Errorf("publicAddr(%s) = %v", addr, got)
		}
	}
}

// Pictures load from files in the data folder only, links followed.
func TestImagesReadOnlyTheirOwnFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	own := filepath.Join(root, "files", "f1", "a.png")
	secret := filepath.Join(outside, "id_rsa")
	link := filepath.Join(root, "files", "link.png")
	_ = os.MkdirAll(filepath.Dir(own), 0o750)
	for _, p := range []string{own, secret} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	ic := newImageCache(root, filepath.Join(root, "images"))
	if _, err := ic.read("file://" + own); err != nil {
		t.Errorf("own file: %v", err)
	}
	for _, p := range []string{secret, link, filepath.Join(root, "..", filepath.Base(outside), "id_rsa")} {
		if _, err := ic.read("file://" + p); !errors.Is(err, errNotOwnFile) {
			t.Errorf("read %s: %v", p, err)
		}
	}
}

// A picture that cannot be read says it failed, rather than loading
// forever, and is tried again later.
func TestImageCacheFailure(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root, filepath.Join(root, "images"))
	url := "file://" + filepath.Join(root, "missing.png")
	done := make(chan struct{})
	if ic.get(url, func() { close(done) }) != nil || ic.hasFailed(url) {
		t.Fatal("a picture loading is shown or failed")
	}
	<-done
	if !ic.hasFailed(url) {
		t.Fatal("a missing picture has not failed")
	}
	if ic.get(url, func() { t.Error("tried again at once") }) != nil {
		t.Fatal("a missing picture is shown")
	}
	// After a while it is tried again.
	ic.mu.Lock()
	ic.failed[url] = time.Now().Add(-imageRetry)
	ic.mu.Unlock()
	again := make(chan struct{})
	ic.get(url, func() { close(again) })
	if ic.hasFailed(url) {
		t.Error("a picture tried again still says it failed")
	}
	<-again
}

// An animated GIF gets a player, looping, for each block that shows it,
// made off the main thread; a GIF of one frame stays a picture.
func TestAnimatedGIFPlays(t *testing.T) {
	root := t.TempDir()
	ic := newImageCache(root, filepath.Join(root, "images"))
	frame := func(c color.Gray) *image.Paletted {
		f := image.NewPaletted(image.Rect(0, 0, 4, 2), color.Palette{color.Black, color.White})
		f.Set(0, 0, c)
		return f
	}
	write := func(name string, frames int) string {
		g := &gif.GIF{Config: image.Config{Width: 4, Height: 2}}
		for i := range frames {
			g.Image = append(g.Image, frame(color.Gray{Y: uint8(255 * (i % 2))}))
			g.Delay = append(g.Delay, 5)
		}
		var b bytes.Buffer
		if err := gif.EncodeAll(&b, g); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return "file://" + path
	}
	load := func(url string) {
		done := make(chan struct{})
		ic.get(url, func() { close(done) })
		<-done
		if ic.get(url, nil) == nil {
			t.Fatalf("%s did not load", url)
		}
	}
	moving, still := write("moving.gif", 3), write("still.gif", 1)
	load(moving)
	load(still)
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
	// A player its block keeps asking for stays; one no block asked for
	// within playerIdle is closed and forgotten, and made again if its
	// block shows once more.
	ic.releasePlayers(time.Now().Add(playerIdle / 2))
	if ic.player(moving, "b1", nil) != p {
		t.Fatal("a player asked for recently was released")
	}
	ic.releasePlayers(time.Now().Add(2 * playerIdle))
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

// A picture whose header says it is far larger than it is, as a small
// file can, is refused before it is decoded, as decoding it would take
// more memory than the device has: a bitmap by its size, an animated GIF
// by all its frames'.
func TestHugePictureRefused(t *testing.T) {
	png := func(w, h uint32) []byte {
		ihdr := make([]byte, 17)
		copy(ihdr, "IHDR")
		binary.BigEndian.PutUint32(ihdr[4:], w)
		binary.BigEndian.PutUint32(ihdr[8:], h)
		ihdr[12], ihdr[13] = 8, 6 // 8 bits a channel, RGBA
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
		"small png":           {png(64, 64), true},
		"huge png":            {png(50000, 50000), false},
		"gif":                 {gifOf(5000, 5000, 2), true},
		"gif of 9 big frames": {gifOf(5000, 5000, 9), false},
		"svg":                 {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="9" height="9"/>`), true},
	} {
		if err := checkPixels(c.data); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n, area := gifFrames(gifOf(4, 2, 3)); n != 3 || area != 24 {
		t.Errorf("gifFrames = %d, %d", n, area)
	}
	root := t.TempDir()
	path := filepath.Join(root, "bomb.png")
	if err := os.WriteFile(path, png(50000, 50000), 0o600); err != nil {
		t.Fatal(err)
	}
	ic := newImageCache(root, filepath.Join(root, "images"))
	done := make(chan struct{})
	ic.get("file://"+path, func() { close(done) })
	<-done
	if !ic.hasFailed("file://" + path) {
		t.Error("the huge picture did not fail")
	}
}

// Clearing the cache closes its GIFs' players, and those it was still
// making then as they are made.
func TestClosedImageCacheClosesPlayers(t *testing.T) {
	root := t.TempDir()
	g := &gif.GIF{Config: image.Config{Width: 4, Height: 2}}
	for i := range 3 {
		f := image.NewPaletted(image.Rect(0, 0, 4, 2), color.Palette{color.Black, color.White})
		f.Set(0, 0, color.Gray{Y: uint8(255 * (i % 2))})
		g.Image, g.Delay = append(g.Image, f), append(g.Delay, 5)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "moving.gif")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	url := "file://" + path
	ic := newImageCache(root, filepath.Join(root, "images"))
	loaded := make(chan struct{})
	ic.get(url, func() { close(loaded) })
	<-loaded
	made := make(chan struct{})
	ic.player(url, "b1", func() { close(made) })
	<-made
	p := ic.player(url, "b1", nil)
	late := make(chan struct{})
	ic.player(url, "b2", func() { close(late) })
	ic.close()
	<-late
	if p.Playing() || len(ic.players) != 0 {
		t.Errorf("after closing: playing %v, %d players kept", p.Playing(), len(ic.players))
	}
}
