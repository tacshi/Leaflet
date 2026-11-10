package store

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func relPath(dir, path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return path
}

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

func (s *Store) Path(path string) string { return absPath(s.Dir, path) }

func OnDevice(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(from, to string) (int64, error) {
	in, err := os.Open(from) // #nosec G304
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

func writeFrom(path string, r io.Reader) error {
	return writeTo(path, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

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

func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (s *Store) removeLocalCopy(path string) bool {
	for _, dir := range []string{filepath.Join(s.Dir, "files"), filepath.Join(s.Dir, "recordings")} {
		if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return false
			}
			if parent := filepath.Dir(path); parent != dir {
				_ = os.Remove(parent)
			}
			return true
		}
	}
	return false
}

// removeCopies removes the copies of files and recordings once their rows
// are gone. A copy that cannot go yet, as the audio a recording still has
// open on Windows, is removed by what holds it: failing here would leave
// the rows gone and the call half done.
func (s *Store) removeCopies() {
	_ = os.RemoveAll(filepath.Join(s.Dir, "files"))
	_ = os.RemoveAll(filepath.Join(s.Dir, "recordings"))
}
