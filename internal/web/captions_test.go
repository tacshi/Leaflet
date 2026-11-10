package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func TestYouTubeTrack(t *testing.T) {
	player := func(status, reason, tracks string) []byte {
		return []byte(`{"playabilityStatus":{"status":"` + status + `","reason":"` + reason + `"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[` + tracks +
			`],"audioTracks":[{"captionTrackIndices":[0,1],"defaultCaptionTrackIndex":0}]}}}`)
	}
	const (
		ja   = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&lang=ja","languageCode":"ja"}`
		en   = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&lang=en","languageCode":"en-US"}`
		auto = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&kind=asr&lang=en","languageCode":"en","kind":"asr"}`
	)
	for _, tc := range []struct {
		name string
		data []byte
		want Track
		err  error
	}{
		{"the creator's in the language spoken", player("OK", "", ja+","+auto+","+en), Track{URL: "https://www.youtube.com/api/timedtext?v=x&lang=en", Language: "en-US"}, nil},
		{"YouTube's own", player("OK", "", ja+","+auto), Track{URL: "https://www.youtube.com/api/timedtext?v=x&kind=asr&lang=en", Language: "en", Auto: true}, nil},
		{"the one YouTube shows first", player("OK", "", ja+","+en), Track{URL: "https://www.youtube.com/api/timedtext?v=x&lang=ja", Language: "ja"}, nil},
		{"none", player("OK", "", ""), Track{}, ErrNoCaptions},
		{"a robot, YouTube thinks", player("LOGIN_REQUIRED", "Sign in to confirm you’re not a bot", en), Track{}, ErrRefused},
		{"a token wanted", player("OK", "", `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&exp=xpe&lang=en","languageCode":"en"}`), Track{}, ErrRefused},
	} {
		got, err := YouTubeTrack(tc.data)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
	}
	var restricted *RestrictedError
	if _, err := YouTubeTrack(player("LOGIN_REQUIRED", "This video is private", en)); !errors.As(err, &restricted) || restricted.Reason != "This video is private" {
		t.Errorf("a private video: %v", err)
	}
	if _, err := YouTubeTrack([]byte("<html>")); err == nil {
		t.Error("no JSON read as a player")
	}
}

