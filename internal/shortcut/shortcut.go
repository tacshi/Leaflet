package shortcut

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"

	"leaflet/internal/doc"
)

var blocks = map[string]struct {
	typ     string
	level   int
	checked bool
}{
	"#":   {typ: "heading", level: 1},
	"##":  {typ: "heading", level: 2},
	"###": {typ: "heading", level: 3},
	"-":   {typ: "bulleted_list"},
	"*":   {typ: "bulleted_list"},
	"1.":  {typ: "numbered_list"},
	"[]":  {typ: "todo_list"},
	"[ ]": {typ: "todo_list"},
	"[x]": {typ: "todo_list", checked: true},
	"[X]": {typ: "todo_list", checked: true},
	">":   {typ: "quote"},
	"```": {typ: "code"},
}

func Block(typ, prefix string) (newType string, data map[string]any, ok bool) {
	p, spaced := strings.CutSuffix(width.Fold.String(prefix), " ")
	b, found := blocks[p]
	if !spaced || !found || typ != "paragraph" && (typ != "bulleted_list" || b.typ != "todo_list") {
		return "", nil, false
	}
	data = doc.TypeData(b.typ, b.level)
	if b.typ == "todo_list" {
		data["checked"] = b.checked
	}
	return b.typ, data, true
}

func Divider(typ, text string) bool { return typ == "paragraph" && width.Fold.String(text) == "---" }

func OpensMenu(r rune) bool {
	return unicode.IsSpace(r) || unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		r > unicode.MaxASCII && unicode.IsPunct(r)
}

func MenuKind(r rune) rune {
	switch r {
	case '/', '／', '、':
		return '/'
	case '@', '＠':
		return '@'
	}
	return 0
}

func Menu(r, before rune) rune {
	kind := MenuKind(r)
	switch {
	case kind == 0:
		return 0
	case r == '、':
		if before != 0 && !unicode.IsSpace(before) {
			return 0
		}
	case before != 0 && !OpensMenu(before):
		return 0
	}
	return kind
}

type Match struct {
	Open, Inner, Close, End int
	Format                  string
}

var markers = []struct{ marker, format string }{
	{"**", "bold"}, {"~~", "strike"}, {"`", "code"}, {"*", "italic"}, {"$", "math"},
}

func Inline[T rune | uint16](text []T, end int, mentioned func(from, to int) bool) (Match, bool) {
	if end < 2 || end > len(text) {
		return Match{}, false
	}
	is := func(at int, marker string) bool {
		for i := range len(marker) {
			if text[at+i] != T(marker[i]) {
				return false
			}
		}
		return true
	}
	for _, mk := range markers {
		m := len(mk.marker)
		if end < 2*m+1 || !is(end-m, mk.marker) || mentioned(end-m, end) {
			continue
		}
		closeAt := end - m
		for start := closeAt - 1; start >= m-1; start-- {
			openAt := start - m + 1
			if !is(openAt, mk.marker) || mentioned(openAt, start+1) {
				continue
			}
			inner := text[start+1 : closeAt]
			if len(inner) == 0 || unicode.IsSpace(rune(inner[0])) || unicode.IsSpace(rune(inner[len(inner)-1])) {
				break
			}
			if m == 1 && openAt > 0 && text[openAt-1] == T(mk.marker[0]) {
				break
			}
			// Two prices are no formula: one starts with no digit.
			if mk.format == "math" && unicode.IsDigit(rune(inner[0])) {
				break
			}
			return Match{Open: openAt, Inner: start + 1, Close: closeAt, End: end, Format: mk.format}, true
		}
	}
	return Match{}, false
}
