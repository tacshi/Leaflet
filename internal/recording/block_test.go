package recording

import (
	"testing"
	"time"

	"leaflet/internal/store"
	"leaflet/locales"
)

// A recording's length shows as minutes and seconds, with hours once it
// has them.
func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0:00", 65 * time.Second: "1:05", time.Hour + 2*time.Minute + 5*time.Second: "1:02:05",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// A block's title is its own, an older block's English one in the app's
// words with its time, and Recording when it has none or is the page's.
func TestBlockTitle(t *testing.T) {
	made := time.Date(2026, 1, 2, 9, 30, 0, 0, time.Local)
	cases := []struct {
		title string
		found bool
		page  string
		want  string
	}{
		{"Standup", true, "Notes", "Standup"},
		{"", true, "Notes", locales.CommonRecording()},
		{"Standup", true, " Standup ", locales.CommonRecording()},
		{"Recording · Jan 2", true, "Notes", locales.RecBlockTitle(made)},
		// Not on this device, its time is not known.
		{"Recording · Jan 2", false, "Notes", "Recording · Jan 2"},
	}
	for _, c := range cases {
		if got := BlockTitle(c.title, made, c.found, c.page); got != c.want {
			t.Errorf("BlockTitle(%q, %v, %q) = %q, want %q", c.title, c.found, c.page, got, c.want)
		}
	}
}

// The status says a transcription running or failed, or the length and
// what transcribed it.
func TestStatus(t *testing.T) {
	minute := 65_000
	cases := []struct {
		rec  store.Recording
		want string
	}{
		{store.Recording{Status: "transcribing"}, locales.RecTranscribing()},
		{store.Recording{Status: "failed", SttProvider: "soniox"}, locales.RecTranscriptionFailed()},
		{store.Recording{Status: "ready", DurationMs: minute}, locales.RecStatus("1:05", locales.RecAudioOnly())},
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: "soniox"}, locales.RecStatus("1:05", "Soniox")},
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: OnDevice}, locales.RecStatus("1:05", locales.RecEngineOnDevice())},
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: "openai-compatible"}, locales.RecStatus("1:05", locales.CommonEndpoint())},
	}
	for _, c := range cases {
		if got := Status(c.rec); got != c.want {
			t.Errorf("Status(%+v) = %q, want %q", c.rec, got, c.want)
		}
	}
}

// A transcript copies as a line each, tidied, after its speaker's name
// when it has one.
func TestTranscriptText(t *testing.T) {
	rec := store.Recording{Transcript: []store.Segment{
		{Text: "Hello", Speaker: "1"},
		{Text: "."},
		{Text: "Shall we start?"},
	}}
	want := locales.RecSpeakerLine("1", "Hello.") + "\nShall we start?"
	if got := TranscriptText(rec); got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
}
