//go:build unix

package export

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSaveKeepsPermissions(t *testing.T) {
	old := syscall.Umask(0o027)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := t.TempDir()
	private := filepath.Join(dir, "private.zip")
	if err := os.WriteFile(private, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.md")
	for path, want := range map[string]fs.FileMode{private: 0o600, fresh: 0o640} {
		if err := Save(path, strings.NewReader("new")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: %v, want %v", filepath.Base(path), got, want)
		}
	}
}
