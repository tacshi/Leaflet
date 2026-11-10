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

var commandDir = "/usr/local/bin"

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
			s.other = true
		default:
			s.taken = link
		}
	}
	return s
}

func isAppExecutable(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "MacOS" && strings.HasSuffix(filepath.Dir(filepath.Dir(path)), ".app/Contents")
}

func installCommand(name, exe, prompt string) error {
	link := filepath.Join(commandDir, name)
	if s := commandState(name, exe); s.taken != "" {
		return errors.New(locales.ExternalCommandTaken(s.taken))
	}
	err := os.MkdirAll(commandDir, 0o755) // #nosec G301
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

func asAdministrator(script, prompt string) error {
	out, err := exec.Command("/usr/bin/osascript", // #nosec G204
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
