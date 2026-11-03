package agents

import (
	"fmt"
	"strings"
	"unicode"

	"leaflet/internal/store"
)

// Recordings' transcripts as agents read them.

// maxTranscript bounds the transcript an agent reads of one recording.
const maxTranscript = 40000

// TranscriptText is a transcript as lines of "[m:ss] text", with a line of
// only punctuation joined to the one before.
func TranscriptText(segs []store.Segment) string {
	var lines []string
	for _, s := range segs {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if strings.TrimFunc(text, func(r rune) bool { return unicode.IsPunct(r) }) == "" && len(lines) > 0 {
			lines[len(lines)-1] += text
			continue
		}
		at := s.StartMs / 1000
		line := fmt.Sprintf("[%d:%02d] ", at/60, at%60)
		if s.Speaker != "" {
			line += "Speaker " + s.Speaker + ": "
		}
		lines = append(lines, line+text)
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > maxTranscript {
		out = string(r[:maxTranscript]) + "\n[The rest of the transcript is cut off.]"
	}
	return out
}
