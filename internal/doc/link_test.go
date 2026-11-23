package doc

import "testing"

func TestLinkHrefMakesLinksThatOpen(t *testing.T) {
	for typed, want := range map[string]string{
		" example.com/a ": "https://example.com/a", "https://x.dev": "https://x.dev", "localhost:3000": "https://localhost:3000",
		"ann@example.com": "mailto:ann@example.com", "mailto:ann@example.com": "mailto:ann@example.com",
		PageLink("p1"): PageLink("p1"), "   ": "",
	} {
		got, ok := LinkHref(typed)
		if !ok || got != want {
			t.Errorf("%q links %q, %v; want %q", typed, got, ok, want)
		}
		if _, page := PageLinkID(got); got != "" && !page && SafeHref(got) == "" {
			t.Errorf("%q links %q, which opens nothing", typed, got)
		}
	}
	for _, typed := range []string{"two words", "file:///etc/hosts", "javascript:alert(1)", "word", "mailto:"} {
		if got, ok := LinkHref(typed); ok {
			t.Errorf("%q links %q", typed, got)
		}
	}
}
