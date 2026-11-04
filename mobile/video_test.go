package mobile

import (
	"encoding/json"
	"strings"
	"testing"
)

// The iOS app draws a video's notes, plays it from their times and follows
// links to its moments as the desktop does.
func TestVideoNotesAndMoments(t *testing.T) {
	var lines []noteLine
	if err := json.Unmarshal([]byte(VideoNotes("A talk.\n1:23 Caches, see 4:56\n")), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].At != -1 || lines[0].Time != "" || lines[1].At != 83 || lines[1].Time != "1:23" ||
		len(lines[1].Parts) != 2 || lines[1].Parts[1] != (notePart{Text: "4:56", At: 296}) {
		t.Errorf("lines %+v", lines)
	}
	if VideoNotes("") != "[]" {
		t.Errorf("no notes: %s", VideoNotes(""))
	}
	var p videoPlayer
	if err := json.Unmarshal([]byte(VideoPlayerAt("https://youtu.be/dQw4w9WgXcQ?t=5", 83)), &p); err != nil || !strings.Contains(p.Page, "start=83") {
		t.Errorf("player at 83: %+v, %v", p, err)
	}
	if err := json.Unmarshal([]byte(VideoPlayer("https://youtu.be/dQw4w9WgXcQ?t=5")), &p); err != nil || !strings.Contains(p.Page, "start=5") {
		t.Errorf("player: %+v, %v", p, err)
	}
	if s := VideoSeek("https://youtu.be/dQw4w9WgXcQ", 83); !strings.Contains(s, `leafletSeek(document.querySelector("iframe, video"), "youtube", 83)`) {
		t.Errorf("seek %s", s)
	}
	if VideoSeek("https://www.bilibili.com/video/BV1KofNYjEuK", 83) != "" || VideoSeek("nope", 1) != "" {
		t.Error("a player that can't seek in place got a script")
	}
	if BlockLink("p1", "b1") != "leaflet://page/p1#b1" {
		t.Errorf("block link %q", BlockLink("p1", "b1"))
	}
	link := MomentLink("p1", "b1", 83)
	if LinkedBlock(link) != "b1" || LinkedMoment(link) != 83 || LinkedMoment("leaflet://page/p1#b1") != -1 || LinkTarget(link) != link {
		t.Errorf("%s: block %q, moment %d, target %q", link, LinkedBlock(link), LinkedMoment(link), LinkTarget(link))
	}
	if NormalizeNotes(" a\r\nb ") != "a\nb" || Clock(3723) != "1:02:03" {
		t.Error("notes or clock")
	}
}
