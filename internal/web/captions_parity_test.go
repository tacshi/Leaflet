package web

import (
	"errors"
	"reflect"
	"testing"

	"leaflet/internal/parity"
)

// A video's captions read here as on the server (testdata/captions.json,
// which supabase/functions/_shared/captions_test.ts writes).
func TestCaptionsAsTheServerReadsThem(t *testing.T) {
	type outcome struct {
		Value *Track `json:"value"`
		Error string `json:"error"`
	}
	var f struct {
		YouTubeTracks []struct {
			Player string `json:"player"`
			outcome
		} `json:"youtube_tracks"`
		YouTubeLines []struct {
			XML   string    `json:"xml"`
			Lines []Caption `json:"lines"`
		} `json:"youtube_lines"`
		VimeoTracks []struct {
			Config string `json:"config"`
			outcome
		} `json:"vimeo_tracks"`
		VTTLines []struct {
			VTT   string    `json:"vtt"`
			Lines []Caption `json:"lines"`
		} `json:"vtt_lines"`
		Passages []struct {
			Lines    []Caption `json:"lines"`
			Passages []Caption `json:"passages"`
		} `json:"passages"`
	}
	parity.Read(t, "../captions.json", &f)
	if len(f.YouTubeTracks) == 0 || len(f.VimeoTracks) == 0 || len(f.VTTLines) == 0 {
		t.Fatal("the fixture has no cases")
	}
	fail := parity.Check(t)
	kind := func(err error) string {
		var restricted *RestrictedError
		switch {
		case errors.Is(err, ErrNoCaptions):
			return "none"
		case errors.Is(err, ErrRefused):
			return "refused"
		case errors.As(err, &restricted):
			return "restricted"
		}
		return "unreadable"
	}
	check := func(name, in string, got Track, err error, want outcome) {
		switch {
		case err != nil && kind(err) != want.Error:
			fail("%s %s: %v; the server's %+v", name, in, err, want)
		case err == nil && (want.Value == nil || got != *want.Value):
			fail("%s %s: %+v; the server's %+v", name, in, got, want)
		}
	}
	for _, c := range f.YouTubeTracks {
		got, err := YouTubeTrack([]byte(c.Player))
		check("youtube", c.Player, got, err, c.outcome)
	}
	for _, c := range f.VimeoTracks {
		got, err := VimeoTrack([]byte(c.Config))
		check("vimeo", c.Config, got, err, c.outcome)
	}
	same := func(a, b []Caption) bool { return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b) }
	for _, c := range f.YouTubeLines {
		if got := YouTubeLines([]byte(c.XML)); !same(got, c.Lines) {
			fail("%q read as\n%+v\nthe server's\n%+v", c.XML, got, c.Lines)
		}
	}
	for _, c := range f.VTTLines {
		if got := VTTLines(c.VTT); !same(got, c.Lines) {
			fail("%q read as\n%+v\nthe server's\n%+v", c.VTT, got, c.Lines)
		}
	}
	for _, c := range f.Passages {
		if got := Passages(c.Lines); !same(got, c.Passages) {
			fail("%+v joined as\n%+v\nthe server's\n%+v", c.Lines, got, c.Passages)
		}
	}
}
