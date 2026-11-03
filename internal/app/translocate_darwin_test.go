package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A copy Gatekeeper moved, or one on a volume that can't be written to,
// as a disk image is, must move first; one on another drive needn't.
func TestTranslocated(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "Leaflet")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		exe: false,
		"/private/var/folders/xy/T/AppTranslocation/0A1B/d/Leaflet.app/Contents/MacOS/Leaflet": true,
		"/Volumes/Backup/Applications/Leaflet.app/Contents/MacOS/Leaflet":                      false,
		// The system's own volume is sealed, read-only.
		"/usr/bin/true": true,
	} {
		if got := translocated(path); got != want {
			t.Errorf("translocated(%s) = %v, want %v", path, got, want)
		}
	}
}
