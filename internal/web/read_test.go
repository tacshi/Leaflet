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
	"unicode/utf8"
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

// useServer has Read ask a test's server, which Fetch would refuse as on
// this machine.
func useServer(t *testing.T) {
	t.Helper()
	was := get
	get = func(ctx context.Context, link string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	t.Cleanup(func() { get = was })
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
