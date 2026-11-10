package theme

import (
	"leaflet/internal/theme/icons"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIconsParse(t *testing.T) {
	for _, name := range icons.Names() {
		data, ok := icons.Read(name)
		if !ok {
			t.Fatalf("%s: not read", name)
		}
		if _, err := ui.ParseSVG(data); err != nil {
			t.Errorf("%s.svg: %v", name, err)
		}
	}
}
