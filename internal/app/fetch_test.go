package app

import (
	"bytes"
	"errors"
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
}
