package recording

import (
	"fmt"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A recording's block, as the desktop and the iOS app both draw it: its
// title and the status under it, and its transcript put in the page.

// FormatDuration is a recording's length as its block shows it: 3:05, or
// 1:02:05.
func FormatDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

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
	engine := locales.CommonEndpoint()
	switch rec.SttProvider {
	case "":
		engine = locales.RecAudioOnly()
	case "soniox":
		engine = "Soniox"
	case OnDevice:
		engine = locales.RecEngineOnDevice()
	}
	return locales.RecStatus(length, engine)
}

// TranscriptBlocks is rec's transcript as blocks to put in its page: a
// paragraph a line, tidied, each after its time in bold.
func TranscriptBlocks(rec store.Recording) []*doc.Node {
	nodes := []*doc.Node{}
	for _, s := range Tidy(rec.Transcript) {
		n := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		n.SetDelta(doc.Delta{
			{Text: FormatDuration(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}},
			{Text: s.Text},
		})
		nodes = append(nodes, n)
	}
	return nodes
}
