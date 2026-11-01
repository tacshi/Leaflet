package mobile

import (
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"

	"leaflet/internal/shortcut"
)

// Markdown typed in a page, as the desktop's editor takes it
// (internal/shortcut). The app makes the change in its own text, its
// offsets in UTF-16 units, as UIKit counts them.

// MarkdownShortcut returns the block a block of type typ becomes as a
// space is typed after prefix, its text before the caret with the space,
// as JSON {"type", "data"}; "" for text that is no shortcut.
func MarkdownShortcut(typ, prefix string) string {
	t, data, ok := shortcut.Block(typ, prefix)
	if !ok {
		return ""
	}
	raw, _ := encode(menuBlock{Type: t, Data: data})
	return raw
}

// DividerShortcut reports whether a block of type typ whose text is text,
// as its last "-" is typed, becomes a divider.
func DividerShortcut(typ, text string) bool { return shortcut.Divider(typ, text) }

// OpensMenu reports whether "/" or "@" typed after before, the character
// before it, opens its menu.
func OpensMenu(before string) bool {
	r, _ := utf8.DecodeLastRuneInString(before)
	return shortcut.OpensMenu(r)
}

// InlineShortcut returns the marker pair typing text's last character
// closes, text being a block's up to the caret, as JSON {"open", "inner",
// "close", "end", "format"} in UTF-16 offsets; "" for none. mentions are
// the runs of text that are mentions, a JSON array of [from, to].
func InlineShortcut(text, mentions string) string {
	var runs [][2]int
	_ = json.Unmarshal([]byte(mentions), &runs)
	mentioned := func(from, to int) bool {
		for _, r := range runs {
			if from < r[1] && r[0] < to {
				return true
			}
		}
		return false
	}
	units := utf16.Encode([]rune(text))
	m, ok := shortcut.Inline(units, len(units), mentioned)
	if !ok {
		return ""
	}
	raw, _ := encode(map[string]any{"open": m.Open, "inner": m.Inner, "close": m.Close, "end": m.End, "format": m.Format})
	return raw
}
