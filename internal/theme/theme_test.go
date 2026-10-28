package theme

import "testing"

func TestPageAndCodeFamilies(t *testing.T) {
	t.Cleanup(func() { SetFonts("", "", "", "") })
	cases := []struct {
		name                  string
		latin, zh, ja, code   string
		text, page, codeFonts string
	}{
		{"defaults", "", "", "", "", "日本語の文", PageFont, "monospace"},
		{"latin only", "Helvetica Neue", "", "", "Menlo", "中文", "Helvetica Neue", "Menlo"},
		{"chinese text", "Georgia", "Kaiti SC", "Hiragino Mincho ProN", "", "中文段落 with English",
			"Georgia, Kaiti SC", "monospace, Kaiti SC"},
		{"japanese text", "", "Kaiti SC", "Hiragino Mincho ProN", "Menlo", "漢字とかな",
			PageFont + ", Hiragino Mincho ProN", "Menlo, Hiragino Mincho ProN"},
		{"katakana", "", "Kaiti SC", "Hiragino Mincho ProN", "", "コード", PageFont + ", Hiragino Mincho ProN", "monospace, Hiragino Mincho ProN"},
		{"no chinese font", "", "", "Hiragino Mincho ProN", "", "中文", PageFont, "monospace"},
	}
	for _, tc := range cases {
		SetFonts(tc.latin, tc.zh, tc.ja, tc.code)
		if got := PageFamily(tc.text); got != tc.page {
			t.Errorf("%s: PageFamily = %q, want %q", tc.name, got, tc.page)
		}
		if got := CodeFamily(tc.text); got != tc.codeFonts {
			t.Errorf("%s: CodeFamily = %q, want %q", tc.name, got, tc.codeFonts)
		}
	}
}
