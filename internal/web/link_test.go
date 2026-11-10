package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"leaflet/internal/doc"
)

func TestReadLinkInfo(t *testing.T) {
	src := `<html><head><title> Fallback  title </title>
<meta content="The Go &amp; you" property="og:title">
<meta name="description" content="Plain description">
<meta property='og:description' content='Build simple, secure, scalable systems'>
<meta property="og:image" content="/images/go-logo.png">
<meta property="og:site_name" content="Go">
</head><body><meta property="og:title" content="not this"></body></html>`
	got := ReadLinkInfo("https://go.dev/doc/", src)
	want := LinkInfo{"The Go & you", "Build simple, secure, scalable systems", "https://go.dev/images/go-logo.png", "Go"}
	if got != want {
		t.Errorf("got %+v", got)
	}
	if got := ReadLinkInfo("https://x.dev", "<title>Only a title</title>"); got.Title != "Only a title" || got.Image != "" {
		t.Errorf("got %+v", got)
	}
	if got := ReadLinkInfo("https://x.dev", `<meta property="og:image" content="javascript:alert(1)">`); got.Image != "" {
		t.Errorf("image %q", got.Image)
	}
}

func TestReadLinkInfoLongerWhenLowered(t *testing.T) {
	src := strings.Repeat("Ⱥ", 50) + "<title>Kept</title></HEAD><title>Not this</title>"
	if got := ReadLinkInfo("https://x.dev", src); got.Title != "Kept" {
		t.Errorf("got %+v", got)
	}
}

func TestLookUpReadsThePagesEncoding(t *testing.T) {
	useServer(t)
	title, err := simplifiedchinese.GBK.NewEncoder().String("新闻首页")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		meta := `<meta charset="gbk">`
		if r.URL.Path == "/header" {
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			meta = ""
		}
		_, _ = w.Write([]byte("<html><head>" + meta + "<title>" + title + "</title></head></html>"))
	}))
	defer srv.Close()
	for _, path := range []string{"/header", "/meta"} {
		info, err := LookUp(srv.URL + path)
		if err != nil || info.Title != "新闻首页" {
			t.Errorf("%s: %q, %v", path, info.Title, err)
		}
	}
}

func TestFillLinkCards(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{
		{ID: "a", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/"}},
		{ID: "b", Type: "link_preview", Data: map[string]any{"url": "https://go.dev/"}},
		{ID: "c", Type: "link_preview", Data: map[string]any{"url": "https://x.dev/", "title": "X"}},
	})
	if got := Unfilled(c); !slices.Equal(got, []string{"https://go.dev/"}) {
		t.Errorf("unfilled %v", got)
	}
	if !(LinkInfo{Title: "Go", Site: "go.dev"}).Fill(c, "https://go.dev/") || c.Find("b").Str("title") != "Go" {
		t.Errorf("filled %+v", c.Find("b").Data)
	}
	if len(Unfilled(c)) != 0 || (LinkInfo{Title: "Go"}).Fill(c, "https://go.dev/") {
		t.Error("filled cards were filled again")
	}
}

func TestLookUpReadsOnlyAPagesHead(t *testing.T) {
	useServer(t)
	release := make(chan struct{})
	sent := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			<-release
		case "/endless":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			n, _ := w.Write([]byte(`<head><meta property="og:title" content="Endless"></head>`))
			chunk := []byte(strings.Repeat("<p>more</p>", 1000))
			for n < 4*maxFetchBytes {
				m, err := w.Write(chunk)
				if n += m; err != nil {
					break
				}
			}
			sent <- n
		}
	}))
	defer srv.Close()
	defer close(release)
	done := make(chan error, 1)
	go func() {
		info, err := LookUp(srv.URL + "/video.mp4")
		if err == nil && info != (LinkInfo{}) {
			t.Errorf("a video's info %+v", info)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("looked up a video: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LookUp waited for a video's body")
	}
	info, err := LookUp(srv.URL + "/endless")
	if err != nil || info.Title != "Endless" {
		t.Errorf("looked up %+v, %v", info, err)
	}
	if n := <-sent; n >= maxFetchBytes {
		t.Errorf("the page sent %d bytes before LookUp let it go", n)
	}
}
