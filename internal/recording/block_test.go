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

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0:00", 65 * time.Second: "1:05", time.Hour + 2*time.Minute + 5*time.Second: "1:02:05",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

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
		{"Recording · Jan 2", false, "Notes", "Recording · Jan 2"},
	}
	for _, c := range cases {
		if got := BlockTitle(c.title, made, c.found, c.page); got != c.want {
			t.Errorf("BlockTitle(%q, %v, %q) = %q, want %q", c.title, c.found, c.page, got, c.want)
		}
	}
}

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
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: Moss}, locales.RecStatus("1:05", "MOSS")},
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: OnDevice}, locales.RecStatus("1:05", locales.RecEngineOnDevice())},
		{store.Recording{Status: "ready", DurationMs: minute, SttProvider: "openai-compatible"}, locales.RecStatus("1:05", locales.CommonEndpoint())},
	}
	for _, c := range cases {
		if got := Status(c.rec); got != c.want {
			t.Errorf("Status(%+v) = %q, want %q", c.rec, got, c.want)
		}
	}
}

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

func TestTranscriptFileLastLine(t *testing.T) {
	got := TranscriptFile([]store.Segment{{StartMs: 1000, Text: "Only."}}, TranscriptSrt)
	if want := "1\n00:00:01,000 --> 00:00:04,000\nOnly.\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

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

func TestTranscriptNotesCut(t *testing.T) {
	long := strings.Repeat("字", 60_000)
	segs := []store.Segment{{StartMs: 0, Text: long + "."}, {StartMs: 1000, Text: long + "."}, {StartMs: 2000, Text: long + "."}, {StartMs: 3000, Text: long + "."}}
	notes, cut := TranscriptNotes(segs)
	if !cut || utf8.RuneCountInString(notes) > doc.MaxNotes || len(doc.NoteLines(notes)) != 3 {
		t.Errorf("cut %v, %d characters, %d lines", cut, utf8.RuneCountInString(notes), len(doc.NoteLines(notes)))
	}
}

func TestFitNotes(t *testing.T) {
	notes := "0:05 Hello\n0:06 Bye"
	for _, c := range []struct {
		room int
		fit  string
		cut  bool
	}{
		{100, notes, false},
		{len(notes), notes, false},
		{len(notes) - 1, "0:05 Hello", true},
		{5, "", true},
		{-2, "", true},
	} {
		if fit, cut := FitNotes(notes, c.room); fit != c.fit || cut != c.cut {
			t.Errorf("room %d: %q, cut %v", c.room, fit, cut)
		}
	}
}

func TestEngineName(t *testing.T) {
	for provider, want := range map[string]string{
		"": "", "soniox": "Soniox", Moss: "MOSS", OnDevice: locales.RecEngineOnDevice(),
		"openai-compatible": locales.CommonEndpoint(),
	} {
		if got := EngineName(provider); got != want {
			t.Errorf("EngineName(%q) = %q, want %q", provider, got, want)
		}
	}
}
