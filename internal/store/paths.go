package store

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The copies of files and the recordings' audio the app keeps in its data
// folder are named in their rows by where they are in it, not by their
// whole paths: the folder can move, as an iOS app's does when it is
// updated, and a whole path would name a file that is gone. Paths go in
// through relPath and come out through absPath.

// relPath is how a row names the file at path: by where it is in dir, when
// it is in it, or by its whole path.
func relPath(dir, path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return path
}

// absPath is the file a row names, with relPath, as it is in dir now. A
// whole path an earlier version kept, of a file that is no longer there,
// names one in a data folder that has moved since: the part after the
// folder ("Leaflet") is looked for in dir.
func absPath(dir, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.Join(dir, filepath.FromSlash(path))
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	slashed := filepath.ToSlash(path)
	i := strings.LastIndex(slashed, "/Leaflet/")
	if i < 0 {
		return path
	}
	rel := filepath.FromSlash(slashed[i+len("/Leaflet/"):])
	if !filepath.IsLocal(rel) {
		return path
	}
	moved := filepath.Join(dir, rel)
	if _, err := os.Stat(moved); err != nil {
		return path
	}
	return moved
}

// Path is the file at path as an entry in the outbox names it (as a row
// does), on this device now.
func (s *Store) Path(path string) string { return absPath(s.Dir, path) }

// OnDevice reports whether a file is on this device.
func OnDevice(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// copyFile copies a file into the app's data folder, whole or not at all
// (writeTo), and returns its size.
func copyFile(from, to string) (int64, error) {
	in, err := os.Open(from) // #nosec G304 -- the file the user picked to add, or a copy of the app's.
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	var n int64
	err = writeTo(to, func(w io.Writer) error {
		var err error
		n, err = io.Copy(w, in)
		return err
	})
	return n, err
}

// writeFrom writes what r reads to a file in the app's data folder, as
// copyFile copies one, as it comes: a download is never all in memory.
func writeFrom(path string, r io.Reader) error {
	return writeTo(path, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// writeTo writes a file the app keeps in its data folder whole or not at
// all: to a file beside it first, which reaches the disk before it moves
// into place, so a row stored after names a whole file, never one a power
// cut left empty or cut short. One that fails leaves nothing.
func writeTo(path string, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".leaflet-*.tmp")
	if err != nil {
		return err
	}
	err = fill(tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir has a move into dir reach the disk, where the system allows it:
// Windows opens no directory to sync.
func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304 -- a folder of the app's data folder.
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// removeLocalCopy removes a file or recording's copy here, only from the
// app's folders, where it keeps them, with the folder of its own it may be
// in once that is empty. It reports whether the copy is gone.
func (s *Store) removeLocalCopy(path string) bool {
	for _, dir := range []string{filepath.Join(s.Dir, "files"), filepath.Join(s.Dir, "recordings")} {
		if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return false
			}
			if parent := filepath.Dir(path); parent != dir {
				_ = os.Remove(parent) // only once it is empty
			}
			return true
		}
	}
	return false
}

// removeCopies removes the copies of files and the recordings' audio the
// app keeps in its data folder, once no row names them: left there, an
// account's attachments and voice would stay on the device after it signs
// out or is deleted.
func (s *Store) removeCopies() error {
	return errors.Join(os.RemoveAll(filepath.Join(s.Dir, "files")), os.RemoveAll(filepath.Join(s.Dir, "recordings")))
}
