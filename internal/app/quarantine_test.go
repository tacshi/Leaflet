package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A file that would run as a program is shown in its folder, not opened.
func TestRunsAsProgram(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{"notes.pdf": 0o600, "setup.EXE": 0o600, "run.command": 0o600, "tool": 0o700} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{"notes.pdf": false, "setup.EXE": true, "run.command": true, "tool": runtime.GOOS != "windows"}
	for name, runs := range want {
		if got := runsAsProgram(filepath.Join(dir, name)); got != runs {
			t.Errorf("runsAsProgram(%s) = %v", name, got)
		}
	}
	// Shortcuts, links and launchers, which run or send elsewhere as they
	// open, though they are no programs themselves.
	for _, name := range []string{"app.desktop", "site.url", "site.webloc", "site.inetloc", "share.library-ms", "find.search-ms", "help.chm"} {
		if !runsAsProgram(filepath.Join(dir, name)) {
			t.Errorf("runsAsProgram(%s) = false", name)
		}
	}
}
