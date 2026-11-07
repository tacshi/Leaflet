package mediaserver

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registered(t *testing.T, page string) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "call.mp3")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	t.Cleanup(s.Close)
	address, err := s.Register(Item{Path: path, Type: "audio/mpeg", Page: page, Policy: "default-src 'none'"})
	if err != nil {
		t.Fatal(err)
	}
	return s, address
}

func get(t *testing.T, method, address string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

// A player's page comes with its policy, and it can read any part of the
// file, which tells only what the app says it is.
func TestServesPageAndFile(t *testing.T) {
	_, address := registered(t, "<audio>")
	res, body := get(t, "GET", address, nil)
	if res.StatusCode != 200 || body != "<audio>" || res.Header.Get("Content-Security-Policy") != "default-src 'none'" {
		t.Errorf("page: %d %q %v", res.StatusCode, body, res.Header)
	}
	res, body = get(t, "GET", address+"media", map[string]string{"Range": "bytes=2-4"})
	if res.StatusCode != 206 || body != "234" || res.Header.Get("Content-Range") != "bytes 2-4/10" {
		t.Errorf("range: %d %q %v", res.StatusCode, body, res.Header)
	}
	for k, want := range map[string]string{"Content-Type": "audio/mpeg", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer"} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if res, _ := get(t, "HEAD", address+"media", nil); res.StatusCode != 200 || res.ContentLength != 10 {
		t.Errorf("HEAD: %d, %d bytes", res.StatusCode, res.ContentLength)
	}
}

// A file with no page is served at the address it gets.
// A cover goes with its token, of the type the app says; none, there is none.
func TestCover(t *testing.T) {
	s := &Server{}
	defer s.Close()
	path := filepath.Join(t.TempDir(), "a.mp3")
	if err := os.WriteFile(path, []byte("sound"), 0o600); err != nil {
		t.Fatal(err)
	}
	with, err := s.Register(Item{Path: path, Type: "audio/mpeg", Page: "<p>", Cover: []byte("picture"), CoverType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	res, body := get(t, http.MethodGet, with+"cover", nil)
	if res.StatusCode != http.StatusOK || body != "picture" || res.Header.Get("Content-Type") != "image/png" ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("cover %d %q %v", res.StatusCode, body, res.Header)
	}
	without, err := s.Register(Item{Path: path, Type: "audio/mpeg", Page: "<p>"})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := get(t, http.MethodGet, without+"cover", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no cover: %d", res.StatusCode)
	}
}

func TestFileAlone(t *testing.T) {
	_, address := registered(t, "")
	if !strings.HasSuffix(address, "/media") {
		t.Fatalf("address %q", address)
	}
	if res, body := get(t, "GET", address, nil); res.StatusCode != 200 || body != "0123456789" {
		t.Errorf("got %d %q", res.StatusCode, body)
	}
	if res, _ := get(t, "GET", strings.TrimSuffix(address, "media"), nil); res.StatusCode != 404 {
		t.Errorf("a page that isn't: %d", res.StatusCode)
	}
}

// Nothing goes by a wrong token, one revoked, another host's name, or a
// way of asking but reading.
func TestRefuses(t *testing.T) {
	s, address := registered(t, "<audio>")
	wrong := strings.TrimSuffix(address, "/") + "0/media"
	if res, _ := get(t, "GET", wrong, nil); res.StatusCode != 404 {
		t.Errorf("wrong token: %d", res.StatusCode)
	}
	if res, _ := get(t, "GET", address+"other", nil); res.StatusCode != 404 {
		t.Errorf("another path: %d", res.StatusCode)
	}
	if res, _ := get(t, "GET", address, map[string]string{"Host": "evil.example"}); res.StatusCode != 404 {
		t.Errorf("another host: %d", res.StatusCode)
	}
	if res, _ := get(t, "POST", address+"media", nil); res.StatusCode != 404 {
		t.Errorf("POST: %d", res.StatusCode)
	}
	s.Revoke(address)
	if res, _ := get(t, "GET", address+"media", nil); res.StatusCode != 404 {
		t.Errorf("revoked: %d", res.StatusCode)
	}
}

// A file gone from the device is not found, nor is a folder served.
func TestFileGone(t *testing.T) {
	s := &Server{}
	t.Cleanup(s.Close)
	for _, path := range []string{filepath.Join(t.TempDir(), "gone"), t.TempDir()} {
		address, err := s.Register(Item{Path: path, Type: "audio/mpeg"})
		if err != nil {
			t.Fatal(err)
		}
		if res, _ := get(t, "GET", address, nil); res.StatusCode != 404 {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
}
