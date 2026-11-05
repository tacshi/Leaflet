package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"leaflet/locales"
)

// The command line is bin\<name>.exe beside the app (cmd/leaflet), a
// console program, which the app is not. Installing it puts its folder on
// the user's Path, which terminals opened after read.

// commandBin is the folder of the command line beside exe.
func commandBin(exe string) string { return filepath.Join(filepath.Dir(exe), "bin") }

func commandState(name, exe string) commandStatus {
	bin := commandBin(exe)
	// A build without it, as go tool mygo dev makes, has none to install.
	if _, err := os.Stat(filepath.Join(bin, name+".exe")); err != nil {
		return commandStatus{}
	}
	path, _, _ := userPath()
	installed := slices.ContainsFunc(pathDirs(path), func(d string) bool { return samePath(d, bin) })
	s := commandStatus{supported: true, installed: installed, newTerminal: true}
	return onPath(s, terminalDirs(path), name, exe, bin, samePath, isFile)
}

// installCommand puts the command's folder on the user's Path, in place of
// other copies': each listed there would stay in front of it. Another
// program's command in front of it is left as it is.
func installCommand(name, exe, _ string) error {
	if s := commandState(name, exe); s.taken != "" && !s.installed {
		return errors.New(locales.ExternalCommandTaken(s.taken))
	}
	path, kind, err := userPath()
	if err != nil {
		return err
	}
	bin := commandBin(exe)
	here := func(d string) bool { return samePath(d, bin) }
	dirs := pathDirs(path)
	kept := slices.DeleteFunc(slices.Clone(dirs), func(d string) bool {
		return !here(d) && isCopyBin(expandDir(d), name, exe, isFile)
	})
	if !slices.ContainsFunc(kept, here) {
		kept = append(kept, bin)
	}
	if slices.Equal(kept, dirs) {
		return nil
	}
	return setUserPath(strings.Join(kept, ";"), kind)
}

func removeCommand(name, exe, _ string) error {
	path, kind, err := userPath()
	if err != nil {
		return err
	}
	bin := commandBin(exe)
	dirs := pathDirs(path)
	kept := slices.DeleteFunc(slices.Clone(dirs), func(d string) bool { return samePath(d, bin) })
	if len(kept) == len(dirs) {
		return nil
	}
	return setUserPath(strings.Join(kept, ";"), kind)
}

// userPath is the user's own Path, and the kind of value it is kept as,
// which may hold %variables%.
func userPath() (string, uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", registry.EXPAND_SZ, err
	}
	defer func() { _ = k.Close() }()
	path, kind, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.EXPAND_SZ, nil
	}
	return path, kind, err
}

// terminalDirs are the folders terminals look for a command in, in order:
// the computer's Path, then user, the user's.
func terminalDirs(user string) []string {
	var out []string
	for _, d := range pathDirs(systemPath() + ";" + user) {
		out = append(out, expandDir(d))
	}
	return out
}

// systemPath is the Path of all the computer's users, "" when it cannot be
// read: a user's stands after it.
func systemPath() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	path, _, _ := k.GetStringValue("Path")
	return path
}

// isFile reports whether a file is at path.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// setUserPath keeps path as the user's Path, of the kind it was, and tells
// the programs running, as Explorer, which starts terminals.
func setUserPath(path string, kind uint32) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()
	if kind == registry.SZ {
		err = k.SetStringValue("Path", path)
	} else {
		err = k.SetExpandStringValue("Path", path)
	}
	if err != nil {
		return err
	}
	env, _ := windows.UTF16PtrFromString("Environment")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung, wait = 0xffff, 0x001a, 0x0002, 5000
	_, _, _ = sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, wait, 0) // #nosec G103 -- a string the call reads, alive until it returns.
	return nil
}

var sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// pathDirs are the folders of a Path.
func pathDirs(path string) []string {
	var out []string
	for d := range strings.SplitSeq(path, ";") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// expandDir is the Path's folder d, which may hold %variables% and quotes,
// as a path.
func expandDir(d string) string {
	if x, err := registry.ExpandString(d); err == nil {
		d = x
	}
	return filepath.Clean(strings.Trim(d, `"`))
}

// samePath reports whether the Path's folder d is dir.
func samePath(d, dir string) bool {
	return strings.EqualFold(expandDir(d), filepath.Clean(dir))
}
