package app

import (
	"errors"
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
