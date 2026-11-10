package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func TestReadCutsALongPageAtALine(t *testing.T) {
	line := strings.Repeat("字", 99)
	text := strings.TrimSuffix(strings.Repeat(line+"\n", 400), "\n")
	cut, truncated := truncate(text)
	if !truncated || utf8.RuneCountInString(cut) > MaxPageText || !strings.HasSuffix(cut, line) {
		t.Errorf("cut to %d characters, truncated %v", utf8.RuneCountInString(cut), truncated)
	}
	if s, cut := truncate("short"); s != "short" || cut {
		t.Errorf("short cut: %q %v", s, cut)
	}
}

func useServer(t *testing.T) {
	t.Helper()
	was := do
	do = http.DefaultClient.Do
	t.Cleanup(func() { do = was })
}

func TestReadFollowsRedirectsInThePagesEncoding(t *testing.T) {
	useServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusMovedPermanently)
		case "/b":
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			_, _ = w.Write(append([]byte("<title>t</title><p>"), 0xc4, 0xe3, 0xba, 0xc3))
		case "/x.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{1, 2, 3})
		case "/notes.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("plain words https://example.com/next."))
		}
	}))
	defer srv.Close()
	p, err := Read(context.Background(), srv.URL+"/a")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != srv.URL+"/b" || p.Title != "t" || p.Text != "你好" {
		t.Errorf("read %+v", p)
	}
	if _, err := Read(context.Background(), srv.URL+"/x.png"); !errors.Is(err, ErrNotText) {
		t.Errorf("read a picture: %v", err)
	}
	p, err = Read(context.Background(), srv.URL+"/notes.txt")
	if err != nil || p.Text != "plain words https://example.com/next." || !slices.Equal(p.Links, []string{"https://example.com/next"}) {
		t.Errorf("read text %+v, %v", p, err)
	}
}

func TestReadOnlyThePublicWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<title>secret</title>"))
	}))
	defer srv.Close()
	if _, err := Read(context.Background(), srv.URL); !errors.Is(err, ErrNotPublic) {
		t.Errorf("read a service on this machine: %v", err)
	}
	if _, err := Read(context.Background(), "file:///etc/hosts"); err == nil {
		t.Error("read a file")
	}
}

func TestReadTellsWhatIsNotTextFirst(t *testing.T) {
	useServer(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/video":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			<-release
		case "/unsaid.png":
			w.Header()["Content-Type"] = nil
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
		case "/unsaid":
			w.Header()["Content-Type"] = nil
			_, _ = w.Write([]byte("<!doctype html><title>t</title><p>Said"))
		}
	}))
	defer srv.Close()
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := Read(context.Background(), srv.URL+"/video")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotText) {
			t.Errorf("read a video: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read waited for a video's body")
	}
	if _, err := Read(context.Background(), srv.URL+"/unsaid.png"); !errors.Is(err, ErrNotText) {
		t.Errorf("read a picture that says nothing of itself: %v", err)
	}
	if p, err := Read(context.Background(), srv.URL+"/unsaid"); err != nil || p.Title != "t" || p.Text != "Said" {
		t.Errorf("read a page that says nothing of itself: %+v, %v", p, err)
	}
}

func TestReadWaitsNoLongerThanFetch(t *testing.T) {
	useServer(t)
	was := fetchWait
	fetchWait = 100 * time.Millisecond
	t.Cleanup(func() { fetchWait = was })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	if _, err := Read(context.Background(), srv.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("read a site that never answers: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Read waited %v", took)
	}
}

func hostilePages() map[string]string {
	fill := func(head, piece, tail string) string {
		return head + strings.Repeat(piece, (maxPageBytes-len(head)-len(tail))/len(piece)) + tail
	}
	nested := strings.Repeat(`<a href="/x"><marquee>`, 250)
	return map[string]string{
		"pieces":         fill("", "x<!---->", ""),
		"spaced pieces":  fill("", "x\u3000<!---->", ""),
		"links":          fill("", "<a href=/x>y</a>", ""),
		"elements":       fill("", "<i>x</i>\n", ""),
		"pre":            fill("<pre>", "x <!---->", ""),
		"pre lines":      fill("<pre>", "\n", "x"),
		"cells":          fill("<table><tr>", "<td>x</td>", ""),
		"long link":      fill("<a href=/x>", "x<!---->", "</a>"),
		"links in links": fill(nested, "y<!---->", ""),
		"deep":           fill(strings.Repeat("<div>", 500), "x<!---->", ""),
		"too deep":       fill("", "<div>", "x"),
	}
}

func TestReadHTMLOfHostilePagesIsQuick(t *testing.T) {
	for name, src := range hostilePages() {
		var parse, took time.Duration
		var p Page
		for i := range 3 {
			start := time.Now()
			_, _ = html.Parse(strings.NewReader(src))
			if d := time.Since(start); i == 0 || d < parse {
				parse = d
			}
			start = time.Now()
			p = ReadHTML("https://example.com/", src)
			if d := time.Since(start); i == 0 || d < took {
				took = d
			}
		}
		if took > 8*parse+time.Second {
			t.Errorf("%s: read in %v, parsed in %v", name, took, parse)
		}
		if utf8.RuneCountInString(p.Text) > MaxPageText {
			t.Errorf("%s: read %d characters", name, utf8.RuneCountInString(p.Text))
		}
	}
}

func TestReadHTMLStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := strings.Repeat("<p>x</p>", 10_000)
	if _, err := readHTML(ctx, "https://example.com/", src); !errors.Is(err, context.Canceled) {
		t.Errorf("read on after its context ended: %v", err)
	}
	deep := strings.Repeat("<div>", 500)
	slow := deep + strings.Repeat("</p>", (maxPageBytes-len(deep))/4)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := readHTML(ctx, "https://example.com/", slow); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("parsed on after its context ended: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("parsed for %v", took)
	}
}

func BenchmarkReadHTMLOfHostilePages(b *testing.B) {
	for name, src := range hostilePages() {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				ReadHTML("https://example.com/", src)
			}
		})
	}
}