func TestYouTubeLines(t *testing.T) {
	srv3 := `<?xml version="1.0" encoding="utf-8" ?><timedtext format="3"><head><ws id="0"/></head><body>
<w t="0" id="1" wp="1" ws="1"/>
<p t="320" d="14260" w="1">[Music]</p>
<p t="18790" w="1" a="1">
</p>
<p t="18800" d="7160" w="1"><s ac="0">We&#39;re</s><s t="239" ac="0"> no</s><s t="559" ac="0"> strangers</s></p>
<p t="22640" d="4320">♪ You know the rules
and so do I ♪</p>
<p t="27040">Tom &amp; Jerry &lt;i&gt;shout&lt;/i&gt;</p>
</body></timedtext>`
	got := YouTubeLines([]byte(srv3))
	want := []Caption{{320, "[Music]"}, {18800, "We're no strangers"}, {22640, "♪ You know the rules and so do I ♪"}, {27040, "Tom & Jerry shout"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("srv3: %+v", got)
	}
	old := `<?xml version="1.0" encoding="utf-8" ?><transcript><text start="1.36" dur="1.68">Hello &amp;amp; welcome</text><text start="3.0405" dur="2">back</text></transcript>`
	if got := YouTubeLines([]byte(old)); !reflect.DeepEqual(got, []Caption{{1360, "Hello & welcome"}, {3041, "back"}}) {
		t.Errorf("older format: %+v", got)
	}
}

func TestCaptionTextKeepsWhatIsNoTag(t *testing.T) {
	for in, want := range map[string]string{
		"1 &lt; 2 and 3 &gt; 2":                            "1 < 2 and 3 > 2",
		"<i>if</i> x &lt; 3 <b>and</b> y &gt; 4 ok":        "if x < 3 and y > 4 ok",
		"press &lt;enter&gt; now":                          "press <enter> now",
		"Tom &amp;amp; Jerry &amp;lt;3":                    "Tom & Jerry <3",
		"&lt;i&gt;shout&lt;/i&gt; &lt;b&gt;loud&lt;/b&gt;": "shout loud",
		"&lt;font color=&quot;#E5E5E5&quot;&gt;so&lt;/font&gt; &lt;c.yellow&gt;it&lt;/c&gt; &lt;v Ann&gt;goes&lt;/v&gt;":                                        "so it goes",
		"&lt;00:00:01.500&gt;one &lt;01:02:03.000&gt;two &lt;ruby&gt;漢&lt;rt&gt;kan&lt;/rt&gt;&lt;/ruby&gt; &lt;lang en&gt;x&lt;/lang&gt; &lt;u&gt;y&lt;/u&gt;": "one two 漢kan x y",
		"a &lt;br&gt; b &lt;body&gt; c &lt;video&gt;": "a <br> b <body> c <video>",
	} {
		if got := captionText(in); got != want {
			t.Errorf("%q reads %q, want %q", in, got, want)
		}
	}
	if got := YouTubeLines([]byte(`<p t="1000">if x &lt; 3 and y &gt; 4 ok</p>`)); !reflect.DeepEqual(got, []Caption{{1000, "if x < 3 and y > 4 ok"}}) {
		t.Errorf("YouTube's line: %+v", got)
	}
	if got := VTTLines("WEBVTT\n\n00:01.000 --> 00:02.000\nif x &lt; 3 and y &gt; 4 ok\n"); !reflect.DeepEqual(got, []Caption{{1000, "if x < 3 and y > 4 ok"}}) {
		t.Errorf("the cue: %+v", got)
	}
}

func TestVimeoTrack(t *testing.T) {
	config := func(tracks string) []byte { return []byte(`{"request":{"lang":"en","text_tracks":[` + tracks + `]}}`) }
	const (
		de   = `{"lang":"de","url":"https://captions.vimeo.com/captions/170.vtt?sig=1","kind":"subtitles","default":true}`
		en   = `{"lang":"en","url":"https://captions.vimeo.com/captions/140662.vtt?sig=2","kind":"subtitles"}`
		fr   = `{"lang":"fr","url":"/texttrack/9.vtt?token=3","kind":"captions"}`
		auto = `{"lang":"fr-x-autogen","url":"https://captions.vimeo.com/captions/7.vtt?sig=4","kind":"captions"}`
	)
	for _, tc := range []struct {
		name, tracks string
		want         Track
	}{
		{"English over the translation shown first", de + "," + en, Track{URL: "https://captions.vimeo.com/captions/140662.vtt?sig=2", Language: "en"}},
		{"the one shown first", de, Track{URL: "https://captions.vimeo.com/captions/170.vtt?sig=1", Language: "de"}},
		{"captions, in the language spoken", de + "," + en + "," + fr, Track{URL: "https://player.vimeo.com/texttrack/9.vtt?token=3", Language: "fr"}},
		{"the creator's in the language Vimeo heard", de + "," + auto + "," + fr, Track{URL: "https://player.vimeo.com/texttrack/9.vtt?token=3", Language: "fr"}},
		{"Vimeo's own", en + "," + auto, Track{URL: "https://captions.vimeo.com/captions/7.vtt?sig=4", Language: "fr", Auto: true}},
	} {
		if got, err := VimeoTrack(config(tc.tracks)); got != tc.want || err != nil {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
	}
	if _, err := VimeoTrack(config("")); !errors.Is(err, ErrNoCaptions) {
		t.Errorf("no tracks: %v", err)
	}
	if _, err := VimeoTrack(config(`{"lang":"en","url":"javascript:alert(1)"}`)); !errors.Is(err, ErrNoCaptions) {
		t.Errorf("a track that is no web link: %v", err)
	}
}

func TestVTTLines(t *testing.T) {
	vtt := "WEBVTT\r\nKind: captions\r\n\r\nNOTE made by hand\r\n\r\nSTYLE\r\n::cue { color: red }\r\n\r\n1\r\n00:00:05.237 --> 00:00:08.043 align:start\r\n<i>Here at Vimeo, there's always</i>\r\n<i>one thing on our minds:</i>\r\n\r\n" +
		"01:02.5 --> 01:04.000\n<v Roger>Fish &amp; chips</v>\n\n2\n1:00:00.000 --> 1:00:02.000\n\n\nbad --> 00:00:01.000\nnot a cue\n"
	want := []Caption{{5237, "Here at Vimeo, there's always one thing on our minds:"}, {62500, "Fish & chips"}}
	if got := VTTLines(vtt); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestPassages(t *testing.T) {
	got := Passages([]Caption{{0, "Hello."}, {2000, "Today we look"}, {6500, "at caches."}, {9000, "They hit"}, {12000, "and they miss"},
		{30000, "until"}, {31000, "they don't."}, {38000, "「終わり」。"}, {45000, "Next"}})
	want := []Caption{{0, "Hello. Today we look at caches."}, {9000, "They hit and they miss"}, {30000, "until they don't."}, {38000, "「終わり」。"}, {45000, "Next"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestHasCaptions(t *testing.T) {
	for link, want := range map[string]bool{"https://youtu.be/dQw4w9WgXcQ": true, "https://vimeo.com/76979871": true,
		"https://www.bilibili.com/video/BV1KofNYjEuK": false, "https://example.com/a.mp4": false} {
		if v, _ := doc.ParseVideoURL(link); HasCaptions(v) != want {
			t.Errorf("%s: %v", link, !want)
		}
	}
}

func TestCaptionsWaitNoLongerThanFetch(t *testing.T) {
	was, wait := do, fetchWait
	fetchWait = 100 * time.Millisecond
	t.Cleanup(func() { do, fetchWait = was, wait })
	release := make(chan struct{})
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request":`))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	do = func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultClient.Do(req)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadCaptions(context.Background(), doc.Video{Provider: "vimeo", ID: "900000001"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || asked.Load() != 1 {
			t.Errorf("captions from a site that stops answering: %v, after asking %d times", err, asked.Load())
		}
	case <-time.After(5 * time.Second):
		t.Error("ReadCaptions still waits on a site that stopped answering")
	}
}
