package app

import (
	"slices"
	"testing"
)

func TestPathDirs(t *testing.T) {
	got := pathDirs(`C:\Windows;; %LOCALAPPDATA%\Programs\Leaflet\bin ;C:\Tools;`)
	if want := []string{`C:\Windows`, `%LOCALAPPDATA%\Programs\Leaflet\bin`, `C:\Tools`}; !slices.Equal(got, want) {
		t.Errorf("pathDirs = %q, want %q", got, want)
	}
	t.Setenv("LEAFLET_TEST_DIR", `C:\Users\me\AppData\Local`)
	for d, want := range map[string]bool{
		`%LEAFLET_TEST_DIR%\Programs\Leaflet\bin`:          true,
		`c:\users\me\appdata\local\programs\leaflet\bin\`:  true,
		`"C:\Users\me\AppData\Local\Programs\Leaflet\bin"`: true,
		`C:\Users\me\AppData\Local\Programs\Leaflet`:       false,
	} {
		if got := samePath(d, `C:\Users\me\AppData\Local\Programs\Leaflet\bin`); got != want {
			t.Errorf("samePath(%q) = %v, want %v", d, got, want)
		}
	}
}
