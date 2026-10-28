package doc

import (
	"strings"
	"unicode"
)

// ToMarkdown writes rich text in the small inline markdown dialect agents
// read and write: **bold**, *italic* or _italic_, `code`, ~~strike~~, and
// [text](url). Text that would read as markup is escaped with
// backslashes, so it reads back as written.
func (d Delta) ToMarkdown() string {
	var b strings.Builder
	for _, r := range d {
		// A page mention is a link to the page, which reads back as one.
		if m := r.Attrs.Mention; m != nil && m.Name != "" && m.Kind != "page" {
			b.WriteString("@" + m.Name)
			continue
		}
		var text string
		if r.Attrs.Code {
			text = codeSpan(r.Text)
		} else {
			// Spaces at either end go outside the markers, which must
			// touch the text to read as markers.
			core := strings.TrimSpace(r.Text)
			lead, _, _ := strings.Cut(r.Text, core)
			trail := r.Text[len(lead)+len(core):]
			text = escapeText(core)
			if core != "" {
				if r.Attrs.Strike {
					text = "~~" + text + "~~"
				}
				if r.Attrs.Italic {
					text = "*" + text + "*"
				}
				if r.Attrs.Bold {
					text = "**" + text + "**"
				}
			}
			text = lead + text + trail
		}
		if r.Attrs.Href != "" {
			text = "[" + text + "](" + linkDestination(r.Attrs.Href) + ")"
		}
		b.WriteString(text)
	}
	return b.String()
}

