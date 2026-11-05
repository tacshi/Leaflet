package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/locales"
)

// The command line's card installs the command, says how to run it, and
// removes it.
func TestCommandSettings(t *testing.T) {
	commandDir = filepath.Join(t.TempDir(), "bin")
	t.Cleanup(func() { commandDir = "/usr/local/bin" })
	exe, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(exe, "Leaflet")
	if err := os.WriteFile(exe, nil, 0o755); err != nil { // #nosec G306 -- an executable, as the app's is.
		t.Fatal(err)
	}
	a := templatesApp(t)
	posted := make(chan func(), 4)
	a.post = func(fn func()) { posted <- fn }
	tt := ui.NewTester(func(c *ui.Context) { a.commandSettings(c, exe, false) }, 700, 200)
	tt.Frame()
	(<-posted)()
	tt.Frame()
	name := commandName()
	if !tt.HasText(locales.ExternalCommandTitle(name)) || !tt.HasText(locales.ExternalCommandDesc()) {
		t.Fatalf("card %v", tt.Texts())
	}
	if err := tt.Click(locales.ExternalCommandInstall()); err != nil {
		t.Fatal(err)
	}
	(<-posted)()
	tt.Frame()
	if target, err := os.Readlink(filepath.Join(commandDir, name)); err != nil || target != exe {
		t.Fatalf("link to %q, %v", target, err)
	}
	if !tt.HasText(locales.ExternalCommandInstalled(name)) {
		t.Errorf("installed %v", tt.Texts())
	}
	if err := tt.Click(locales.ExternalCommandRemove()); err != nil {
		t.Fatal(err)
	}
	(<-posted)()
	tt.Frame()
	if _, err := os.Lstat(filepath.Join(commandDir, name)); !os.IsNotExist(err) || !tt.HasText(locales.ExternalCommandInstall()) {
		t.Errorf("after removing: %v, %v", err, tt.Texts())
	}
}

func TestInstallCommand(t *testing.T) {
	root := t.TempDir()
	commandDir = filepath.Join(root, "bin")
	t.Cleanup(func() { commandDir = "/usr/local/bin" })
	app := func(name string) string {
		exe := filepath.Join(root, name+".app", "Contents", "MacOS", name)
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, nil, 0o755); err != nil { // #nosec G306 -- an executable, as an app's is.
			t.Fatal(err)
		}
		exe, _ = filepath.EvalSymlinks(exe)
		return exe
	}
	exe, old := app("Leaflet"), app("Old Leaflet")
	link := filepath.Join(commandDir, "leaflet")

	if s := commandState("leaflet", exe); !s.supported || s.installed || s.other || s.taken != "" {
		t.Errorf("before installing %+v", s)
	}
	// The folder made, as /usr/local/bin may not be there.
	if err := installCommand("leaflet", exe, ""); err != nil {
		t.Fatal(err)
	}
	if s := commandState("leaflet", exe); !s.installed {
		t.Errorf("after installing %+v", s)
	}
	// Another copy's link, or a link to one deleted, is replaced.
	for _, target := range []string{old, filepath.Join(root, "Gone.app", "Contents", "MacOS", "Gone")} {
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if s := commandState("leaflet", exe); !s.other || s.installed {
			t.Errorf("linked to %s: %+v", target, s)
		}
		if err := removeCommand("leaflet", exe, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Error("removing removed another copy's link")
		}
		if err := installCommand("leaflet", exe, ""); err != nil {
			t.Fatal(err)
		}
		if s := commandState("leaflet", exe); !s.installed {
			t.Errorf("after updating from %s: %+v", target, s)
		}
	}
	if err := removeCommand("leaflet", exe, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("after removing: %v", err)
	}

	// Another program's command, a file or a link to one, is left alone.
	other := filepath.Join(root, "other")
	if err := os.WriteFile(other, nil, 0o755); err != nil { // #nosec G306 -- an executable.
		t.Fatal(err)
	}
	for _, place := range []func() error{
		func() error { return os.Symlink(other, link) },
		func() error { return os.WriteFile(link, []byte("#!/bin/sh\n"), 0o755) }, // #nosec G306 -- an executable.
	} {
		_ = os.Remove(link)
		if err := place(); err != nil {
			t.Fatal(err)
		}
		if s := commandState("leaflet", exe); s.taken != link || s.other || s.installed {
			t.Errorf("another program's %+v", s)
		}
		if err := installCommand("leaflet", exe, ""); err == nil {
			t.Error("installing over another program's command")
		}
		if s := commandState("leaflet", exe); s.taken != link {
			t.Errorf("after installing over another program's %+v", s)
		}
	}
}
