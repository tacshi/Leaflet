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
		{"https://blog-r2.jw1.dev/3b8wzvg9E4_tDvUx.mp4", Video{Provider: "file", ID: "https://blog-r2.jw1.dev/3b8wzvg9E4_tDvUx.mp4"}},
		{"https://example.com/clips/A.MOV?sig=1#t=1m", Video{Provider: "file", ID: "https://example.com/clips/A.MOV?sig=1", Start: 60}},
		{"http://example.com/a.webm", Video{Provider: "file", ID: "http://example.com/a.webm"}},
		{"https://www.bilibili.com/video/BV1KofNYjEuK/?spm_id_from=333.337&vd_source=ab", Video{Provider: "bilibili", ID: "BV1KofNYjEuK"}},
		{"https://www.bilibili.com/video/BV1KofNYjEuK?p=2&t=30.4", Video{Provider: "bilibili", ID: "BV1KofNYjEuK", Part: 2, Start: 30}},
		{"https://www.bilibili.com/zh-Hans/video/BV1e5nv6GEZJ/", Video{Provider: "bilibili", ID: "BV1e5nv6GEZJ"}},
		{"https://m.bilibili.com/video/av573719854?p=1", Video{Provider: "bilibili", ID: "av573719854"}},
		{"https://www.bilibili.com/s/video/BV1KofNYjEuK", Video{Provider: "bilibili", ID: "BV1KofNYjEuK"}},
		{"https://b23.tv/BV1KofNYjEuK", Video{Provider: "bilibili", ID: "BV1KofNYjEuK"}},
		{"https://player.bilibili.com/player.html?isOutside=true&aid=573719854&cid=1&p=3", Video{Provider: "bilibili", ID: "av573719854", Part: 3}},
		{"https://player.bilibili.com/player.html?bvid=BV1KofNYjEuK&t=1m", Video{Provider: "bilibili", ID: "BV1KofNYjEuK", Start: 60}},
		{"https://www.dailymotion.com/video/x8j5v1k?start=12", Video{Provider: "dailymotion", ID: "x8j5v1k", Start: 12}},
		{"https://www.dailymotion.com/video/x7tgad0_a-title", Video{Provider: "dailymotion", ID: "x7tgad0"}},
		{"https://dai.ly/x8j5v1k", Video{Provider: "dailymotion", ID: "x8j5v1k"}},
		{"https://www.dailymotion.com/embed/video/x8j5v1k", Video{Provider: "dailymotion", ID: "x8j5v1k"}},
		{"https://geo.dailymotion.com/player.html?video=x8j5v1k&startTime=5", Video{Provider: "dailymotion", ID: "x8j5v1k", Start: 5}},
		{"https://www.nicovideo.jp/watch/sm9?from=30", Video{Provider: "niconico", ID: "sm9", Start: 30}},
		{"https://sp.nicovideo.jp/watch/so38016254", Video{Provider: "niconico", ID: "so38016254"}},
		{"https://nico.ms/sm9", Video{Provider: "niconico", ID: "sm9"}},
		{"https://embed.nicovideo.jp/watch/sm9", Video{Provider: "niconico", ID: "sm9"}},
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
		"https://example.com/a.mp3", "https://example.com/mp4", "file:///Users/me/a.mp4", "https:///a.mp4",
		"https://www.bilibili.com/", "https://www.bilibili.com/video/", "https://www.bilibili.com/video/BV1KofNYjEu",
		"https://www.bilibili.com/video/BV0KofNYjEuK", "https://www.bilibili.com/video/av0", "https://b23.tv/AbCdEfG",
		"https://space.bilibili.com/video/BV1KofNYjEuK", "https://player.bilibili.com/player.html",
		"//player.bilibili.com/player.html?bvid=BV1KofNYjEuK",
		"https://www.dailymotion.com/DW_Espanol", "https://www.dailymotion.com/video/", "https://dai.ly/",
		"https://www.nicovideo.jp/watch/lv123", "https://www.nicovideo.jp/sm9", "https://nico.ms/sm0",
	} {
		if v, ok := ParseVideoURL(link); ok {
			t.Errorf("%q is not a video: %+v", link, v)
		}
	}
}

