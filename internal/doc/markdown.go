package doc

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/markdown/parser"
)

func (d Delta) ToMarkdown() string { return d.markdown(escapeText, escapeText, LinkDestination) }

func (d Delta) markdown(escape, linked, dest func(string) string) string {
	var toks []mdToken
	var open []mdMark
	whole := d.Compact()
	runs := trimSpans(whole)
	// A page mention's link is its own, apart from the same link beside it: of is the run of whole it is cut from.
	of := make([]int, len(runs))
	for i, k, at, end := 0, 0, 0, 0; i < len(runs); i++ {
		for ; k < len(whole) && at >= end; k++ {
			end += len(whole[k].Text)
		}
		if m := runs[i].Attrs; m.Href != "" && m.Mention != nil && m.Mention.Kind == "page" {
			of[i] = k
		}
		at += len(runs[i].Text)
	}
	marks := func(i int) []mdMark {
		out := marksOf(runs[i].Attrs)
		if of[i] != 0 {
			out[0].mention = of[i]
		}
		return out
	}
	for i, r := range runs {
		want := marks(i)
		spans := func(m mdMark) int {
			n := 1
			for n < len(runs)-i && slices.Contains(marks(i+n), m) {
				n++
			}
			return n
		}
		k := 0
		for k < len(open) && slices.Contains(want, open[k]) {
			k++
		}
		// A mark that ends inside a page mention, at the spaces that end it, would cut its link in two: the mark
		// closes before the link, and opens again in it.
		if of[i] != 0 && !slices.Contains(open[:k], want[0]) {
			link := spans(want[0])
			if j := slices.IndexFunc(open[:k], func(m mdMark) bool { return spans(m) < link }); j >= 0 {
				k = j
			}
		}
		for len(open) > k {
			toks = append(toks, mdToken{mark: open[len(open)-1], close: true})
			open = open[:len(open)-1]
		}
		var opening []mdMark
		for _, m := range want {
			if !slices.Contains(open, m) {
				opening = append(opening, m)
			}
		}
		slices.SortStableFunc(opening, func(a, b mdMark) int { return spans(b) - spans(a) })
		for _, m := range opening {
			toks = append(toks, mdToken{mark: m, open: true})
			open = append(open, m)
		}
		switch m := r.Attrs.Mention; {
		case writtenByName(m):
			name := m.Name
			if name == "" {
				name = strings.TrimPrefix(r.Text, "@")
			}
			toks = append(toks, mdToken{text: "[@" + linked(name) + "](" + dest(MentionLink(m.Kind, m.ID)) + ")"})
		case r.Attrs.Math:
			toks = append(toks, mdToken{text: r.Text, math: true})
		case r.Attrs.Code:
			if n := len(toks); n > 0 && toks[n-1].code {
				toks[n-1].text += r.Text
			} else {
				toks = append(toks, mdToken{text: r.Text, code: true})
			}
		case r.Attrs.Href != "":
			toks = append(toks, mdToken{text: linked(r.Text)})
		default:
			toks = append(toks, mdToken{text: escape(r.Text)})
		}
	}
	for i := len(open) - 1; i >= 0; i-- {
		toks = append(toks, mdToken{mark: open[i], close: true})
	}
	toks = spacesOutside(toks)
	var b strings.Builder
	// plain[i] tells that byte i of b is text of the page's, where a dollar
	// sign is one to escape if it would read as the start of a formula.
	var plain []bool
	defer func() { plain = nil }()
	for i, t := range toks {
		before := b.Len()
		if t.math {
			next := ""
			for _, after := range toks[i+1:] {
				if after.text != "" || after.code {
					next = after.text
					break
				}
				if after.open || after.close {
					next = "*"
					break
				}
			}
			b.WriteString(mathSpan(t.text, next))
			plain = append(plain, make([]bool, b.Len()-len(plain))...)
			continue
		}
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
			b.WriteString("](" + dest(t.mark.href) + ")")
		case t.mark.kind == 'c' && t.open:
			b.WriteString(colorOpen + t.mark.href + `">`)
		case t.mark.kind == 'c':
			b.WriteString(colorClose)
		case t.mark.kind == '*' && boldTouchesItalic(toks, i):
			b.WriteString("__")
		case t.mark.kind == '*':
			b.WriteString("**")
		case t.mark.kind == 'i':
			b.WriteString("*")
		case t.mark.kind == '~':
			b.WriteString("~~")
		}
		plain = append(plain[:min(len(plain), before)], make([]bool, b.Len()-min(len(plain), before))...)
		if plainToken(t) {
			for j := before; j < len(plain); j++ {
				plain[j] = true
			}
		}
	}
	return escapeDollars(b.String(), plain)
}

