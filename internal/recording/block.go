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

func FormatDuration(d time.Duration) string { return doc.Clock(int(d.Seconds())) }

func BlockTitle(title string, created time.Time, found bool, page string) string {
	if day := created.Local(); found && !created.IsZero() && title == "Recording · "+day.Format("Jan 2") {
		title = locales.RecBlockTitle(day)
	}
	if title == "" || strings.TrimSpace(page) == title {
		return locales.CommonRecording()
	}
	return title
}

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

func TranscriptText(rec store.Recording) string {
	var lines []string
	for _, s := range Tidy(rec.Transcript) {
		lines = append(lines, spoken(s))
	}
	return strings.Join(lines, "\n")
}

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

func spoken(s store.Segment) string {
	if s.Speaker != "" {
		return locales.RecSpeakerLine(s.Speaker, s.Text)
	}
	return s.Text
}

const (
	TranscriptTxt = "txt"
	TranscriptSrt = "srt"
	TranscriptVtt = "vtt"
)

const cueLasts = 3000

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

var vttEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

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

func cueTime(ms int, mark string) string {
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, mark, ms%1000)
}
