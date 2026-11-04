package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A share link is followed to the video it leads to, its watch link kept
// without what the share added; any other link, or a share of what is no
// video, stays as it is, and so does one the site can't be asked about.
func TestVideoLink(t *testing.T) {
	was := shortLinkClient
	t.Cleanup(func() { shortLinkClient = was })
	var asked []string
	shortLinkClient = &http.Client{
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			asked = append(asked, r.URL.String())
			to := map[string]string{
				"/xuajGqj": "https://www.bilibili.com/video/BV1bY4G6pEEg?buvid=Y&p=1&share_medium=iphone&t=30&unique_k=xuajGqj",
				"/live":    "https://live.bilibili.com/123",
			}[r.URL.Path]
			if to == "" {
				return nil, io.ErrUnexpectedEOF
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {to}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}),
		CheckRedirect: was.CheckRedirect,
	}
	for link, want := range map[string]string{
		" https://b23.tv/xuajGqj ":                 "https://www.bilibili.com/video/BV1bY4G6pEEg?t=30",
		"https://b23.tv/live":                      "https://b23.tv/live",
		"https://b23.tv/gone":                      "https://b23.tv/gone",
		"https://b23.tv/BV1KofNYjEuK":              "https://b23.tv/BV1KofNYjEuK",
		"https://www.youtube.com/watch?v=dQw4w9Wg": "https://www.youtube.com/watch?v=dQw4w9Wg",
	} {
		if got := VideoLink(link); got != want {
			t.Errorf("VideoLink(%q) = %q, want %q", link, got, want)
		}
	}
	if len(asked) != 3 {
		t.Errorf("asked %v, only of the share links", asked)
	}
}

// A Bilibili video gets its part's cid, asked once a run; one Bilibili
// can't tell of now is asked again next time, and another site's video is
// as it is.
func TestVideoPart(t *testing.T) {
	was := partsFetch
	t.Cleanup(func() { partsFetch = was })
	var asked []string
	fails := true
	partsFetch = func(link string) ([]byte, error) {
		asked = append(asked, link)
		if fails {
			return nil, io.ErrUnexpectedEOF
		}
		return []byte(`{"code":0,"data":[{"cid":43310384091,"page":1,"part":"Zelda","first_frame":"http://i2.hdslb.com/f.jpg"}]}`), nil
	}
	v := doc.Video{Provider: "bilibili", ID: "BV1uVxD6oE1y", Start: 30}
	if got, _ := VideoPart(v); got != v {
		t.Errorf("a part not told of: %+v", got)
	}
	fails = false
	for range 2 {
		got, part := VideoPart(v)
		if got.CID != 43310384091 || got.Start != 30 || part.Picture != "https://i2.hdslb.com/f.jpg" {
			t.Errorf("got %+v, %+v", got, part)
		}
	}
	yt := doc.Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}
	if got, _ := VideoPart(yt); got != yt {
		t.Error("a YouTube video changed")
	}
	if len(asked) != 2 {
		t.Errorf("asked %v", asked)
	}
}
