package doc

import (
	"regexp"
	"strings"
)

// ToMarkdown writes rich text in the small inline markdown dialect agents
// read and write: **bold**, *italic* or _italic_, `code`, ~~strike~~, and
// [text](url).
func (d Delta) ToMarkdown() string {
	var b strings.Builder
	for _, r := range d {
		// A page mention is a link to the page, which reads back as one.
		if m := r.Attrs.Mention; m != nil && m.Name != "" && m.Kind != "page" {
			b.WriteString("@" + m.Name)
			continue
		}
		text := r.Text
		if r.Attrs.Code {
			text = "`" + text + "`"
		} else {
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
		if r.Attrs.Href != "" {
			text = "[" + text + "](" + r.Attrs.Href + ")"
		}
		b.WriteString(text)
	}
	return b.String()
}

// FromMarkdown reads the inline markdown dialect.
func FromMarkdown(markdown string) Delta {
	var out Delta
	parseInline([]rune(markdown), Attrs{}, &out)
	return out.Compact()
}

var linkRe = regexp.MustCompile(`^\[([^\]]+)\]\(([^)\s]+)\)`)

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
		if c == '\\' && i+1 < len(s) && strings.ContainsRune("*_`~[]()\\", s[i+1]) {
			plain.WriteRune(s[i+1])
			i += 2
			continue
		}
		if c == '`' {
			if end := indexRune(s, '`', i+1); end > i+1 {
				flush()
				a := attrs
				a.Code = true
				*out = append(*out, Run{Text: string(s[i+1 : end]), Attrs: a})
				i = end + 1
				continue
			}
		}
		if c == '[' {
			if m := linkRe.FindStringSubmatch(string(s[i:])); m != nil {
				flush()
				// A link to a page is a mention of it; one to a block of a
				// page stays a link, with its own text.
				if id, ok := PageLinkID(m[2]); ok && LinkBlock(m[2]) == "" {
					text := FromMarkdown(m[1]).Plain()
					*out = append(*out, Run{Text: text, Attrs: PageMention(id, text)})
					i += len([]rune(m[0]))
					continue
				}
				a := attrs
				a.Href = m[2]
				parseInline([]rune(m[1]), a, out)
				i += len([]rune(m[0]))
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

func findSingle(s []rune, marker rune, from int) int {
	for j := from; j < len(s); j++ {
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

func isWord(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func indexRune(s []rune, r rune, from int) int {
	for j := from; j < len(s); j++ {
		if s[j] == r {
			return j
		}
	}
	return -1
}

func indexString(s []rune, sub string, from int) int {
	m := []rune(sub)
	for j := from; j+len(m) <= len(s); j++ {
		if string(s[j:j+len(m)]) == sub {
			return j
		}
	}
	return -1
}
