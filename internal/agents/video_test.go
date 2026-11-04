package agents

import (
	"errors"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/web"
)

// read_video_transcript reads what is said in a video block of the page,
// from its site's captions, in passages after their times.
func TestReadVideoTranscript(t *testing.T) {
	c := doc.FromBlocks([]*doc.Node{
		{ID: "yt", Type: "video", Data: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "title": "Talk"}},
		{ID: "bili", Type: "video", Data: map[string]any{"url": "https://www.bilibili.com/video/BV1KofNYjEuK"}},
		{ID: "vm", Type: "video", Data: map[string]any{"url": "https://vimeo.com/76979871/8272103f6e"}},
		{ID: "rec", Type: "recording", Data: map[string]any{"recording_id": "r1"}},
		{ID: "p1", Type: "paragraph"},
	})
	var asked []doc.Video
	answer := map[string]error{}
	// Reading other pages is on, as only then may a run be kept to the
	// page's URLs.
	x := &Executor{Ctx: ToolContext{Content: c, PageID: "page", ListPages: func() []map[string]any { return nil }, Captions: func(v doc.Video) (web.Captions, error) {
		asked = append(asked, v)
		if err := answer[v.Provider]; err != nil {
			return web.Captions{}, err
		}
		return web.Captions{Language: "en", Auto: true, Lines: []web.Caption{{StartMs: 0, Text: "Hello."}, {StartMs: 2000, Text: "Today, caches."},
			{StartMs: 83000, Text: "They miss."}}}, nil
	}}}
	out := x.Call("read_video_transcript", map[string]any{"id": "yt"})
	if out["transcript"] != "[0:00] Hello. Today, caches.\n[1:23] They miss." || out["language"] != "en" || out["auto_generated"] != true || out["title"] != "Talk" {
		t.Errorf("read %v", out)
	}
	if len(asked) != 1 || asked[0].ID != "dQw4w9WgXcQ" {
		t.Errorf("asked for %+v", asked)
	}
	x.Call("read_video_transcript", map[string]any{"id": "vm"})
	if len(asked) != 2 || asked[1].Hash != "8272103f6e" {
		t.Errorf("asked for %+v", asked)
	}
	answer["youtube"] = web.ErrNoCaptions
	answer["vimeo"] = &web.RestrictedError{Reason: "private"}
	for id, want := range map[string]string{
		"nope": "No block", "p1": "not a video", "rec": "recording", "bili": "only in YouTube's and Vimeo's", "yt": "no captions", "vm": "private",
	} {
		if e, _ := x.Call("read_video_transcript", map[string]any{"id": id})["error"].(string); !strings.Contains(e, want) {
			t.Errorf("%s: %q", id, e)
		}
	}
	answer["youtube"] = web.ErrRefused
	if e, _ := x.Call("read_video_transcript", map[string]any{"id": "yt"})["error"].(string); !strings.Contains(e, "YouTube refused") {
		t.Errorf("refused: %q", e)
	}
	answer["youtube"] = errors.New("dial tcp: timeout")
	if e, _ := x.Call("read_video_transcript", map[string]any{"id": "yt"})["error"].(string); !strings.Contains(e, "timeout") {
		t.Errorf("failed: %q", e)
	}
	// Once the run has read the user's other pages, only a video the page
	// had as it began may be read: one it put there itself could be named
	// for what it read.
	x.armed = true
	answer["youtube"] = nil
	if out := x.Call("read_video_transcript", map[string]any{"id": "yt"}); out["error"] != nil {
		t.Errorf("a video the page had: %v", out)
	}
	c.Find("yt").Set("url", "https://youtu.be/aqz-KE-bpKQ")
	if e, _ := x.Call("read_video_transcript", map[string]any{"id": "yt"})["error"].(string); !strings.Contains(e, "cannot use the web") {
		t.Errorf("a video put there in the run: %q", e)
	}
	x.Ctx.Captions = nil
	if e, _ := x.Call("read_video_transcript", map[string]any{"id": "vm"})["error"].(string); !strings.Contains(e, "off") {
		t.Errorf("without captions: %q", e)
	}
}
