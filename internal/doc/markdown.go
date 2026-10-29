package doc

import (
	"strings"
	"unicode"
)

// ToMarkdown writes rich text in the small inline markdown dialect agents
// read and write: **bold** or __bold__, *italic* or _italic_, `code`,
// ~~strike~~, and [text](url). Text that would read as markup is escaped with
// backslashes, so it reads back as written. A format over several runs is
// one span, the others nested in it, so runs side by side read back as
// they were.
func (d Delta) ToMarkdown() string {
	var toks []mdToken
	var open []mdMark
	for _, r := range trimSpans(d.Compact()) {
		want := marksOf(r.Attrs)
		k := 0
		for k < len(open) && k < len(want) && open[k] == want[k] {
			k++
		}
		for len(open) > k {
			toks = append(toks, mdToken{mark: open[len(open)-1], close: true})
			open = open[:len(open)-1]
		}
		for _, m := range want[k:] {
			toks = append(toks, mdToken{mark: m, open: true})
			open = append(open, m)
		}
		switch m := r.Attrs.Mention; {
		case m != nil && m.Name != "" && m.Kind != "page":
			// A page mention is a link to the page, which reads back as
			// one; others are their names.
			toks = append(toks, mdToken{text: "@" + escapeText(m.Name)})
		case r.Attrs.Code:
			// Code spans side by side differ only in what Markdown can't
			// say: they are one span, as backticks touching would not
			// read back.
			if n := len(toks); n > 0 && toks[n-1].code {
				toks[n-1].text += r.Text
			} else {
				toks = append(toks, mdToken{text: r.Text, code: true})
			}
		default:
			toks = append(toks, mdToken{text: escapeText(r.Text)})
		}
	}
	for i := len(open) - 1; i >= 0; i-- {
		toks = append(toks, mdToken{mark: open[i], close: true})
	}
	var b strings.Builder
	for i, t := range toks {
		switch {
		case t.code:
			b.WriteString(codeSpan(t.text))
		case !t.open && !t.close:
			b.WriteString(t.text)
		case t.mark.kind == '[' && t.open:
			b.WriteString("[")
		case t.mark.kind == '[':
			b.WriteString("](" + linkDestination(t.mark.href) + ")")
		case t.mark.kind == '*' && boldTouchesItalic(toks, i):
			// Bold beside an italic's * is __, as *** reads either way.
			b.WriteString("__")
		case t.mark.kind == '*':
			b.WriteString("**")
		case t.mark.kind == 'i':
			b.WriteString("*")
		case t.mark.kind == '~':
			b.WriteString("~~")
		}
	}
	return b.String()
}

// mdMark is a format Markdown writes around text: a link to href, bold
// ('*'), italic ('i') or strike ('~').
type mdMark struct {
	kind byte
	href string
}

// mdToken is a mark opening or closing, or text: written out, or code.
type mdToken struct {
	mark        mdMark
	open, close bool
	text        string
	code        bool
}

// marksOf are the marks of a run, outermost first: a link holds the
// others, as its brackets must.
func marksOf(a Attrs) []mdMark {
	var out []mdMark
	if a.Href != "" {
		out = append(out, mdMark{kind: '[', href: a.Href})
	}
	if a.Bold {
		out = append(out, mdMark{kind: '*'})
	}
	if a.Italic {
		out = append(out, mdMark{kind: 'i'})
	}
	if a.Strike {
		out = append(out, mdMark{kind: '~'})
	}
	return out
}

// boldTouchesItalic reports whether the bold mark toks[i] opens or closes
// has an italic mark next to either of its ends.
func boldTouchesItalic(toks []mdToken, i int) bool {
	at := func(j int) bool {
		return j >= 0 && j < len(toks) && (toks[j].open || toks[j].close) && toks[j].mark.kind == 'i'
	}
	// The other end: bold doesn't nest in bold, so it is the next bold
	// mark after an open, or before a close.
	step := 1
	if toks[i].close {
		step = -1
	}
	j := i + step
	for toks[j].mark.kind != '*' || !toks[j].open && !toks[j].close {
		j += step
	}
	return at(i-1) || at(i+1) || at(j-1) || at(j+1)
}

// trimSpans takes bold, italic and strike off the spaces at either end of
// the text they span, outside code: markers must touch text to read as
// markers.
func trimSpans(d Delta) Delta {
	type char struct {
		r   rune
		a   Attrs
		run int
	}
	var cs []char
	for i, r := range d {
		for _, c := range r.Text {
			cs = append(cs, char{c, r.Attrs, i})
		}
	}
	marks := []func(*Attrs) *bool{
		func(a *Attrs) *bool { return &a.Bold },
		func(a *Attrs) *bool { return &a.Italic },
		func(a *Attrs) *bool { return &a.Strike },
	}
	space := func(i int) bool { return !cs[i].a.Code && unicode.IsSpace(cs[i].r) }
	for _, mark := range marks {
		for i := 0; i < len(cs); {
			if !*mark(&cs[i].a) {
				i++
				continue
			}
			j := i
			for j < len(cs) && *mark(&cs[j].a) {
				j++
			}
			lo, hi := i, j
			for lo < hi && space(lo) {
				*mark(&cs[lo].a) = false
				lo++
			}
			for hi > lo && space(hi-1) {
				*mark(&cs[hi-1].a) = false
				hi--
			}
			i = j
		}
	}
	var out Delta
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && cs[j].run == cs[i].run && cs[j].a.Equal(cs[i].a) {
			j++
		}
		text := make([]rune, 0, j-i)
		for _, c := range cs[i:j] {
			text = append(text, c.r)
		}
		out = append(out, Run{Text: string(text), Attrs: cs[i].a})
		i = j
	}
	return out.Compact()
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
		} else if s[i] == '`' {
			// A ] in code is the code's.
			if _, n := codeSpanAt(s[i:]); n > 0 {
				i += n - 1
			}
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
		// ***both*** is bold and italic, as agents write it.
		if i+2 < len(s) && (c == '*' || c == '_') && s[i+1] == c && s[i+2] == c {
			marker := string([]rune{c, c, c})
			if end := indexString(s, marker, i+3); end > i+3 {
				flush()
				a := attrs
				a.Bold, a.Italic = true, true
				parseInline(s[i+3:end], a, out)
				i = end + 3
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

// skipSpan returns where a code span or a link at s[j] ends, or j when
// none starts there: they hold their text, markers and all, as the
// markers around them look for their ends.
func skipSpan(s []rune, j int) int {
	switch s[j] {
	case '`':
		if _, n := codeSpanAt(s[j:]); n > 0 {
			return j + n
		}
	case '[':
		if _, _, n, ok := link(s[j:]); ok {
			return j + n
		}
	}
	return j
}

func findSingle(s []rune, marker rune, from int) int {
	for j := from; j < len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if k := skipSpan(s, j); k > j {
			j = k - 1
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
		if k := skipSpan(s, j); k > j {
			j = k - 1
			continue
		}
		if string(s[j:j+len(m)]) == sub {
			return j
		}
	}
	return -1
}
