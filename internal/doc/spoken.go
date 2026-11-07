package doc

import "strings"

// What is being said as audio plays: the line of a transcript, by its time.

// CurrentLine is the index of the line of lines being said at sec seconds
// in: the last that has started, or -1 before the first. A line without a
// time goes with the one before it.
func CurrentLine(lines []NoteLine, sec float64) int {
	current := -1
	for i, l := range lines {
		if l.At < 0 {
			continue
		}
		if float64(l.At) > sec {
			break
		}
		current = i
	}
	return current
}

// NoteText is what a line of notes says, with the times in it.
func NoteText(l NoteLine) string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
