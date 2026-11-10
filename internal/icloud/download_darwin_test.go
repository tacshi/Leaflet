//go:build darwin && !ios

package icloud

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartDownloadOutsideICloud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := StartDownload(path)
	if err == nil {
		t.Fatal("no error for a file iCloud Drive does not keep")
	}
	if err.Error() == "" {
		t.Error("the error says nothing")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != `{}` { // #nosec G304
		t.Errorf("the file was touched: %q, %v", data, err)
	}

	f := Folder{Root: filepath.Dir(path)}
	if err := f.download(path); !errors.Is(err, ErrNotReady) {
		t.Errorf("a download that could not start: %v, want not ready", err)
	}
}
