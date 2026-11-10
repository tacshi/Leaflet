package doc

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxNotes = 200000

type NoteLine struct {
	At      int
	Parts   []NotePart
	Line    int
	Speaker string
}

type NotePart struct {
	Text string
	At   int
}

func Clock(sec int) string {
	sec = max(sec, 0)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func ParseClock(s string) (int, bool) {
	n, ok := clockLen(s)
	if !ok || n != len(s) {
		return 0, false
	}
	return clockSeconds(s), true
}

func clockLen(s string) (int, bool) {
	var groups []int
	i := 0
	for {
		j := i
		for j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return 0, false
		}
		groups = append(groups, j-i)
		i = j
		if len(groups) == 3 || i+1 >= len(s) || s[i] != ':' || s[i+1] < '0' || s[i+1] > '9' {
			break
		}
		i++
	}
	switch {
	case len(groups) < 2 || groups[len(groups)-1] != 2:
		return 0, false
	case len(groups) == 3 && (groups[0] > 2 || groups[1] != 2):
		return 0, false
	}
	parts := strings.Split(s[:i], ":")
	for _, p := range parts[1:] {
		if p[0] > '5' {
			return 0, false
		}
	}
	return i, true
}

func clockSeconds(s string) int {
	sec := 0
	for p := range strings.SplitSeq(s, ":") {
		n, _ := strconv.Atoi(p)
		sec = sec*60 + n
	}
	return sec
}

func NormalizeNotes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func NoteLines(notes string) []NoteLine {
	var out []NoteLine
	for line := range strings.SplitSeq(NormalizeNotes(notes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		at, rest := leadingTime(line)
		out = append(out, NoteLine{At: at, Parts: noteParts(rest)})
	}
	return out
}

func leadingTime(line string) (int, string) {
	s := line
	bracket := strings.HasPrefix(s, "[")
	if bracket {
		s = s[1:]
	}
	n, ok := clockLen(s)
	if !ok {
		return -1, line
	}
	at, rest := clockSeconds(s[:n]), s[n:]
	if bracket {
		var closed bool
		if rest, closed = strings.CutPrefix(rest, "]"); !closed {
			return -1, line
		}
	}
	if runsOn(rest) {
		return -1, line
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if r, size := utf8.DecodeRuneInString(rest); rest != "" && strings.ContainsRune(noteSeparators, r) {
		rest = strings.TrimLeftFunc(rest[size:], unicode.IsSpace)
	}
	return at, rest
}

const noteSeparators = "-–—:|·"

func noteParts(text string) []NotePart {
	var parts []NotePart
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			continue
		}
		n, ok := clockLen(text[i:])
		if !ok || !standsAlone(text, i, i+n) {
			continue
		}
		if i > start {
			parts = append(parts, NotePart{Text: text[start:i], At: -1})
		}
		parts = append(parts, NotePart{Text: text[i : i+n], At: clockSeconds(text[i : i+n])})
		start = i + n
		i += n - 1
	}
	if start < len(text) {
		parts = append(parts, NotePart{Text: text[start:], At: -1})
	}
	return parts
}

func standsAlone(text string, i, j int) bool {
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	if r, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && (word(r) || r == ':' || r == '.') {
		return false
	}
	return !runsOn(text[j:])
}

func runsOn(rest string) bool {
	r, size := utf8.DecodeRuneInString(rest)
	switch {
	case rest == "":
		return false
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return true
	}
	return (r == ':' || r == '.') && size < len(rest) && rest[size] >= '0' && rest[size] <= '9'
}

func (l NoteLine) Text() string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