func TestVideoURLs(t *testing.T) {
	yt := Video{Provider: "youtube", ID: "dQw4w9WgXcQ", Start: 90}
	if got := yt.PlayerURL(true); got != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&playsinline=1&rel=0&start=90" {
		t.Errorf("player %s", got)
	}
	if got := yt.PlayerURL(false); got != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?playsinline=1&rel=0&start=90" {
		t.Errorf("still player %s", got)
	}
	if got := yt.WatchURL(); got != "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=90s" {
		t.Errorf("watch %s", got)
	}
	vm := Video{Provider: "vimeo", ID: "76979871", Hash: "8272103f6e", Start: 5}
	if got := vm.PlayerURL(true); got != "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=8272103f6e#t=5s" {
		t.Errorf("player %s", got)
	}
	if got := vm.PlayerURL(false); got != "https://player.vimeo.com/video/76979871?dnt=1&h=8272103f6e#t=5s" {
		t.Errorf("still player %s", got)
	}
	if got := vm.InfoURL(); got != "https://vimeo.com/api/oembed.json?width=1280&url=https%3A%2F%2Fvimeo.com%2F76979871%2F8272103f6e" {
		t.Errorf("oembed %s", got)
	}
	if vm.Thumbnail() != "" || yt.Thumbnail() != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" {
		t.Errorf("thumbnails %q %q", vm.Thumbnail(), yt.Thumbnail())
	}
	f := Video{Provider: "file", ID: "https://example.com/clips/a%20b.mp4?sig=1", Start: 60}
	if f.PlayerURL(true) != "https://example.com/clips/a%20b.mp4?sig=1#t=60" || f.WatchURL() != f.PlayerURL(true) {
		t.Errorf("file player %s, watch %s", f.PlayerURL(true), f.WatchURL())
	}
	if f.InfoURL() != "" || f.Thumbnail() != "" || f.FileName() != "a b.mp4" || yt.FileName() != "" {
		t.Errorf("file info %q, thumbnail %q, name %q", f.InfoURL(), f.Thumbnail(), f.FileName())
	}

	for _, tc := range []struct {
		v                  Video
		watch, still, info string
	}{
		{Video{Provider: "bilibili", ID: "BV1KofNYjEuK", Part: 2, Start: 30},
			"https://www.bilibili.com/video/BV1KofNYjEuK?p=2&t=30",
			"https://player.bilibili.com/player.html?autoplay=0&bvid=BV1KofNYjEuK&high_quality=1&p=2&t=30",
			"https://api.bilibili.com/x/web-interface/view?bvid=BV1KofNYjEuK"},
		{Video{Provider: "bilibili", ID: "av573719854"},
			"https://www.bilibili.com/video/av573719854",
			"https://player.bilibili.com/player.html?aid=573719854&autoplay=0&high_quality=1",
			"https://api.bilibili.com/x/web-interface/view?aid=573719854"},
		{Video{Provider: "dailymotion", ID: "x8j5v1k", Start: 12},
			"https://www.dailymotion.com/video/x8j5v1k",
			"https://geo.dailymotion.com/player.html?video=x8j5v1k&startTime=12",
			"https://www.dailymotion.com/services/oembed?url=https%3A%2F%2Fwww.dailymotion.com%2Fvideo%2Fx8j5v1k"},
		{Video{Provider: "niconico", ID: "sm9", Start: 30},
			"https://www.nicovideo.jp/watch/sm9?from=30",
			"https://embed.nicovideo.jp/watch/sm9?from=30",
			"https://ext.nicovideo.jp/api/getthumbinfo/sm9"},
		{Video{Provider: "niconico", ID: "sm9"},
			"https://www.nicovideo.jp/watch/sm9",
			"https://embed.nicovideo.jp/watch/sm9",
			"https://ext.nicovideo.jp/api/getthumbinfo/sm9"},
	} {
		if got := tc.v.WatchURL(); got != tc.watch {
			t.Errorf("%+v: watch %s", tc.v, got)
		}
		if got := tc.v.PlayerURL(false); got != tc.still {
			t.Errorf("%+v: still player %s", tc.v, got)
		}
		if got := tc.v.InfoURL(); got != tc.info {
			t.Errorf("%+v: info %s", tc.v, got)
		}
		if v, ok := ParseVideoURL(tc.watch); !ok || v.ID != tc.v.ID || v.Part != tc.v.Part {
			t.Errorf("%s reads as %+v", tc.watch, v)
		}
	}
	bv := Video{Provider: "bilibili", ID: "BV1KofNYjEuK"}
	if got := bv.PlayerURL(true); got != "https://player.bilibili.com/player.html?autoplay=1&bvid=BV1KofNYjEuK&high_quality=1" {
		t.Errorf("bilibili player %s", got)
	}
	nico := Video{Provider: "niconico", ID: "sm9", Start: 30}
	if got := nico.PlayerURL(true); got != "https://embed.nicovideo.jp/watch/sm9?from=30" {
		t.Errorf("niconico player %s", got)
	}
}

