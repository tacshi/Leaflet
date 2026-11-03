// Package shortcut is what Markdown typed in a page becomes, for the
// desktop's editor and the iOS app's alike: "# ", "- ", "[] " and the
// like at a block's start, "---" alone, and **bold**, *italic*, `code`
// and ~~strike~~ typed in full. Each editor makes the change in its own
// text; the rules are here.
package shortcut

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"

	"leaflet/internal/doc"
)

// blocks are the prefixes a space after makes a block of: its type, and
// whether a to-do is checked.
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

// Block is the kind of block a block of type typ becomes as a space is
// typed after prefix, all of its text before the caret, the space
// included: in full width too, the prefix and the space, as an input
// method for Chinese or Japanese types them ("＃　", "１．　"). A
// paragraph becomes any kind; a bullet, a to-do. ok is false for text
// that is no shortcut.
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

// Divider reports whether a block of type typ whose text is text, as its
// last "-" is typed at the end, becomes a divider: a paragraph of "---",
// or of full-width "－－－".
func Divider(typ, text string) bool { return typ == "paragraph" && width.Fold.String(text) == "---" }

// OpensMenu reports whether a "/" or "@" typed after r opens its menu:
// after a space, or after Chinese or Japanese text or punctuation, which
// has no spaces between words; not inside a word, as in a@b.com or a/b.
func OpensMenu(r rune) bool {
	return unicode.IsSpace(r) || unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		r > unicode.MaxASCII && unicode.IsPunct(r)
}

// MenuKind is the menu a character stands for, '/' or '@', or 0 for
// none: "/" and "@", their full-width forms "／" and "＠", which input
// methods for Chinese and Japanese type, and "、", which a Chinese one
// types for "/".
func MenuKind(r rune) rune {
	switch r {
	case '/', '／', '、':
		return '/'
	case '@', '＠':
		return '@'
	}
	return 0
}

// Menu is the menu r typed opens, '/' or '@' as MenuKind has it, after
// before, the character before it, or 0 at a block's start; 0 for none.
// It opens where OpensMenu says, but "、", a comma in Chinese and
// Japanese text, only at a block's start or after a space, where it is
// none.
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

// Match is a pair of markers an inline shortcut formats what is between:
// the opening marker from Open to Inner, the text to format from Inner
// to Close, and the closing marker from Close to End, the caret.
type Match struct {
	Open, Inner, Close, End int
	// Format is "bold", "strike", "code" or "italic".
	Format string
}

// markers are the inline shortcuts, tried in turn: "**" before "*".
var markers = []struct{ marker, format string }{
	{"**", "bold"}, {"~~", "strike"}, {"`", "code"}, {"*", "italic"},
}

// Inline finds the marker pair typing text's last unit, at end, closes:
// **bold**, ~~strike~~, `code` or *italic*, around text that neither
// starts nor ends with a space. A marker in a mention, which mentioned
// says of a run of text, is no marker, and a pair never cuts one. text is
// in runes, or in UTF-16 units, as an editor counts its offsets; the
// markers are ASCII, a unit each either way.
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
			// "**" is bold's, not italic around a "*".
			if m == 1 && openAt > 0 && text[openAt-1] == T(mk.marker[0]) {
				break
			}
			return Match{Open: openAt, Inner: start + 1, Close: closeAt, End: end, Format: mk.format}, true
		}
	}
	return Match{}, false
}
