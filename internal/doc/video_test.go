package doc

import (
	"strings"
	"testing"
)

func TestParseVideoURL(t *testing.T) {
	for _, tc := range []struct {
		link string
		want Video
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://youtube.com/watch?v=dQw4w9WgXcQ&t=1m30s&list=x", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}},
		{"https://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://youtu.be/dQw4w9WgXcQ?t=42", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 42}},
		{" https://www.youtube.com/shorts/dQw4w9WgXcQ ", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://www.youtube.com/live/dQw4w9WgXcQ?si=abc", Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}},
		{"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?start=7", Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 7}},
		{"https://vimeo.com/76979871", Video{Provider: "vimeo", ID: "76979871"}},
		{"https://vimeo.com/76979871/8272103f6e#t=1h2s", Video{Provider: "vimeo", ID: "76979871", Hash: "8272103f6e", Start: 3602}},
		{"https://vimeo.com/channels/staffpicks/76979871", Video{Provider: "vimeo", ID: "76979871"}},
		{"https://player.vimeo.com/video/76979871?h=8272103f6e", Video{Provider: "vimeo", ID: "76979871", Hash: "8272103f6e"}},
	} {
		got, ok := ParseVideoURL(tc.link)
		if !ok || got != tc.want {
			t.Errorf("%s: got %+v, %v", tc.link, got, ok)
		}
	}
	for _, link := range []string{
		"", "dQw4w9WgXcQ", "https://www.youtube.com/", "https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/@channel", "https://vimeo.com/staffpicks", "ftp://youtu.be/dQw4w9WgXcQ",
		"https://example.com/watch?v=dQw4w9WgXcQ", "see https://youtu.be/dQw4w9WgXcQ",
	} {
		if v, ok := ParseVideoURL(link); ok {
			t.Errorf("%q is not a video: %+v", link, v)
		}
	}
}

func TestVideoURLs(t *testing.T) {
	yt := Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}
	if got := yt.PlayerURL(); got != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&playsinline=1&rel=0&start=90" {
		t.Errorf("player %s", got)
	}
	if got := yt.WatchURL(); got != "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=90s" {
		t.Errorf("watch %s", got)
	}
	vm := Video{Provider: "vimeo", ID: "76979871", Hash: "8272103f6e", Start: 5}
	if got := vm.PlayerURL(); got != "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=8272103f6e#t=5s" {
		t.Errorf("player %s", got)
	}
	if got := vm.OEmbedURL(); got != "https://vimeo.com/api/oembed.json?width=1280&url=https%3A%2F%2Fvimeo.com%2F76979871%2F8272103f6e" {
		t.Errorf("oembed %s", got)
	}
	if vm.Thumbnail() != "" || yt.Thumbnail() != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" {
		t.Errorf("thumbnails %q %q", vm.Thumbnail(), yt.Thumbnail())
	}
}

func TestVideoBlocks(t *testing.T) {
	n, err := SpecToNode(map[string]any{"type": "video", "video": map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Never"}}, "", "Agent")
	if err != nil || n.Type != "video" || n.Str("url") != "https://youtu.be/dQw4w9WgXcQ" || n.Str("title") != "Never" {
		t.Fatalf("got %+v, %v", n, err)
	}
	if _, err := SpecToNode(map[string]any{"type": "video", "video": map[string]any{"url": "https://example.com/a.mp4"}}, "", "Agent"); err == nil {
		t.Error("a link that is not YouTube or Vimeo made a video")
	}
	c := FromBlocks([]*Node{n})
	if views := BlockViews(c); views[0]["video"].(map[string]any)["url"] != "https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("view %v", views[0])
	}
	if md := PageMarkdown("P", c, nil); !strings.Contains(md, "[Never](https://youtu.be/dQw4w9WgXcQ)") {
		t.Errorf("markdown %q", md)
	}
	if c.PlainText() != "Never" {
		t.Errorf("plain %q", c.PlainText())
	}
}
