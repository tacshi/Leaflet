package editor

import (
	"strings"
	"testing"

	"leaflet/internal/skills"
)

// The skill that tells agents how to color text gives the color picker's
// palette, whose colors turn lighter in the dark look and whose highlights
// are the same colors with alpha 40.
func TestExpressiveSkillGivesThePalette(t *testing.T) {
	s, _ := skills.Get(skills.Expressive)
	for _, c := range colors {
		if !strings.Contains(s.Body, "| "+c.key+" | `"+c.hex+"` |") {
			t.Errorf("%s leaves out %s %s", s.Name, c.key, c.hex)
		}
	}
	if !strings.Contains(s.Body, "with `"+highlightAlpha+"` after it") {
		t.Errorf("%s does not say a highlight's alpha is %s", s.Name, highlightAlpha)
	}
}