// escapeText escapes the characters that would read as markup: all the
// markers but a _ inside a word, which can't start or end italics.
func escapeText(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, c := range runes {
		switch c {
		case '\\', '*', '`', '~', '[', ']':
			b.WriteByte('\\')
		case '_':
			if i == 0 || i+1 == len(runes) || !isWord(runes[i-1]) || !isWord(runes[i+1]) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// codeSpan writes code between more backticks than any run of them in it,
// with a space inside each end where it starts or ends with a backtick or
// a space, as CommonMark reads code spans.
func codeSpan(s string) string {
	fence, run := 1, 0
	for _, c := range s {
		if c == '`' {
			run++
			fence = max(fence, run+1)
		} else {
			run = 0
		}
	}
	ticks := strings.Repeat("`", fence)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") ||
		strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ") && strings.TrimSpace(s) != "" {
		s = " " + s + " "
	}
	return ticks + s + ticks
}

// linkDestination writes a link's url, in angle brackets when it holds
// what would end it or read as markup: parentheses, spaces, backslashes.
func linkDestination(href string) string {
	if !strings.ContainsAny(href, "()<>\\ \t\n") {
		return href
	}
	r := strings.NewReplacer("\\", "\\\\", "<", "\\<", ">", "\\>", "\n", "%0A")
	return "<" + r.Replace(href) + ">"
}

// EscapeBlockStart escapes what at the start of a block's text would
// read as another kind of block: a heading's #, a list item's marker, a
// quote's >, a table's |, a math block's $$, or a rule.
func EscapeBlockStart(md string) string {
	s := strings.TrimLeft(md, " \t")
	lead := md[:len(md)-len(s)]
	hashes := strings.TrimLeft(s, "#")
	digits := strings.TrimLeft(s, "0123456789")
	switch {
	case s == "":
		return md
	case len(hashes) < len(s) && len(s)-len(hashes) <= 6 && (hashes == "" || hashes[0] == ' '),
		s[0] == '>', s[0] == '|', strings.HasPrefix(s, "$$"), IsRule(s),
		(s[0] == '-' || s[0] == '+') && (len(s) == 1 || s[1] == ' '):
		return lead + "\\" + s
	case len(digits) < len(s) && len(s)-len(digits) <= 9 && digits != "" && (digits[0] == '.' || digits[0] == ')') &&
		(len(digits) == 1 || digits[1] == ' '):
		n := len(s) - len(digits)
		return lead + s[:n] + "\\" + s[n:]
	}
	return md
}

// FromMarkdown reads the inline markdown dialect.
func FromMarkdown(markdown string) Delta {
	var out Delta
	parseInline([]rune(markdown), Attrs{}, &out)
	return out.Compact()
}

// link reads [text](url) at the start of s: the text, the url and how many
// runes they take. As in CommonMark, the url may be in angle brackets or
// hold balanced parentheses, and backslashes escape in both.
func link(s []rune) (text []rune, href string, n int, ok bool) {
	i, depth := 1, 0
	for ; i < len(s); i++ {
		if s[i] == '\\' {
			i++
		} else if s[i] == '[' {
			depth++
		} else if s[i] == ']' {
			if depth == 0 {
				break
			}
			depth--
		}
	}
	if i == 1 || i+1 >= len(s) || s[i+1] != '(' {
		return nil, "", 0, false
	}
	text = s[1:i]
	var b strings.Builder
	j := i + 2
	if j < len(s) && s[j] == '<' {
		for j++; j < len(s) && s[j] != '>'; j++ {
			if s[j] == '<' || s[j] == '\n' {
				return nil, "", 0, false
			}
			if s[j] == '\\' && j+1 < len(s) && isPunct(s[j+1]) {
				j++
			}
			b.WriteRune(s[j])
		}
		if j+1 >= len(s) || s[j+1] != ')' {
			return nil, "", 0, false
		}
		n = j + 2
	} else {
		depth = 0
		for ; j < len(s); j++ {
			c := s[j]
			if unicode.IsSpace(c) {
				return nil, "", 0, false
			}
			if c == '\\' && j+1 < len(s) && isPunct(s[j+1]) {
				j++
				c = s[j]
			} else if c == '(' {
				depth++
			} else if c == ')' {
				if depth == 0 {
					break
				}
				depth--
			}
			b.WriteRune(c)
		}
		if j >= len(s) {
			return nil, "", 0, false
		}
		n = j + 1
	}
	if b.Len() == 0 {
		return nil, "", 0, false
	}
	return text, b.String(), n, true
}

// isPunct reports whether a backslash escapes c: ASCII punctuation.
func isPunct(c rune) bool {
	return c < 0x80 && (unicode.IsPunct(c) || unicode.IsSymbol(c))
}

func parseInline(s []rune, attrs Attrs, out *Delta) {
	var plain strings.Builder
	flush := func() {
		if plain.Len() == 0 {
			return
		}
		*out = append(*out, Run{Text: plain.String(), Attrs: attrs})
		plain.Reset()
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' && i+1 < len(s) && isPunct(s[i+1]) {
			plain.WriteRune(s[i+1])
			i += 2
			continue
		}
		if c == '`' {
			if code, n := codeSpanAt(s[i:]); n > 0 {
				flush()
				a := attrs
				a.Code = true
				*out = append(*out, Run{Text: code, Attrs: a})
				i += n
				continue
			}
		}
		if c == '[' {
			if text, href, n, ok := link(s[i:]); ok {
				flush()
				// A link to a page is a mention of it; one to a block of a
				// page stays a link, with its own text.
				if id, ok := PageLinkID(href); ok && LinkBlock(href) == "" {
					text := FromMarkdown(string(text)).Plain()
					*out = append(*out, Run{Text: text, Attrs: PageMention(id, text)})
					i += n
					continue
				}
				a := attrs
				a.Href = href
				parseInline(text, a, out)
				i += n
				continue
			}
		}
		if i+1 < len(s) && (c == '*' || c == '~' || c == '_') && s[i+1] == c {
			marker := string([]rune{c, c})
			if end := indexString(s, marker, i+2); end > i+2 {
				flush()
				a := attrs
				if c == '~' {
					a.Strike = true
				} else {
					a.Bold = true
				}
				parseInline(s[i+2:end], a, out)
				i = end + 2
				continue
			}
		}
		if c == '*' || c == '_' {
			prevIsWord := i > 0 && isWord(s[i-1])
			if c != '_' || !prevIsWord {
				if end := findSingle(s, c, i+1); end > i+1 {
					flush()
					a := attrs
					a.Italic = true
					parseInline(s[i+1:end], a, out)
					i = end + 1
					continue
				}
			}
		}
		plain.WriteRune(c)
		i++
	}
	flush()
}

// codeSpanAt reads a code span at the start of s, between runs of as many
// backticks, and how many runes it takes; 0 when there is none. A space
// inside each end comes off, as CommonMark has it.
func codeSpanAt(s []rune) (string, int) {
	open := 0
	for open < len(s) && s[open] == '`' {
		open++
	}
	for j := open; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		end := j
		for end < len(s) && s[end] == '`' {
			end++
		}
		if end-j == open {
			code := string(s[open:j])
			if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
				code = code[1 : len(code)-1]
			}
			if code == "" {
				return "", 0
			}
			return code, end
		}
		j = end
	}
	return "", 0
}

func findSingle(s []rune, marker rune, from int) int {
	for j := from; j < len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if s[j] != marker {
			continue
		}
		if j+1 < len(s) && s[j+1] == marker {
			j++
			continue
		}
		if marker == '_' && j+1 < len(s) && isWord(s[j+1]) {
			continue
		}
		if j > from && s[j-1] == ' ' {
			continue
		}
		return j
	}
	return -1
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

func indexString(s []rune, sub string, from int) int {
	m := []rune(sub)
	for j := from; j+len(m) <= len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if string(s[j:j+len(m)]) == sub {
			return j
		}
	}
	return -1
}
