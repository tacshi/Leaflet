package recording

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"leaflet/internal/doc"
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

// A transcript saves with its times and speakers, in each kind of file: a
// line that doesn't say when it ends lasts until the next, and the last a
// moment, and subtitles' cues are a line each, in their own markup.
func TestTranscriptFile(t *testing.T) {
	segs := []store.Segment{
		{StartMs: 1500, EndMs: 3250, Text: "Hello\nthere.", Speaker: "1"},
		{StartMs: 65000, Text: "a < b & c > d."},
		{StartMs: 3661001, Text: "Last."},
	}
	speaker := locales.RecSpeakerLine("1", "Hello there.")
	for format, want := range map[string]string{
		TranscriptTxt: "0:01  " + speaker + "\n1:05  a < b & c > d.\n1:01:01  Last.\n",
		TranscriptSrt: "1\n00:00:01,500 --> 00:00:03,250\n" + speaker + "\n\n" +
			"2\n00:01:05,000 --> 01:01:01,001\na < b & c > d.\n\n" +
			"3\n01:01:01,001 --> 01:01:04,001\nLast.\n\n",
		TranscriptVtt: "WEBVTT\n\n00:00:01.500 --> 00:00:03.250\n<v " + locales.RecSpeaker("1") + ">Hello there.\n\n" +
			"00:01:05.000 --> 01:01:01.001\na &lt; b &amp; c &gt; d.\n\n" +
			"01:01:01.001 --> 01:01:04.001\nLast.\n\n",
	} {
		if got := TranscriptFile(segs, format); got != want {
			t.Errorf("%s:\n%q\nwant\n%q", format, got, want)
		}
	}
}

// The last line of a transcript, with no end and none after it, stays up
// a moment.
func TestTranscriptFileLastLine(t *testing.T) {
	got := TranscriptFile([]store.Segment{{StartMs: 1000, Text: "Only."}}, TranscriptSrt)
	if want := "1\n00:00:01,000 --> 00:00:04,000\nOnly.\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Lines put in a page keep who said them.
func TestTranscriptBlocksKeepSpeakers(t *testing.T) {
	nodes := TranscriptBlocks(store.Recording{Transcript: []store.Segment{{StartMs: 65000, Text: "Hi.", Speaker: "2"}}})
	if len(nodes) != 1 {
		t.Fatalf("got %d lines", len(nodes))
	}
	d := nodes[0].Delta()
	if len(d) != 2 || d[0].Text != "1:05  " || !d[0].Attrs.Bold || d[1].Text != locales.RecSpeakerLine("2", "Hi.") {
		t.Errorf("line: %+v", d)
	}
}

// A transcript reads as notes: a line each, tidied, after its time and
// speaker, which is what plays it from there.
func TestTranscriptNotes(t *testing.T) {
	notes, cut := TranscriptNotes([]store.Segment{
		{StartMs: 5000, Text: "Hello\nthere.", Speaker: "1"},
		{StartMs: 65000, Text: "Bye."},
		{StartMs: 70000, Text: " "},
	})
	if want := "0:05 " + locales.RecSpeakerLine("1", "Hello there.") + "\n1:05 Bye."; notes != want || cut {
		t.Errorf("notes %q, cut %v", notes, cut)
	}
	lines := doc.NoteLines(notes)
	if len(lines) != 2 || lines[0].At != 5 || lines[1].At != 65 {
		t.Errorf("read back as %+v", lines)
	}
}

// What doesn't fit goes, a whole line at a time, and says so.
func TestTranscriptNotesCut(t *testing.T) {
	long := strings.Repeat("字", 60_000)
	segs := []store.Segment{{StartMs: 0, Text: long + "."}, {StartMs: 1000, Text: long + "."}, {StartMs: 2000, Text: long + "."}, {StartMs: 3000, Text: long + "."}}
	notes, cut := TranscriptNotes(segs)
	if !cut || utf8.RuneCountInString(notes) > doc.MaxNotes || len(doc.NoteLines(notes)) != 3 {
		t.Errorf("cut %v, %d characters, %d lines", cut, utf8.RuneCountInString(notes), len(doc.NoteLines(notes)))
	}
}
