package web

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"leaflet/internal/golden"
)

func TestReadHTMLOfPages(t *testing.T) {
	var cases []struct {
		Page string `json:"page"`
		HTML string `json:"html"`
		Read struct {
			Links []string `json:"links"`
		} `json:"read"`
	}
	f := golden.Open(t, "pages.json")
	f.Decode(&cases)
	for i, c := range cases {
		p := ReadHTML(c.Page, c.HTML)
		f.Check(p.Title, i, "read", "title")
		f.Check(p.Text, i, "read", "text")
		f.Check(p.Truncated, i, "read", "truncated")
		if !slices.Equal(p.Links, c.Read.Links) {
			f.Mismatch(append([]string{}, p.Links...), i, "read", "links")
		}
	}
}

type trackCase struct {
	Player json.RawMessage `json:"player,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
	Value  *Track          `json:"value,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func TestCaptionsRead(t *testing.T) {
	type outcome struct {
		Value *Track `json:"value"`
		Error string `json:"error"`
	}
	var cases struct {
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
	f := golden.Open(t, "captions.json")
	f.Decode(&cases)
	if len(cases.YouTubeTracks) == 0 || len(cases.YouTubeLines) == 0 || len(cases.VimeoTracks) == 0 || len(cases.VTTLines) == 0 || len(cases.Passages) == 0 {
		t.Fatal("the golden file has no cases")
	}
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
	same := func(got Track, err error, want outcome) bool {
		if err != nil {
			return kind(err) == want.Error
		}
		return want.Value != nil && got == *want.Value
	}
	made := func(c trackCase, got Track, err error) trackCase {
		if err != nil {
			c.Error = kind(err)
			return c
		}
		c.Value = &got
		return c
	}
	for i, c := range cases.YouTubeTracks {
		if got, err := YouTubeTrack([]byte(c.Player)); !same(got, err, c.outcome) {
			f.Mismatch(made(trackCase{Player: f.Raw("youtube_tracks", i, "player")}, got, err), "youtube_tracks", i)
		}
	}
	for i, c := range cases.VimeoTracks {
		if got, err := VimeoTrack([]byte(c.Config)); !same(got, err, c.outcome) {
			f.Mismatch(made(trackCase{Config: f.Raw("vimeo_tracks", i, "config")}, got, err), "vimeo_tracks", i)
		}
	}
	lines := func(got, want []Caption, path ...any) {
		if len(got) == 0 && len(want) == 0 || reflect.DeepEqual(got, want) {
			return
		}
		f.Mismatch(append([]Caption{}, got...), path...)
	}
	for i, c := range cases.YouTubeLines {
		lines(YouTubeLines([]byte(c.XML)), c.Lines, "youtube_lines", i, "lines")
	}
	for i, c := range cases.VTTLines {
		lines(VTTLines(c.VTT), c.Lines, "vtt_lines", i, "lines")
	}
	for i, c := range cases.Passages {
		lines(Passages(c.Lines), c.Passages, "passages", i, "passages")
	}
}
