package core

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
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
	for _, line := range strings.SplitAfter(md, "\n") {
		text := strings.TrimRight(line, "\r\n")
		switch {
		case fence != "":
			b.WriteString(line)
			if closesFence(text, fence) {
				fence = ""
			}
			continue
		case opensFence(text) != "":
			flush()
			fence, inPara = opensFence(text), false
			b.WriteString(line)
			continue
		case strings.TrimSpace(text) == "":
			para.WriteString(line)
			flush()
			inPara = false
			continue
		case !inPara && (strings.HasPrefix(text, "    ") || strings.HasPrefix(text, "\t")):
			flush()
			b.WriteString(line)
			continue
		}
		para.WriteString(line)
		inPara = true
	}
	flush()
	return b.String()
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
// or ~~~, or "".
func opensFence(line string) string {
	line = trimIndent(line)
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(line) && line[n] == ch {
			n++
		}
		if n >= 3 {
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
