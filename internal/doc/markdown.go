package doc

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ToMarkdown writes rich text in the small inline markdown dialect agents
// read and write: **bold** or __bold__, *italic* or _italic_, `code`,
// ~~strike~~, <span style="color:#ef4444ff;background:#eab30840">text color
// and highlight</span>, and [text](url); a page mention is
// [Title](leaflet://page/<id>), and a person or an agent
// [@Name](mention:user:<id>) or mention:agent:<id>.
// Text that would read as markup is escaped with backslashes, so it reads
// back as written. A format over several runs is
// one span, the others nested in it, so runs side by side read back as
// they were.
func (d Delta) ToMarkdown() string {
	var toks []mdToken
	var open []mdMark
	runs := trimSpans(d.Compact())
	for i, r := range runs {
		want := marksOf(r.Attrs)
		// The marks open that this run still has stay open, as far as
		// the first it hasn't: closing one to open it again inside
		// another would put its marker after a space, as in "*the *[",
		// where it no longer reads as one.
		k := 0
		for k < len(open) && slices.Contains(want, open[k]) {
			k++
		}
		for len(open) > k {
			toks = append(toks, mdToken{mark: open[len(open)-1], close: true})
			open = open[:len(open)-1]
		}
		// Of the marks to open, the one that spans the most runs goes
		// outermost, so it closes last; in marksOf's order otherwise.
		var opening []mdMark
		for _, m := range want {
			if !slices.Contains(open, m) {
				opening = append(opening, m)
			}
		}
		spans := func(m mdMark) int {
			n := 1
			for n < len(runs)-i && slices.Contains(marksOf(runs[i+n].Attrs), m) {
				n++
			}
			return n
		}
		slices.SortStableFunc(opening, func(a, b mdMark) int { return spans(b) - spans(a) })
		for _, m := range opening {
			toks = append(toks, mdToken{mark: m, open: true})
			open = append(open, m)
		}
		switch m := r.Attrs.Mention; {
		case m != nil && m.ID != "" && m.Kind != "page":
			// A page mention is a link to the page, which reads back as
			// one; a person or an agent is a link to mention:<kind>:<id>,
			// as the server's agents write it, so it reads back as the
			// mention and not as text.
			name := m.Name
			if name == "" {
				name = strings.TrimPrefix(r.Text, "@")
			}
			toks = append(toks, mdToken{text: "[@" + escapeText(name) + "](" + LinkDestination(MentionLink(m.Kind, m.ID)) + ")"})
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
	toks = spacesOutside(toks)
	var b strings.Builder
	for i, t := range toks {
		// A ! before a link would make it a picture: it is escaped.
		if t.open && t.mark.kind == '[' || plainToken(t) && strings.HasPrefix(t.text, "[") {
			if out := b.String(); strings.HasSuffix(out, "!") {
				b.Reset()
				b.WriteString(out[:len(out)-1] + `\!`)
			}
		}
		switch {
		case t.code:
			b.WriteString(codeSpan(t.text))
		case !t.open && !t.close:
			b.WriteString(t.text)
		case t.mark.kind == '[' && t.open:
			b.WriteString("[")
		case t.mark.kind == '[':
			b.WriteString("](" + LinkDestination(t.mark.href) + ")")
		case t.mark.kind == 'c' && t.open:
			b.WriteString(colorOpen + t.mark.href + `">`)
		case t.mark.kind == 'c':
			b.WriteString(colorClose)
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
// ('*'), italic ('i'), strike ('~') or a color ('c'), href its style.
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

// plainToken reports whether a token is text written out as it is.
func plainToken(t mdToken) bool { return !t.open && !t.close && !t.code }

// marksOf are the marks of a run, outermost first when they open
// together: a link holds the others, and a color the formats.
func marksOf(a Attrs) []mdMark {
	var out []mdMark
	if a.Href != "" {
		out = append(out, mdMark{kind: '[', href: a.Href})
	}
	if style := colorStyle(a); style != "" {
		out = append(out, mdMark{kind: 'c', href: style})
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

// spacesOutside moves spaces off the inner side of bold, italic and strike
// markers, where a marker no longer reads as one: those before a closing
// marker go after it, those after an opening one before it. trimSpans
// keeps a format's own ends clear, but a format closed at a link's end
// and opened again past it, as in "[read *this *](u)*and that*", gets new
// ends. The spaces lose the format there, which they can't show; they
// never cross a link's brackets or a color's tags, so a link keeps its
// text and a color its spaces.
func spacesOutside(toks []mdToken) []mdToken {
	plain := plainToken
	emphasis := func(t mdToken) bool { return (t.open || t.close) && t.mark.kind != '[' && t.mark.kind != 'c' }
	var out []mdToken
	for i := 0; i < len(toks); {
		if !toks[i].open && !toks[i].close {
			out = append(out, toks[i])
			i++
			continue
		}
		// The markers here, and the spaces among them, each at the
		// number of markers before it.
		type space struct {
			text string
			at   int
		}
		var marks []mdToken
		var spaces []space
		j := i
		for ; j < len(toks); j++ {
			if t := toks[j]; t.open || t.close {
				marks = append(marks, t)
			} else if plain(t) && strings.TrimSpace(t.text) == "" {
				spaces = append(spaces, space{t.text, len(marks)})
			} else {
				break
			}
		}
		if n := len(out); n > 0 && plain(out[n-1]) {
			t := strings.TrimRightFunc(out[n-1].text, unicode.IsSpace)
			spaces = append([]space{{out[n-1].text[len(t):], 0}}, spaces...)
			out[n-1].text = t
		}
		if j < len(toks) && plain(toks[j]) {
			t := strings.TrimLeftFunc(toks[j].text, unicode.IsSpace)
			spaces = append(spaces, space{toks[j].text[:len(toks[j].text)-len(t)], len(marks)})
			toks[j].text = t
		}
		for k := range spaces {
			at := &spaces[k].at
			for *at < len(marks) && emphasis(marks[*at]) && marks[*at].close {
				*at++
			}
			for *at > 0 && emphasis(marks[*at-1]) && marks[*at-1].open {
				*at--
			}
		}
		var seq []mdToken
		for k := 0; k <= len(marks); k++ {
			for _, s := range spaces {
				if s.at == k && s.text != "" {
					seq = append(seq, mdToken{text: s.text})
				}
			}
			if k < len(marks) {
				seq = append(seq, marks[k])
			}
		}
		// With the spaces out, a format can open and close on nothing,
		// or close and open again on the spot: it needs neither.
		for k := 0; k+1 < len(seq); {
			a, b := seq[k], seq[k+1]
			if (a.open || a.close) && (b.open || b.close) && a.mark == b.mark && a.open != b.open {
				seq = slices.Delete(seq, k, k+2)
				k = max(k-1, 0)
				continue
			}
			k++
		}
		out = append(out, seq...)
		i = j
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
// the text they span, outside code, and off line breaks in code too:
// markers must touch text to read as markers, and a line can't start with
// one that closes, where it can read as a list item's.
func trimSpans(d Delta) Delta {
	const bold, italic, strike = 1, 2, 4
	marksOf := func(a Attrs) (m uint8) {
		if a.Bold {
			m |= bold
		}
		if a.Italic {
			m |= italic
		}
		if a.Strike {
			m |= strike
		}
		return m
	}
	// Most text has none of the three, and nothing to trim.
	if !slices.ContainsFunc(d, func(r Run) bool { return marksOf(r.Attrs) != 0 }) {
		return d.Compact()
	}
	// A character as trimming sees it: its run and where in the run's text
	// it starts, whether a mark comes off it at a span's end, and the
	// marks it keeps; its other formats are its run's.
	type char struct {
		run, at int32
		space   bool
		marks   uint8
	}
	n := 0
	for _, r := range d {
		n += utf8.RuneCountInString(r.Text)
	}
	cs := make([]char, 0, n)
	for i, r := range d {
		m := marksOf(r.Attrs)
		for at, c := range r.Text {
			cs = append(cs, char{int32(i), int32(at), (!r.Attrs.Code || c == '\n') && unicode.IsSpace(c), m})
		}
	}
	for _, mark := range []uint8{bold, italic, strike} {
		for i := 0; i < len(cs); {
			if cs[i].marks&mark == 0 {
				i++
				continue
			}
			j := i
			for j < len(cs) && cs[j].marks&mark != 0 {
				j++
			}
			lo, hi := i, j
			for lo < hi && cs[lo].space {
				cs[lo].marks &^= mark
				lo++
			}
			for hi > lo && cs[hi-1].space {
				cs[hi-1].marks &^= mark
				hi--
			}
			i = j
		}
	}
	out := make(Delta, 0, len(d))
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && cs[j].run == cs[i].run && cs[j].marks == cs[i].marks {
			j++
		}
		r := d[cs[i].run]
		end := len(r.Text)
		if j < len(cs) && cs[j].run == cs[i].run {
			end = int(cs[j].at)
		}
		a := r.Attrs
		a.Bold, a.Italic, a.Strike = cs[i].marks&bold != 0, cs[i].marks&italic != 0, cs[i].marks&strike != 0
		out = append(out, Run{Text: r.Text[cs[i].at:end], Attrs: a})
		i = j
	}
	return out.Compact()
}

// The tags a color is written between (ToMarkdown).
const (
	colorOpen  = `<span style="`
	colorClose = `</span>`
)

// maxColorStyle is the longest style a color's tag is read with: a text of
// tags that end nowhere is not looked through to its end from each.
const maxColorStyle = 64

// colorStyle is how a run's color and highlight are written in a tag, ""
// without them.
func colorStyle(a Attrs) string {
	var parts []string
	if ValidColor(a.Color) {
		parts = append(parts, "color:"+a.Color)
	}
	if ValidColor(a.Background) {
		parts = append(parts, "background:"+a.Background)
	}
	return strings.Join(parts, ";")
}

// colorTagAt reads a color's opening tag at the start of s: the formats it
// sets on attrs, and how many runes it takes; 0 when there is none, as a
// style of anything but hex colors, which reads as the text it is.
func colorTagAt(s []rune, attrs Attrs) (Attrs, int) {
	if !hasRunes(s, colorOpen) {
		return attrs, 0
	}
	start := len(colorOpen)
	rest := s[start:min(len(s), start+maxColorStyle)]
	end := slices.Index(rest, '"')
	if end < 0 || !hasRunes(s[start+end:], `">`) {
		return attrs, 0
	}
	found := false
	for _, part := range strings.Split(string(rest[:end]), ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		name, value, _ := strings.Cut(part, ":")
		value = strings.TrimSpace(value)
		if !ValidColor(value) {
			return attrs, 0
		}
		switch strings.TrimSpace(name) {
		case "color":
			attrs.Color = value
		case "background", "background-color":
			attrs.Background = value
		default:
			return attrs, 0
		}
		found = true
	}
	if !found {
		return attrs, 0
	}
	return attrs, start + end + 2
}

// hasRunes reports whether s starts with prefix.
func hasRunes(s []rune, prefix string) bool {
	p := []rune(prefix)
	return len(s) >= len(p) && slices.Equal(s[:len(p)], p)
}

// escapeText escapes the characters that would read as markup: all the
// markers but a _ inside a word, which can't start or end italics, and the
// < of a color's tags.
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
		case '<':
			if hasRunes(runes[i:], colorOpen) || hasRunes(runes[i:], colorClose) {
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

// ReadableMarkdown is ToMarkdown for people to read, in an export or a
// copy for another app: a person or an agent mentioned is the name the
// page shows, as mention links mean something only to Leaflet's agents,
// and colors stay out, as Markdown has none.
func (d Delta) ReadableMarkdown() string {
	out := make(Delta, len(d))
	for i, r := range d {
		if m := r.Attrs.Mention; m != nil && m.Kind != "page" {
			if m.Name != "" {
				r.Text = "@" + m.Name
			}
			r.Attrs.Mention = nil
		}
		out[i] = r
	}
	// Text can read back other than it was where Markdown can't say it, as
	// a format over a space between runs of another: what it reads back as
	// is written instead, so the same Markdown comes out each time.
	md := out.Uncolored().ToMarkdown()
	for range 3 {
		again := FromMarkdown(md).ToMarkdown()
		if again == md {
			break
		}
		md = again
	}
	return md
}

// Uncolored is d without its colors and highlights.
func (d Delta) Uncolored() Delta {
	out := slices.Clone(d)
	for i := range out {
		out[i].Attrs.Color, out[i].Attrs.Background = "", ""
	}
	return out
}

// MentionLink is how inline markdown writes a mention of a person or an
// agent: [@Name](mention:user:<id>), or mention:agent:<id> for an agent.
func MentionLink(kind, id string) string {
	if kind != "agent" {
		kind = "user"
	}
	return "mention:" + kind + ":" + id
}

// mentionOf reads a mention link: the kind of what it mentions and its ID.
func mentionOf(href string) (kind, id string, ok bool) {
	rest, ok := strings.CutPrefix(href, "mention:")
	if !ok {
		return "", "", false
	}
	kind, id, _ = strings.Cut(rest, ":")
	return kind, id, (kind == "user" || kind == "agent") && id != ""
}

// LinkDestination writes a link's url, in angle brackets when it holds
// what would end it or read as markup: parentheses, spaces, backslashes.
// A backtick is written as a url can hold one: what looks for code spans
// around a link, in a table's row or a toggle's summary, reads Markdown
// before it reads links.
func LinkDestination(href string) string {
	href = strings.ReplaceAll(href, "`", "%60")
	if !strings.ContainsAny(href, "()<>\\ \t\n") {
		return href
	}
	r := strings.NewReplacer("\\", "\\\\", "<", "\\<", ">", "\\>", "\n", "%0A")
	return "<" + r.Replace(href) + ">"
}

// EscapeBlockStart escapes what at the start of a block's text would
// read as another kind of block: a heading's #, a list item's marker, a
// quote's >, a table's |, a math block's $$, a rule, a toggle's
// <details> or <summary>, or the ! that would make a link a picture.
// They are looked for past spaces of any kind, as a no-break or an
// ideographic space, which ParseLines passes over before a $$ or a
// </details>.
func EscapeBlockStart(md string) string {
	s := strings.TrimLeftFunc(md, unicode.IsSpace)
	lead := md[:len(md)-len(s)]
	hashes := strings.TrimLeft(s, "#")
	digits := strings.TrimLeft(s, "0123456789")
	switch {
	case s == "":
		return md
	case len(hashes) < len(s) && len(s)-len(hashes) <= 6 && (hashes == "" || hashes[0] == ' '),
		s[0] == '>', s[0] == '|', strings.HasPrefix(s, "$$"), strings.HasPrefix(s, "!["), IsRule(s), toggleTag(s),
		(s[0] == '-' || s[0] == '+') && (len(s) == 1 || s[1] == ' '):
		return lead + "\\" + s
	case len(digits) < len(s) && len(s)-len(digits) <= 9 && digits != "" && (digits[0] == '.' || digits[0] == ')') &&
		(len(digits) == 1 || digits[1] == ' '):
		n := len(s) - len(digits)
		return lead + s[:n] + "\\" + s[n:]
	}
	return md
}

// toggleTag reports whether s starts with a tag ParseLines reads a toggle
// by.
func toggleTag(s string) bool {
	for _, tag := range []string{"<details", "</details", "<summary"} {
		if _, ok := cutFold(s, tag); ok {
			return true
		}
	}
	return false
}

// FromMarkdown reads the inline markdown dialect.
func FromMarkdown(markdown string) Delta {
	var out Delta
	parseInline([]rune(markdown), Attrs{}, &out, 0)
	return out.Compact()
}

// maxInlineDepth is how deep formats and links nest: past it, text is read
// as it is written. Each level reads its text again.
const maxInlineDepth = 32

// linkScan reads the links of a text, s, at its [s. Where the ] that ends
// a link's text is from each place, and the ) that ends its url, are
// worked out once for the whole text, from its end, as it is first asked
// for a link: scanned for from each [ instead, a text of many [s closed by
// none would be read as many times.
type linkScan struct {
	s []rune
	// closes is, for each place, where the first ] at its depth is from
	// there, a [ and its ] in between, an escaped one or one in a code span
	// not counting, or -1; urlEnds is, for each place in a url, where the
	// first ) at its depth is, a ( and its ) in between, an escaped one not
	// counting, or -1 past a space.
	closes, urlEnds []int
	// found is, of each marker, where it is first found from each place
	// passed (first).
	found map[string]map[int]int
}

// scan works out closes and urlEnds, each place from the places after it.
func (l *linkScan) scan() {
	s := l.s
	l.closes, l.urlEnds = make([]int, len(s)), make([]int, len(s))
	at := func(m []int, q int) int {
		if q < len(s) {
			return m[q]
		}
		return -1
	}
	// A code span from a backtick goes to the first run after its own of
	// as many backticks as there are from it to its run's end, as
	// codeSpanAt finds it: runs is where the nearest run of each length
	// after the place the pass is at starts, and runEnd where the
	// backticks from that place end.
	runs := map[int]int{}
	runEnd := len(s)
	for q := len(s) - 1; q >= 0; q-- {
		switch s[q] {
		case '\\':
			l.closes[q] = at(l.closes, q+2)
		case '`':
			if q+1 == len(s) || s[q+1] != '`' {
				runEnd = q + 1
			}
			// A ] in code is the code's.
			next := q + 1
			if start, ok := runs[runEnd-q]; ok {
				next = start + runEnd - q
			}
			l.closes[q] = at(l.closes, next)
			if q == 0 || s[q-1] != '`' {
				runs[runEnd-q] = q
			}
		case '[':
			l.closes[q] = -1
			if inner := at(l.closes, q+1); inner >= 0 {
				l.closes[q] = at(l.closes, inner+1)
			}
		case ']':
			l.closes[q] = q
		default:
			l.closes[q] = at(l.closes, q+1)
		}
		switch c := s[q]; {
		case unicode.IsSpace(c):
			l.urlEnds[q] = -1
		case c == '\\' && q+1 < len(s) && isPunct(s[q+1]):
			l.urlEnds[q] = at(l.urlEnds, q+2)
		case c == '(':
			l.urlEnds[q] = -1
			if inner := at(l.urlEnds, q+1); inner >= 0 {
				l.urlEnds[q] = at(l.urlEnds, inner+1)
			}
		case c == ')':
			l.urlEnds[q] = q
		default:
			l.urlEnds[q] = at(l.urlEnds, q+1)
		}
	}
}

// link reads [text](url) at s[i]: the text, the url and how many runes
// they take. As in CommonMark, the url may be in angle brackets or hold
// balanced parentheses, and backslashes escape in both.
func (l *linkScan) link(i int) (text []rune, href string, n int, ok bool) {
	s := l.s
	if l.closes == nil {
		l.scan()
	}
	end := -1
	if i+1 < len(s) {
		end = l.closes[i+1]
	}
	if end <= i+1 || end+1 >= len(s) || s[end+1] != '(' {
		return nil, "", 0, false
	}
	text = s[i+1 : end]
	var b strings.Builder
	j := end + 2
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
		n = j + 2 - i
	} else {
		if j >= len(s) || l.urlEnds[j] < 0 {
			return nil, "", 0, false
		}
		for k := l.urlEnds[j]; j < k; j++ {
			if s[j] == '\\' && j+1 < len(s) && isPunct(s[j+1]) {
				j++
			}
			b.WriteRune(s[j])
		}
		n = j + 1 - i
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

func parseInline(s []rune, attrs Attrs, out *Delta, depth int) {
	if depth > maxInlineDepth {
		*out = append(*out, Run{Text: string(s), Attrs: attrs})
		return
	}
	links := &linkScan{s: s}
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
			if text, href, n, ok := links.link(i); ok {
				flush()
				// A link to a page is a mention of it, in the formats
				// around the link and those all its text has, as ToMarkdown
				// writes a mention's own; one to a block of a page stays a
				// link, with its own text.
				if id, ok := PageLinkID(href); ok && LinkBlock(href) == "" {
					var inner Delta
					parseInline(text, attrs, &inner, depth+1)
					title := inner.Plain()
					a, m := sharedAttrs(inner, attrs), PageMention(id, title)
					a.Href, a.Mention = m.Href, m.Mention
					*out = append(*out, Run{Text: title, Attrs: a})
					i += n
					continue
				}
				// A link to mention:user:<id> or mention:agent:<id> is a
				// mention of that person or agent, by the name it shows.
				if kind, id, ok := mentionOf(href); ok {
					var inner Delta
					parseInline(text, Attrs{}, &inner, depth+1)
					name := strings.TrimPrefix(inner.Plain(), "@")
					a := attrs
					a.Mention = &Mention{Kind: kind, ID: id, Name: name}
					*out = append(*out, Run{Text: "@" + name, Attrs: a})
					i += n
					continue
				}
				a := attrs
				a.Href = href
				parseInline(text, a, out, depth+1)
				i += n
				continue
			}
		}
		if c == '<' {
			if a, n := colorTagAt(s[i:], attrs); n > 0 {
				if end := indexString(links, colorClose, i+n); end > i+n {
					flush()
					parseInline(s[i+n:end], a, out, depth+1)
					i = end + len(colorClose)
					continue
				}
			}
		}
		// ***both*** is bold and italic, as agents write it.
		if i+2 < len(s) && (c == '*' || c == '_') && s[i+1] == c && s[i+2] == c {
			marker := string([]rune{c, c, c})
			if end := indexString(links, marker, i+3); end > i+3 {
				flush()
				a := attrs
				a.Bold, a.Italic = true, true
				parseInline(s[i+3:end], a, out, depth+1)
				i = end + 3
				continue
			}
		}
		if i+1 < len(s) && (c == '*' || c == '~' || c == '_') && s[i+1] == c {
			marker := string([]rune{c, c})
			if end := indexString(links, marker, i+2); end > i+2 {
				flush()
				a := attrs
				if c == '~' {
					a.Strike = true
				} else {
					a.Bold = true
				}
				parseInline(s[i+2:end], a, out, depth+1)
				i = end + 2
				continue
			}
		}
		if c == '*' || c == '_' {
			prevIsWord := i > 0 && isWord(s[i-1])
			if c != '_' || !prevIsWord {
				if end := findSingle(links, c, i+1); end > i+1 {
					flush()
					a := attrs
					a.Italic = true
					parseInline(s[i+1:end], a, out, depth+1)
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

// sharedAttrs are the formats every run of d has, or attrs when it has
// none. A run only of spaces counts only when all are: ToMarkdown takes
// bold, italic and strike off the spaces at a format's ends.
func sharedAttrs(d Delta, attrs Attrs) Attrs {
	if text := slices.DeleteFunc(slices.Clone(d), func(r Run) bool { return strings.TrimSpace(r.Text) == "" }); len(text) > 0 {
		d = text
	}
	if len(d) == 0 {
		return attrs
	}
	a := d[0].Attrs
	for _, r := range d[1:] {
		b := r.Attrs
		a.Bold, a.Italic, a.Under = a.Bold && b.Bold, a.Italic && b.Italic, a.Under && b.Under
		a.Strike, a.Code = a.Strike && b.Strike, a.Code && b.Code
		if a.Color != b.Color {
			a.Color = ""
		}
		if a.Background != b.Background {
			a.Background = ""
		}
	}
	return a
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

// skipSpan returns where a code span or a link at s[j] ends, of the text
// links reads, or j when none starts there: they hold their text, markers
// and all, as the markers around them look for their ends.
func skipSpan(links *linkScan, j int) int {
	s := links.s
	switch s[j] {
	case '`':
		if _, n := codeSpanAt(s[j:]); n > 0 {
			return j + n
		}
	case '[':
		if _, _, n, ok := links.link(j); ok {
			return j + n
		}
	}
	return j
}

// first is where scanning from p for a marker, key, first finds one, a
// place at a time as next has it, or -1: each place passed is told what
// was found, so no place is scanned from twice.
func (l *linkScan) first(key string, p, end int, next func(int) int, takes func(int) bool) int {
	if l.found == nil {
		l.found = map[string]map[int]int{}
	}
	known := l.found[key]
	if known == nil {
		known = map[int]int{}
		l.found[key] = known
	}
	var passed []int
	at := -1
	for p < end {
		if k, ok := known[p]; ok {
			at = k
			break
		}
		if takes(p) {
			at = p
			break
		}
		passed = append(passed, p)
		p = next(p)
	}
	for _, q := range passed {
		known[q] = at
	}
	return at
}

// findSingle is where a single * or _ closes from from: one not doubled,
// not before a word for _, nor after a space but right at from, outside
// code spans and links.
func findSingle(links *linkScan, marker rune, from int) int {
	s := links.s
	next := func(j int) int {
		if s[j] == '\\' {
			return j + 2
		}
		if k := skipSpan(links, j); k > j {
			return k
		}
		if s[j] == marker && j+1 < len(s) && s[j+1] == marker {
			return j + 2
		}
		return j + 1
	}
	closes := func(j int) bool {
		if s[j] != marker {
			return false
		}
		if j+1 == len(s) {
			return true
		}
		return s[j+1] != marker && (marker != '_' || !isWord(s[j+1]))
	}
	if from >= len(s) {
		return -1
	}
	// Right at from, a space before it does not count.
	if closes(from) {
		return from
	}
	return links.first(string(marker)+"1", next(from), len(s), next, func(j int) bool { return closes(j) && s[j-1] != ' ' })
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

func indexString(links *linkScan, sub string, from int) int {
	s := links.s
	m := []rune(sub)
	next := func(j int) int {
		if s[j] == '\\' {
			return j + 2
		}
		if k := skipSpan(links, j); k > j {
			return k
		}
		return j + 1
	}
	// A marker's characters are never a backslash, nor what opens a span.
	return links.first(sub, from, len(s)-len(m)+1, next, func(j int) bool { return string(s[j:j+len(m)]) == sub })
}
