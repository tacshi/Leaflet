package doc

import "testing"

func TestTagColor(t *testing.T) {
	for name, want := range map[string]string{"guide": "#14b8a6", "Work": "#22c55e", "HOME": "#ec4899", "home": "#ec4899"} {
		if got := TagColor(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}
