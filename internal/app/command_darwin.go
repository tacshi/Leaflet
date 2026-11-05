package app

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"leaflet/locales"
)

// commandDir is where the command line's link goes: on the PATH of every
// shell of a Mac (/etc/paths), though it may not be there yet, nor
// writable without an administrator.
var commandDir = "/usr/local/bin"

// commandState is how the command named name stands: a link to exe, this
// app's executable, as installCommand makes it.
func commandState(name, exe string) commandStatus {
	s := commandStatus{supported: true}
	link := filepath.Join(commandDir, name)
	info, err := os.Lstat(link)
	switch {
	case err != nil:
	case info.Mode()&os.ModeSymlink == 0:
		s.taken = link
	default:
		target, err := filepath.EvalSymlinks(link)
		switch {
		case err == nil && target == exe:
			s.installed = true
		case err != nil || isAppExecutable(target):
			// Another copy of Leaflet, or one since deleted.
			s.other = true
		default:
			s.taken = link
		}
	}
	return s
}

// isAppExecutable reports whether path is an app's executable, in its
// bundle.
func isAppExecutable(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "MacOS" && strings.HasSuffix(filepath.Dir(filepath.Dir(path)), ".app/Contents")
}

// installCommand links the command named name to exe, in place of a link
// to another copy: as the user can, else as an administrator, which macOS
// asks for with prompt.
func installCommand(name, exe, prompt string) error {
	link := filepath.Join(commandDir, name)
	if s := commandState(name, exe); s.taken != "" {
		return errors.New(locales.ExternalCommandTaken(s.taken))
	}
	err := os.MkdirAll(commandDir, 0o755) // #nosec G301 -- a folder of commands, which every user runs.
	if err == nil {
		if err = os.Remove(link); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if err == nil {
		err = os.Symlink(exe, link)
	}
	if errors.Is(err, fs.ErrPermission) {
		return asAdministrator("/bin/mkdir -p "+shellQuote(commandDir)+" && /bin/ln -sfn "+shellQuote(exe)+" "+shellQuote(link), prompt)
	}
	return err
}

// removeCommand removes the link installCommand made.
func removeCommand(name, exe, prompt string) error {
	link := filepath.Join(commandDir, name)
	if !commandState(name, exe).installed {
		return nil
	}
	err := os.Remove(link)
	if errors.Is(err, fs.ErrPermission) {
		return asAdministrator("/bin/rm -f "+shellQuote(link), prompt)
	}
	return err
}

// asAdministrator runs the shell command script as an administrator,
// once macOS has asked for one's password with prompt. Both reach
// AppleScript as arguments, never in its text. Cancelling is no error:
// nothing changed.
func asAdministrator(script, prompt string) error {
	out, err := exec.Command("/usr/bin/osascript", // #nosec G204 -- AppleScript of the app's own, with the command and prompt as its arguments.
		"-e", "on run argv",
		"-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
		"-e", "end run",
		script, prompt).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "(-128)") {
			return nil
		}
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
	}
	return err
}
