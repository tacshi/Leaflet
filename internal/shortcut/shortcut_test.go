package shortcut

import (
	"testing"
	"unicode/utf16"
)

func TestBlock(t *testing.T) {
	for _, tc := range []struct {
		typ, prefix, want string
		ok                bool
	}{
		{"paragraph", "# ", "heading", true},
		{"paragraph", "###　", "heading", true},
		{"paragraph", "＃　", "heading", true},
		{"paragraph", "１．　", "numbered_list", true},
		{"paragraph", "－ ", "bulleted_list", true},
		{"paragraph", "＞　", "quote", true},
		{"paragraph", "［］　", "todo_list", true},
		{"paragraph", "``` ", "code", true},
		{"paragraph", "[X] ", "todo_list", true},
		{"bulleted_list", "[] ", "todo_list", true},
		{"bulleted_list", "# ", "", false},
		{"heading", "- ", "", false},
		{"paragraph", "#", "", false},
		{"paragraph", "a # ", "", false},
	} {
		typ, data, ok := Block(tc.typ, tc.prefix)
		if typ != tc.want || ok != tc.ok {
			t.Errorf("Block(%q, %q) = %q, %v", tc.typ, tc.prefix, typ, ok)
		}
		if tc.prefix == "[X] " && data["checked"] != true {
			t.Errorf("[X] makes %v", data)
		}
	}
	if _, data, _ := Block("paragraph", "## "); data["level"] != 2 {
		t.Errorf("## makes %v", data)
	}
}

func TestInline(t *testing.T) {
	none := func(int, int) bool { return false }
	for text, want := range map[string]string{
		"a **b**":   "bold",
		"~~gone~~":  "strike",
		"`x`":       "code",
		"say *it*":  "italic",
		"** b**":    "",
		"***":       "",
		"x * y *":   "",
		"no marker": "",
	} {
		m, ok := Inline([]rune(text), len([]rune(text)), none)
		if m.Format != want || ok != (want != "") {
			t.Errorf("%q makes %q", text, m.Format)
		}
	}
	text := utf16.Encode([]rune("😀 **hi**"))
	m, ok := Inline(text, len(text), none)
	if !ok || m.Open != 3 || m.Inner != 5 || m.Close != 7 || m.End != 9 {
		t.Errorf("UTF-16 match %+v", m)
	}
	inMention := func(from, to int) bool { return from < 3 }
	if _, ok := Inline([]rune("*a* b*"), 6, inMention); ok {
		t.Error("a pair cut a mention")
	}
}

func TestOpensMenu(t *testing.T) {
	for r, want := range map[rune]bool{' ': true, '字': true, 'か': true, '。': true, 'a': false, '.': false, '한': false} {
		if OpensMenu(r) != want {
			t.Errorf("OpensMenu(%q) = %v", r, !want)
		}
	}
}

func TestMenu(t *testing.T) {
	for _, tc := range []struct {
		r, before, want rune
	}{
		{'/', 0, '/'}, {'／', 0, '/'}, {'／', '字', '/'}, {'＠', ' ', '@'}, {'@', 'a', 0}, {'／', 'a', 0},
		{'、', 0, '/'}, {'、', ' ', '/'}, {'、', '　', '/'}, {'、', '果', 0}, {'x', 0, 0},
	} {
		if got := Menu(tc.r, tc.before); got != tc.want {
			t.Errorf("Menu(%q, %q) = %q, want %q", tc.r, tc.before, got, tc.want)
		}
	}
	if !Divider("paragraph", "－－－") || Divider("heading", "---") {
		t.Error("a full-width divider")
	}
}