// mathSpan writes a formula as $x^2$, or between backticks as $`x^2`$ where
// the plain form would not read back as it is: next is the text after it.
func mathSpan(tex, next string) string {
	simple := tex != "" && !strings.ContainsAny(tex, "$`\n") && !strings.HasSuffix(tex, `\`) &&
		!unicode.IsSpace([]rune(tex)[0]) && !unicode.IsSpace([]rune(tex)[len([]rune(tex))-1])
	if simple && (next == "" || !unicode.IsDigit([]rune(next)[0])) {
		return "$" + tex + "$"
	}
	return "$" + codeSpan(tex) + "$"
}

// escapeDollars escapes the dollar signs of plain text that would start a
// formula, and no other: a price stays as it is written.
func escapeDollars(s string, plain []bool) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var runes []rune
	var text []bool
	for i, c := range s {
		runes, text = append(runes, c), append(text, i < len(plain) && plain[i])
	}
	// From the end, so that each is read with the ones after it as they
	// will be written: one escaped is none a formula ends at.
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] != '$' || !text[i] {
			continue
		}
		if _, n := mathAt(runes, i); n > 0 {
			runes = slices.Insert(runes, i, '\\')
			text = slices.Insert(text, i, true)
		}
	}
	return string(runes)
}

// mathAt reads the formula the dollar sign at i opens: $x^2$, which starts
// and ends at no space and ends before no digit, so that prices are not
// formulas; $$x^2$$; or $`x^2`$, which holds anything. n is 0 where none
// opens there.
func mathAt(s []rune, i int) (tex string, n int) {
	rest := s[i+1:]
	switch {
	case len(rest) == 0:
		return "", 0
	case rest[0] == '`':
		code, n := codeSpanAt(rest)
		if n == 0 || n >= len(rest) || rest[n] != '$' {
			return "", 0
		}
		return code, n + 2
	case rest[0] == '$':
		for j := 1; j+1 < len(rest); j++ {
			if rest[j] == '\n' {
				break
			}
			if rest[j] == '$' && rest[j+1] == '$' {
				if tex := strings.TrimSpace(string(rest[1:j])); tex != "" && !strings.Contains(tex, "$") {
					return tex, j + 3
				}
				break
			}
		}
		return "", 0
	case unicode.IsSpace(rest[0]):
		return "", 0
	}
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
		case '\n':
			return "", 0
		case '$':
			if j == 0 || unicode.IsSpace(rest[j-1]) || j+1 < len(rest) && unicode.IsDigit(rest[j+1]) {
				return "", 0
			}
			return string(rest[:j]), j + 2
		}
	}
	return "", 0
}

type mdMark struct {
	kind    byte
	href    string
	mention int
}

// writtenByName tells a mention of a person or an agent, written whole as a link of its name: no part of it is cut
// out of the marks around it.
func writtenByName(m *Mention) bool { return m != nil && m.ID != "" && m.Kind != "page" }

type mdToken struct {
	mark        mdMark
	open, close bool
	text        string
	code, math  bool
}