func TestVideoInfo(t *testing.T) {
	for _, tc := range []struct {
		v                Video
		data             string
		title, thumbnail string
		fails            bool
	}{
		{Video{Provider: "youtube", ID: "dQw4w9WgXcQ"}, `{"title":" Never Gonna Give You Up ","thumbnail_url":"https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg"}`,
			"Never Gonna Give You Up", "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg", false},
		{Video{Provider: "bilibili", ID: "BV1884g6mE8K"}, `{"code":0,"message":"0","data":{"bvid":"BV1884g6mE8K","title":"师父！","pic":"http://i2.hdslb.com/bfs/archive/97c9.jpg"}}`,
			"师父！", "https://i2.hdslb.com/bfs/archive/97c9.jpg", false},
		{Video{Provider: "bilibili", ID: "BV1GJ411x7h7"}, `{"code":-404,"message":"啥都木有","ttl":1}`, "", "", true},
		{Video{Provider: "bilibili", ID: "BV1GJ411x7h7"}, `<!DOCTYPE html><html>`, "", "", true},
		{Video{Provider: "niconico", ID: "sm9"}, `<?xml version="1.0" encoding="UTF-8"?>
<nicovideo_thumb_response status="ok"><thumb><video_id>sm9</video_id><title>新・豪血寺一族 -煩悩解放 - レッツゴー！陰陽師</title>
<thumbnail_url>https://nicovideo.cdn.nimg.jp/thumbnails/9/9</thumbnail_url><tags><tag>sm9</tag></tags></thumb></nicovideo_thumb_response>`,
			"新・豪血寺一族 -煩悩解放 - レッツゴー！陰陽師", "https://nicovideo.cdn.nimg.jp/thumbnails/9/9", false},
		{Video{Provider: "niconico", ID: "sm1"}, `<nicovideo_thumb_response status="fail"><error><code>DELETED</code><description>deleted</description></error></nicovideo_thumb_response>`,
			"", "", true},
	} {
		title, thumb, err := tc.v.ReadInfo([]byte(tc.data))
		if title != tc.title || thumb != tc.thumbnail || (err != nil) != tc.fails {
			t.Errorf("%s: %q %q %v", tc.v.Provider, title, thumb, err)
		}
	}
}

func TestVideoBlocks(t *testing.T) {
	n, err := SpecToNode(map[string]any{"type": "video", "video": map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Never"}}, "", "Agent")
	if err != nil || n.Type != "video" || n.Str("url") != "https://youtu.be/dQw4w9WgXcQ" || n.Str("title") != "Never" {
		t.Fatalf("got %+v, %v", n, err)
	}
	if _, err := SpecToNode(map[string]any{"type": "video", "video": map[string]any{"url": "https://example.com/a.mp4"}}, "", "Agent"); err != nil {
		t.Errorf("a link to a video file made no video: %v", err)
	}
	if _, err := SpecToNode(map[string]any{"type": "video", "video": map[string]any{"url": "https://example.com/a"}}, "", "Agent"); err == nil {
		t.Error("a link that is not to a video made a video")
	}
	c := FromBlocks([]*Node{n})
	if views := BlockViews(c); views[0]["video"].(map[string]any)["url"] != "https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("view %v", views[0])
	}
	if md := PageMarkdown("P", c, MarkdownOptions{}); !strings.Contains(md, "[Never](https://youtu.be/dQw4w9WgXcQ)") {
		t.Errorf("markdown %q", md)
	}
	if c.PlainText() != "Never" {
		t.Errorf("plain %q", c.PlainText())
	}
}
