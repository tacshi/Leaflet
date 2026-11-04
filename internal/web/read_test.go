package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Pages read here as the agent function reads them (supabase/functions/
// _shared/testdata/pages.json, which its tests write): an agent reads a
// page alike on the device and on the server.
func TestReadHTMLAsTheServerDoes(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/pages.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Page string `json:"page"`
		HTML string `json:"html"`
		Read struct {
			URL       string   `json:"url"`
			Title     string   `json:"title"`
			Text      string   `json:"text"`
			Links     []string `json:"links"`
			Truncated bool     `json:"truncated"`
		} `json:"read"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, c := range cases {
		p := ReadHTML(c.Page, c.HTML)
		w := c.Read
		if p.Title != w.Title || p.Text != w.Text || p.Truncated != w.Truncated || !slices.Equal(p.Links, w.Links) && len(p.Links)+len(w.Links) > 0 {
			t.Errorf("%s read\n%q %q %v %q\nthe server's\n%q %q %v %q", c.Page, p.Title, p.Text, p.Truncated, p.Links, w.Title, w.Text, w.Truncated, w.Links)
		}
	}
}

// A long page is cut at the end of a line near the most an agent reads.
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

// useServer has Read, LookUp and Fetch ask a test's server, which Fetch
// would refuse as on this machine.
func useServer(t *testing.T) {
	t.Helper()
	was := do
	do = http.DefaultClient.Do
	t.Cleanup(func() { do = was })
}

// A page is read where its redirects lead, in the encoding it says it is
// in; one that is not text is not read.
func TestReadFollowsRedirectsInThePagesEncoding(t *testing.T) {
	useServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusMovedPermanently)
		case "/b":
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			_, _ = w.Write(append([]byte("<title>t</title><p>"), 0xc4, 0xe3, 0xba, 0xc3)) // 你好 in GBK
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

// Read reaches only the public web, as Fetch does.
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

// What says it is not text is not read: Read answers before the body
// comes. What says nothing of itself is told by its first bytes.
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

// A site that takes the request and never answers holds Read up no
// longer than Fetch would wait, however long its caller would.
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

// hostilePages are pages made to be slow to read, each as long as a page
// read from the web may be: a line of many pieces, many links, many small
// elements, a long link, links in links, and elements nested as deep as a
// page may be.
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

// A page made to be slow reads in about the time it takes to parse it:
// each piece of it once, and none past the most of it an agent reads.
// Each is timed at its quickest of three, taken in turns, as other tests
// running meanwhile, under the race detector most of all, slow one or the
// other at times; a reading that went over each piece again for each
// would take a hundred times as long as parsing, or more.
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

// A reading stops when its caller's time is up, its parsing too, which
// takes a while for each tag among many open elements.
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