func plainToken(t mdToken) bool { return !t.open && !t.close && !t.code && !t.math }

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

func boldTouchesItalic(toks []mdToken, i int) bool {
	at := func(j int) bool {
		return j >= 0 && j < len(toks) && (toks[j].open || toks[j].close) && toks[j].mark.kind == 'i'
	}
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
	if !slices.ContainsFunc(d, func(r Run) bool { return marksOf(r.Attrs) != 0 }) {
		return d.Compact()
	}
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
		named := writtenByName(r.Attrs.Mention)
		for at, c := range r.Text {
			kept := r.Attrs.Code || r.Attrs.Math
			cs = append(cs, char{int32(i), int32(at), !named && (!kept || c == '\n') && unicode.IsSpace(c), m})
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

const (
	colorOpen  = `<span style="`
	colorClose = `</span>`
)

const maxColorStyle = 64

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

func hasRunes(s []rune, prefix string) bool {
	p := []rune(prefix)
	return len(s) >= len(p) && slices.Equal(s[:len(p)], p)
}

func escapeText(s string) string { return escapeMarkup(s, false, false) }

func escapeCommonMark(s string) string { return escapeMarkup(s, true, true) }

func escapeLinkText(s string) string { return escapeMarkup(s, true, false) }

func escapeMarkup(s string, commonMark, addresses bool) string {
	var runes []rune
	var at []int
	for i, c := range s {
		runes, at = append(runes, c), append(at, i)
	}
	var path []bool
	if addresses {
		path = addressPaths(runes)
	}
	var b strings.Builder
	for i, c := range runes {
		switch c {
		case '~':
			if path == nil || !path[i] || runes[i-1] == '~' || runes[i+1] == '~' {
				b.WriteByte('\\')
			}
		case '\\', '*', '`', '[', ']':
			b.WriteByte('\\')
		case '_':
			if path != nil && path[i] && isWord(runes[i-1]) && runes[i+1] != '_' {
				break
			}
			if i == 0 || i+1 == len(runes) || !isWord(runes[i-1]) || !isWord(runes[i+1]) {
				b.WriteByte('\\')
			}
		case '<':
			if hasRunes(runes[i:], colorOpen) || hasRunes(runes[i:], colorClose) ||
				commonMark && i+1 < len(runes) && (htmlStart(runes[i+1]) || emailAutolink(runes[i+1:])) {
				b.WriteByte('\\')
			}
		case '&':
			if commonMark && startsReference(s[at[i]:]) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

func addressPaths(s []rune) []bool {
	var path []bool
	for i := 0; i < len(s); i++ {
		if i > 0 && !unicode.IsSpace(s[i-1]) && !strings.ContainsRune("(*_~", s[i-1]) {
			continue
		}
		starts := func(prefix string) bool {
			if len(s)-i < len(prefix) {
				return false
			}
			for k, c := range prefix {
				if at := s[i+k]; at != c && at != unicode.ToUpper(c) {
					return false
				}
			}
			return true
		}
		host := i
		switch {
		case starts("http://"):
			host += len("http://")
		case starts("https://"):
			host += len("https://")
		case !starts("www."):
			continue
		}
		j := host
		for j < len(s) && (isWord(s[j]) || s[j] == '.' || s[j] == '-' || s[j] == ':') {
			j++
		}
		if j == host || j == len(s) || !strings.ContainsRune("/?#", s[j]) {
			continue
		}
		end := j
		for end < len(s) && !unicode.IsSpace(s[end]) && s[end] != '<' {
			end++
		}
		if path == nil {
			path = make([]bool, len(s))
		}
		for k := j; k < end-1; k++ {
			path[k] = true
		}
		i = end - 1
	}
	return path
}

func startsReference(s string) bool {
	n, _ := parser.Entity(s)
	return n > 0
}

func htmlStart(c rune) bool {
	return c < 0x80 && (unicode.IsLetter(c) || c == '/' || c == '!' || c == '?')
}

func emailAutolink(s []rune) bool {
	for i, c := range s {
		switch {
		case c == '@':
			return i > 0
		case c >= 0x80 || !unicode.IsLetter(c) && !unicode.IsDigit(c) && !strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", c):
			return false
		}
	}
	return false
}

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
	md := out.Uncolored().commonMark()
	for range 3 {
		again := FromMarkdown(md).commonMark()
		if again == md {
			break
		}
		md = again
	}
	return md
}

func (d Delta) commonMark() string {
	return d.markdown(escapeCommonMark, escapeLinkText, commonMarkDestination)
}

func (d Delta) Uncolored() Delta {
	out := slices.Clone(d)
	for i := range out {
		out[i].Attrs.Color, out[i].Attrs.Background = "", ""
	}
	return out
}

func MentionLink(kind, id string) string {
	if kind != "agent" {
		kind = "user"
	}
	return "mention:" + kind + ":" + id
}

func mentionOf(href string) (kind, id string, ok bool) {
	rest, ok := strings.CutPrefix(href, "mention:")
	if !ok {
		return "", "", false
	}
	kind, id, _ = strings.Cut(rest, ":")
	return kind, id, (kind == "user" || kind == "agent") && id != ""
}

func LinkDestination(href string) string { return linkDestination(href, false) }

func commonMarkDestination(href string) string { return linkDestination(href, true) }

func linkDestination(href string, commonMark bool) string {
	href = strings.ReplaceAll(href, "`", "%60")
	angled := strings.ContainsAny(href, "()<>\\ \t\n")
	if angled {
		href = strings.NewReplacer("\\", "\\\\", "<", "\\<", ">", "\\>", "\n", "%0A").Replace(href)
	}
	if commonMark && strings.Contains(href, "&") {
		var b strings.Builder
		for i := range len(href) {
			if href[i] == '&' && startsReference(href[i:]) {
				b.WriteByte('\\')
			}
			b.WriteByte(href[i])
		}
		href = b.String()
	}
	if angled {
		return "<" + href + ">"
	}
	return href
}

func EscapeBlockStart(md string) string {
	s := strings.TrimLeftFunc(md, unicode.IsSpace)
	lead := md[:len(md)-len(s)]
	hashes := strings.TrimLeft(s, "#")
	digits := strings.TrimLeft(s, "0123456789")
	switch {
	case s == "":
		return md
	case len(hashes) < len(s) && len(s)-len(hashes) <= 6 && (hashes == "" || hashes[0] == ' '),
		s[0] == '>', s[0] == '|', strings.HasPrefix(s, "$$"), strings.HasPrefix(s, "!["), IsRule(s), underline(s),
		tableRule(s),
		s[0] == '<' && len(s) > 1 && htmlStart(rune(s[1])),
		(s[0] == '-' || s[0] == '+') && (len(s) == 1 || s[1] == ' '):
		return lead + "\\" + s
	case len(digits) < len(s) && len(s)-len(digits) <= 9 && digits != "" && (digits[0] == '.' || digits[0] == ')') &&
		(len(digits) == 1 || digits[1] == ' '):
		n := len(s) - len(digits)
		return lead + s[:n] + "\\" + s[n:]
	}
	return md
}

func underline(s string) bool {
	s = strings.TrimRight(s, " \t")
	return s != "" && (strings.Trim(s, "=") == "" || strings.Trim(s, "-") == "")
}

func tableRule(s string) bool {
	return strings.Trim(s, "-:| \t") == "" && strings.Contains(s, "-") && strings.ContainsAny(s, "|:")
}

func headingMarkdown(md string) string {
	s := strings.TrimRight(md, " \t")
	rest := strings.TrimRight(s, "#")
	if rest == s || rest != "" && !strings.HasSuffix(rest, " ") && !strings.HasSuffix(rest, "\t") {
		return md
	}
	return rest + "\\" + md[len(rest):]
}

func FromMarkdown(markdown string) Delta {
	var out Delta
	parseInline([]rune(markdown), Attrs{}, &out, 0)
	return out.Compact()
}

const maxInlineDepth = 32

type linkScan struct {
	s               []rune
	closes, urlEnds []int
	found           map[string]map[int]int
	code            codeSpans[rune]
}

// codeAt reads the code span the backticks at i open, as codeSpanAt does. Where it reads none, the whole run of
// backticks is text.
func (l *linkScan) codeAt(i int) (code string, n int) {
	l.code.s = l.s
	if n = l.code.span(i); n == 0 {
		return "", 0
	}
	open := ticks(l.s[i:])
	return codeText(l.s[i+open : i+n-open]), n
}

func (l *linkScan) scan() {
	s := l.s
	l.closes, l.urlEnds = make([]int, len(s)), make([]int, len(s))
	at := func(m []int, q int) int {
		if q < len(s) {
			return m[q]
		}
		return -1
	}
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
			next := runEnd
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
			code, n := links.codeAt(i)
			if n == 0 {
				n = ticks(s[i:])
				plain.WriteString(string(s[i : i+n]))
				i += n
				continue
			}
			flush()
			a := attrs
			a.Code = true
			*out = append(*out, Run{Text: code, Attrs: a})
			i += n
			continue
		}
		if c == '$' {
			if tex, n := mathAt(s, i); n > 0 {
				flush()
				a := attrs
				a.Math = true
				*out = append(*out, Run{Text: tex, Attrs: a})
				i += n
				continue
			}
		}
		if c == '[' {
			if text, href, n, ok := links.link(i); ok {
				flush()
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
		a.Strike, a.Code, a.Math = a.Strike && b.Strike, a.Code && b.Code, a.Math && b.Math
		if a.Color != b.Color {
			a.Color = ""
		}
		if a.Background != b.Background {
			a.Background = ""
		}
	}
	return a
}

// codeSpans finds the code spans of a text, each in no more time than its own length takes: one pass finds, for
// the backticks at every place, the next run of as many, where their span ends.
type codeSpans[T byte | rune] struct {
	s    []T
	ends []int
}

// span is how long the code span that the backticks at i open is, 0 where no run of as many closes it.
func (c *codeSpans[T]) span(i int) int {
	s := c.s
	if c.ends == nil {
		c.ends = make([]int, len(s))
		runs := map[int]int{}
		runEnd := len(s)
		for q := len(s) - 1; q >= 0; q-- {
			if s[q] != '`' {
				continue
			}
			if q+1 == len(s) || s[q+1] != '`' {
				runEnd = q + 1
			}
			if start, ok := runs[runEnd-q]; ok {
				c.ends[q] = start + runEnd - q
			}
			if q == 0 || s[q-1] != '`' {
				runs[runEnd-q] = q
			}
		}
	}
	return max(c.ends[i]-i, 0)
}

func ticks[T byte | rune](s []T) int {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	return n
}

func codeText(s []rune) string {
	code := string(s)
	if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
		code = code[1 : len(code)-1]
	}
	return code
}

func codeSpanAt(s []rune) (string, int) {
	open := ticks(s)
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
			code := codeText(s[open:j])
			if code == "" {
				return "", 0
			}
			return code, end
		}
		j = end
	}
	return "", 0
}

func skipSpan(links *linkScan, j int) int {
	s := links.s
	switch s[j] {
	case '`':
		if _, n := links.codeAt(j); n > 0 {
			return j + n
		}
		return j + ticks(s[j:])
	case '[':
		if _, _, n, ok := links.link(j); ok {
			return j + n
		}
	}
	return j
}

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
	return links.first(sub, from, len(s)-len(m)+1, next, func(j int) bool { return string(s[j:j+len(m)]) == sub })
}
