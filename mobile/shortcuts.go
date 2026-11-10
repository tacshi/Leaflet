package mobile

import (
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"

	"leaflet/internal/shortcut"
)

func MarkdownShortcut(typ, prefix string) string {
	defer catch(nil)
	t, data, ok := shortcut.Block(typ, prefix)
	if !ok {
		return ""
	}
	raw, _ := encode(menuBlock{Type: t, Data: data})
	return raw
}

func DividerShortcut(typ, text string) bool {
	defer catch(nil)
	return shortcut.Divider(typ, text)
}

func Menu(typed, before string) string {
	defer catch(nil)
	r, n := utf8.DecodeRuneInString(typed)
	if n == 0 || n != len(typed) {
		return ""
	}
	var b rune
	if before != "" {
		b, _ = utf8.DecodeLastRuneInString(before)
	}
	if m := shortcut.Menu(r, b); m != 0 {
		return string(m)
	}
	return ""
}

func MenuKind(char string) string {
	defer catch(nil)
	r, n := utf8.DecodeRuneInString(char)
	if n == 0 || n != len(char) {
		return ""
	}
	if m := shortcut.MenuKind(r); m != 0 {
		return string(m)
	}
	return ""
}

func InlineShortcut(text, mentions string) string {
	defer catch(nil)
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
