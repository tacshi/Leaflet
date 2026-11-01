package theme

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestIconsParse checks that every bundled icon draws, as a hand-made one
// such as record.svg may not.
func TestIconsParse(t *testing.T) {
	entries, err := icons.ReadDir("icons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".svg") {
			continue
		}
		data, err := icons.ReadFile("icons/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ui.ParseSVG(data); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
	}
}
