package doc

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A video block's notes are its own lines of text, under its player, as a
// video's description is on its site: a time in them plays the video from
// there, and one that starts a line makes the line a moment, with its time
// beside it, as a recording's transcript has.

// MaxNotes is the most characters a video's notes hold, as on the server.
const MaxNotes = 20000

// NoteLine is a line of a video's notes: the time it starts with, in
// seconds, or -1, and its text after that time, in parts.
type NoteLine struct {
	At    int
	Parts []NotePart
}

// NotePart is a piece of a note's text: a time in it, at seconds into the
// video, or text, at -1.
type NotePart struct {
	Text string
	At   int
}

// Clock writes seconds as a video or a recording shows a time: 3:05, or
// 1:02:05.
func Clock(sec int) string {
	sec = max(sec, 0)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// ParseClock reads a time as Clock writes it, or as people do: 1:23,
// 01:23, 75:00 or 1:02:03, in seconds.
func ParseClock(s string) (int, bool) {
	n, ok := clockLen(s)
	if !ok || n != len(s) {
		return 0, false
	}
	return clockSeconds(s), true
}

// clockLen is how long the time that s starts with is, in bytes: minutes
// and seconds, the minutes up to three digits, or hours, minutes and
// seconds, two digits each but the hours.
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

// clockSeconds is the seconds of a time clockLen found.
func clockSeconds(s string) int {
	sec := 0
	for p := range strings.SplitSeq(s, ":") {
		n, _ := strconv.Atoi(p)
		sec = sec*60 + n
	}
	return sec
}

// NormalizeNotes is a video's notes as a block keeps them: a line break
// for each of any kind, and no space around them.
func NormalizeNotes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// NoteLines reads a video's notes as their block shows them, its blank
// lines left out. A time at a line's start, in brackets or not, is the
// line's time, with a dash or colon after it left out as well.
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

// leadingTime is the time a line starts with and what follows it, or -1
// and the line.
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
	// The time stands alone: 1:23pm is not one.
	if runsOn(rest) {
		return -1, line
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if r, size := utf8.DecodeRuneInString(rest); rest != "" && strings.ContainsRune(noteSeparators, r) {
		rest = strings.TrimLeftFunc(rest[size:], unicode.IsSpace)
	}
	return at, rest
}

// noteSeparators may follow a line's time, before its text.
const noteSeparators = "-–—:|·"

// noteParts splits text at the times in it, each standing alone: not part
// of a longer number or a word, as 12:34:56:78, 1.2:30 and 10:30am are.
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

// standsAlone tells whether the time at text[i:j] is not part of
// something longer: no letter or digit next to it, nor a colon or a point
// before it, nor one after it that more digits follow.
func standsAlone(text string, i, j int) bool {
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	if r, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && (word(r) || r == ':' || r == '.') {
		return false
	}
	return !runsOn(text[j:])
}

// runsOn reports whether what follows a time makes it part of something
// longer: a letter or a digit, as 1:23pm, or a colon or a point before a
// digit, as 12:34:56:78 or 1:23.5.
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

// Text is the line's text without its time.
func (l NoteLine) Text() string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
