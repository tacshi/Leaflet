package core

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/agents"
	"leaflet/internal/store"
)

// Mentions of agents in comments, the same on the desktop and the phone:
// an @ and an agent's name, or @agent, as a whole word outside code, where
// the comment shows it marked and the agent answers in the thread.

// Mentioned is which of names md mentions where MarkMentions marks them,
// in lower case.
func Mentioned(md string, names []string) map[string]bool {
	out := map[string]bool{}
	MarkMentions(md, names, func(m string) string {
		out[strings.ToLower(m[1:])] = true
		return m
	})
	return out
}

// CommentAgent is the agent a comment mentions, to answer in its thread:
// one it names, the first as hub.Options lists them, or else the default
// for @agent, as Hub.Resolve picks it. mentioned is false when the comment
// mentions none, and the error says why the default can't run.
func CommentAgent(hub *agents.Hub, body string) (agent store.ModelChoice, mentioned bool, err error) {
	options := hub.Options()
	names := []string{"agent"}
	for _, o := range options {
		names = append(names, o.Name())
	}
	hit := Mentioned(body, names)
	for _, o := range options {
		if hit[strings.ToLower(o.Name())] {
			return o.Choice(), true, nil
		}
	}
	if !hit["agent"] {
		return store.ModelChoice{}, false, nil
	}
	o, err := hub.Resolve(nil)
	if err != nil {
		return store.ModelChoice{}, true, err
	}
	return o.Choice(), true, nil
}

// MarkMentions is md with each mention of one of names as mark has it:
// an @ and the name, a whole word, outside code, where it stays as
// written: fenced code blocks, indented ones, and code spans. The longest
// of names that fits is the one mentioned.
func MarkMentions(md string, names []string, mark func(mention string) string) string {
	names = slices.Clone(names)
	slices.SortFunc(names, func(x, y string) int { return len(y) - len(x) })
	var b, para strings.Builder
	// A paragraph's lines are marked together, for a code span may go
	// over more than one of them.
	flush := func() {
		b.WriteString(markInline(para.String(), names, mark))
		para.Reset()
	}
	fence := ""
	// inPara is set while a paragraph goes on: an indented line then
	// goes on with it, not starting a code block.
	inPara := false
	// item is the column the text of the open list item starts at, or -1
	// outside one: a line indented that far goes on with the item, and is
	// read from there, so what is indented under an item is its text, not
	// code, unless it is indented as code from there.
	item := -1
	for _, line := range strings.SplitAfter(md, "\n") {
		text := strings.TrimRight(line, "\r\n")
		blank := strings.TrimSpace(text) == ""
		n, base := indent(text), 0
		if item >= 0 && n >= item {
			base = item
		}
		rest := dropIndent(text, base)
		if fence != "" {
			b.WriteString(line)
			if closesFence(rest, fence) {
				fence = ""
			}
			continue
		}
		// A line left of the item's text that neither goes on with its
		// paragraph nor starts an item ends the list.
		if item >= 0 && n < item && !blank && !inPara && itemText(text) < 0 {
			item = -1
		}
		switch {
		case opensFence(rest) != "":
			flush()
			fence, inPara = opensFence(rest), false
			b.WriteString(line)
			continue
		case blank:
			para.WriteString(line)
			flush()
			inPara = false
			continue
		case !inPara && n-base >= 4:
			flush()
			b.WriteString(line)
			continue
		}
		if w := itemText(rest); w >= 0 && n-base < 4 {
			item = base + w
		}
		para.WriteString(line)
		inPara = true
	}
	flush()
	return b.String()
}

// indent is how many columns of spaces and tabs line starts with, a tab
// going to the next column of four.
func indent(line string) int {
	n := 0
	for _, ch := range []byte(line) {
		switch ch {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

// dropIndent is line without the first cols columns of its indent.
func dropIndent(line string, cols int) string {
	n := 0
	for n < cols && line != "" && (line[0] == ' ' || line[0] == '\t') {
		if line[0] == '\t' {
			n += 4 - n%4
		} else {
			n++
		}
		line = line[1:]
	}
	return line
}

// itemText is the column a list item's text starts at, where line starts
// one, as - or 1. and a space, or -1.
func itemText(line string) int {
	rest := trimIndent(line)
	at := len(line) - len(rest)
	n := 0
	for n < len(rest) && n < 9 && '0' <= rest[n] && rest[n] <= '9' {
		n++
	}
	switch {
	case n > 0 && n < len(rest) && (rest[n] == '.' || rest[n] == ')'):
		n++
	case n == 0 && rest != "" && strings.IndexByte("-*+", rest[0]) >= 0:
		n = 1
	default:
		return -1
	}
	if n == len(rest) {
		return at + n + 1
	}
	if rest[n] != ' ' && rest[n] != '\t' {
		return -1
	}
	// The text starts after the marker's spaces, up to four; more are a
	// space and code indented in the item.
	if w := indent(rest[n:]); w <= 4 {
		return at + n + w
	}
	return at + n + 1
}

// markInline marks the mentions in a paragraph, outside its code spans; a
// backslash keeps the character after it, a backtick too, from starting
// one.
func markInline(s string, names []string, mark func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i++
			continue
		case ch == '`':
			// A span opens with a run of backticks and closes with a run
			// as long; one that never closes is backticks as written.
			n := backticks(s[i:])
			end := i + n
			if j := closingRun(s[end:], n); j >= 0 {
				end += j + n
			}
			b.WriteString(s[i:end])
			i = end - 1
			continue
		case ch == '@' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\n' || s[i-1] == '\t'):
			if n := mentionLen(s[i+1:], names); n > 0 {
				b.WriteString(mark(s[i : i+1+n]))
				i += n
				continue
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// backticks is how many backticks s starts with.
func backticks(s string) int {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	return n
}

// closingRun is where in s a run of exactly n backticks starts, or -1.
func closingRun(s string, n int) int {
	for j := 0; j < len(s); {
		if m := backticks(s[j:]); m > 0 {
			if m == n {
				return j
			}
			j += m
			continue
		}
		j++
	}
	return -1
}

// opensFence is the fence a line opens a fenced code block with, as ```
// or ~~~, or "". Backticks with a backtick after them on the line open
// none: they are a code span, as ```x``` is.
func opensFence(line string) string {
	line = trimIndent(line)
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(line) && line[n] == ch {
			n++
		}
		if n >= 3 && (ch != '`' || !strings.Contains(line[n:], "`")) {
			return line[:n]
		}
	}
	return ""
}

// closesFence reports whether a line closes the code block fence opened:
// a run of its character at least as long, alone on the line.
func closesFence(line, fence string) bool {
	line = trimIndent(line)
	n := 0
	for n < len(line) && line[n] == fence[0] {
		n++
	}
	return n >= len(fence) && strings.TrimSpace(line[n:]) == ""
}

// trimIndent drops the up to three spaces a fence may be indented by.
func trimIndent(line string) string {
	for i := 0; i < 3 && strings.HasPrefix(line, " "); i++ {
		line = line[1:]
	}
	return line
}

// mentionLen is how long the name of names that rest starts with is, a
// whole word, or 0.
func mentionLen(rest string, names []string) int {
	for _, name := range names {
		n := len(name)
		if n == 0 || len(rest) < n || !strings.EqualFold(rest[:n], name) {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(rest[n:]); !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return n
		}
	}
	return 0
}
