package app

import (
	"path/filepath"
	"testing"
)

func TestOnPath(t *testing.T) {
	const bin = "/programs/Leaflet/bin"
	exe := "/programs/Leaflet/Leaflet.exe"
	same := func(d, dir string) bool { return filepath.Clean(d) == filepath.Clean(dir) }
	for _, c := range []struct {
		name  string
		dirs  []string
		files []string
		other bool
		taken string
	}{
		{name: "alone", dirs: []string{"/windows", bin, "/tools"}},
		{name: "nothing of the name", dirs: []string{"/windows"}, files: []string{"/windows/notepad.exe"}},
		{name: "another program first", dirs: []string{"/tools", bin}, files: []string{"/tools/leaflet.exe"}, taken: "/tools/leaflet.exe"},
		{name: "a script first", dirs: []string{"/npm", bin}, files: []string{"/npm/leaflet.cmd"}, taken: "/npm/leaflet.cmd"},
		{name: "another program after", dirs: []string{bin, "/tools"}, files: []string{"/tools/leaflet.exe"}},
		{name: "another copy first", dirs: []string{"/old/bin", bin}, files: []string{"/old/bin/leaflet.exe", "/old/Leaflet.exe"}, other: true},
		{name: "a folder called bin", dirs: []string{"/usr/bin", bin}, files: []string{"/usr/bin/leaflet.exe"}, taken: "/usr/bin/leaflet.exe"},
		{name: "a copy's folder gone", dirs: []string{"/old/bin", bin}},
		{name: "not listed", dirs: []string{"/tools"}, files: []string{"/tools/leaflet.bat"}, taken: "/tools/leaflet.bat"},
	} {
		isFile := func(path string) bool {
			for _, f := range c.files {
				if filepath.ToSlash(path) == f {
					return true
				}
			}
			return false
		}
		s := onPath(commandStatus{supported: true}, c.dirs, "leaflet", exe, bin, same, isFile)
		if s.other != c.other || filepath.ToSlash(s.taken) != c.taken || !s.supported {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}
