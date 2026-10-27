package icloud

import (
	"os"
	"path/filepath"
	"testing"
)

// Foundation answers for a file outside iCloud Drive with an error, not
// a crash.
func TestStartDownloadOutsideICloud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startDownload(path); err == nil {
		t.Log("iCloud started a download outside iCloud Drive")
	} else {
		t.Logf("as expected: %v", err)
	}
}
