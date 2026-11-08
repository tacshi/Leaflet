package recording

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A recording's block, as the desktop and the iOS app both draw it: its
// title and the status under it, and its transcript put in the page.

// FormatDuration is a recording's length as its block shows it: 3:05, or
// 1:02:05.
func FormatDuration(d time.Duration) string { return doc.Clock(int(d.Seconds())) }

// BlockTitle is the name a recording block shows: its title, with the time
// it was made where the title, as older blocks', gives only the day, or
// just "Recording" on a page of the same name, as the menu bar makes, and
// for a block with no title. Older blocks were titled in English,
// "Recording · Jan 2". found says the recording, made at created, is on
// this device.
func BlockTitle(title string, created time.Time, found bool, page string) string {
	if day := created.Local(); found && !created.IsZero() && title == "Recording · "+day.Format("Jan 2") {
		title = locales.RecBlockTitle(day)
	}
	if title == "" || strings.TrimSpace(page) == title {
		return locales.CommonRecording()
	}
	return title
}

// Status is the line under a recording block's title as the recording
// rec has it, when nothing runs for it on this device: transcribing,
// failed, or its length and what transcribed it.
func Status(rec store.Recording) string {
	switch rec.Status {
	case "transcribing":
		return locales.RecTranscribing()
	case "failed":
		return locales.RecTranscriptionFailed()
	}
	length := FormatDuration(time.Duration(rec.DurationMs) * time.Millisecond)
	engine := EngineName(rec.SttProvider)
	if engine == "" {
		engine = locales.RecAudioOnly()
	}
	return locales.RecStatus(length, engine)
}

// EngineName is the engine of the speech settings' provider, as a
// transcript's line says what made it, or "" for none.
func EngineName(provider string) string {
	switch provider {
	case "":
		return ""
	case "soniox":
		return "Soniox"
	case Moss:
		return "MOSS"
	case OnDevice:
		return locales.RecEngineOnDevice()
	}
	return locales.CommonEndpoint()
}

// TranscriptBlocks is rec's transcript as blocks to put in its page: a
// paragraph a line, tidied, each after its time in bold and its speaker's
// name when it has one.
func TranscriptBlocks(rec store.Recording) []*doc.Node {
	nodes := []*doc.Node{}
	for _, s := range Tidy(rec.Transcript) {
		n := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		n.SetDelta(doc.Delta{
			{Text: FormatDuration(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}},
			{Text: spoken(s)},
		})
		nodes = append(nodes, n)
	}
	return nodes
}

// TranscriptText is rec's transcript as text to copy: a line each, tidied,
// after its speaker's name when it has one.
func TranscriptText(rec store.Recording) string {
	var lines []string
	for _, s := range Tidy(rec.Transcript) {
		lines = append(lines, spoken(s))
	}
	return strings.Join(lines, "\n")
}

// TranscriptNotes is a transcript as a block's notes, tidied: a line each,
// each after its time, which plays from there, and its speaker. What
// doesn't fit in doc.MaxNotes goes, and cut says so.
func TranscriptNotes(segs []store.Segment) (notes string, cut bool) {
	var b strings.Builder
	room := doc.MaxNotes
	for _, s := range Tidy(segs) {
		s.Text = strings.Join(strings.Fields(s.Text), " ")
		if s.Text == "" {
			continue
		}
		line := doc.Clock(s.StartMs/1000) + " " + spoken(s)
		count := utf8.RuneCountInString(line) + 1
		if count > room {
			cut = true
			break
		}
		room -= count
		b.WriteString(line + "\n")
	}
	return doc.NormalizeNotes(b.String()), cut
}

// FitNotes is notes, a transcript as TranscriptNotes writes it, in room
// characters: what doesn't fit goes, a line at a time from the end, and
// cut says so. Room for none of it leaves "".
func FitNotes(notes string, room int) (fit string, cut bool) {
	if utf8.RuneCountInString(notes) <= room {
		return notes, false
	}
	var b strings.Builder
	for line := range strings.SplitSeq(notes, "\n") {
		count := utf8.RuneCountInString(line) + 1
		if count > room {
			break
		}
		room -= count
		b.WriteString(line + "\n")
	}
	return doc.NormalizeNotes(b.String()), true
}

// spoken is a line's text, after its speaker's name when it has one.
func spoken(s store.Segment) string {
	if s.Speaker != "" {
		return locales.RecSpeakerLine(s.Speaker, s.Text)
	}
	return s.Text
}

// The kinds of file a transcript exports as.
const (
	// TranscriptTxt is plain text: a line each, after its time.
	TranscriptTxt = "txt"
	// TranscriptSrt is SubRip subtitles.
	TranscriptSrt = "srt"
	// TranscriptVtt is WebVTT subtitles, whose voices name the speakers.
	TranscriptVtt = "vtt"
)

// cueLasts is how long a line stays on screen when its end isn't known.
const cueLasts = 3000

// TranscriptFile is a transcript as a file of format, tidied, with its
// times and speakers. A cue's text is a single line, as players show it.
func TranscriptFile(segs []store.Segment, format string) string {
	lines := Tidy(segs)
	var b strings.Builder
	if format == TranscriptVtt {
		b.WriteString("WEBVTT\n\n")
	}
	cue := 0
	for i, s := range lines {
		s.Text = strings.Join(strings.Fields(s.Text), " ")
		if s.Text == "" {
			continue
		}
		cue++
		switch format {
		case TranscriptSrt:
			fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", cue, cueTime(s.StartMs, ","), cueTime(cueEnd(lines, i), ","), spoken(s))
		case TranscriptVtt:
			text := vttEscape.Replace(s.Text)
			if s.Speaker != "" {
				text = "<v " + vttEscape.Replace(locales.RecSpeaker(s.Speaker)) + ">" + text
			}
			fmt.Fprintf(&b, "%s --> %s\n%s\n\n", cueTime(s.StartMs, "."), cueTime(cueEnd(lines, i), "."), text)
		default:
			b.WriteString(FormatDuration(time.Duration(s.StartMs)*time.Millisecond) + "  " + spoken(s) + "\n")
		}
	}
	return b.String()
}

// vttEscape keeps a line's text from reading as WebVTT's own markup.
var vttEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// cueEnd is when line i of lines ends: as it says, or else when the next
// starts, or a moment after it starts. Cues can't end before they start.
func cueEnd(lines []store.Segment, i int) int {
	s := lines[i]
	if s.EndMs > s.StartMs {
		return s.EndMs
	}
	if i+1 < len(lines) && lines[i+1].StartMs > s.StartMs {
		return lines[i+1].StartMs
	}
	return s.StartMs + cueLasts
}

// cueTime is a time in a subtitle file: 00:01:05,250, with mark before
// its milliseconds, as SubRip and WebVTT each have it.
func cueTime(ms int, mark string) string {
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, mark, ms%1000)
}
