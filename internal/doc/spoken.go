package doc

import "strings"

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

func NoteText(l NoteLine) string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
