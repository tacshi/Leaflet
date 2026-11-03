package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file opened from a page is marked as from the internet first, and a
// mark it has already, as from the browser that downloaded it, stays.
func TestMarkDownloaded(t *testing.T) {
	dir := t.TempDir()
	path, marked := filepath.Join(dir, "a.pdf"), filepath.Join(dir, "b.pdf")
	for _, p := range []string{path, marked} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const browsers = "[ZoneTransfer]\r\nZoneId=3\r\nHostUrl=https://example.com/b.pdf\r\n"
	if err := os.WriteFile(marked+":Zone.Identifier", []byte(browsers), 0o600); err != nil {
		t.Fatal(err)
	}
	markDownloaded(path)
	markDownloaded(marked)
	got, err := os.ReadFile(path + ":Zone.Identifier") // #nosec G304 -- a file in the test's own temporary folder.
	if err != nil || !strings.Contains(string(got), "ZoneId=3") {
		t.Errorf("mark %q, %v", got, err)
	}
	if got, err := os.ReadFile(marked + ":Zone.Identifier"); err != nil || string(got) != browsers { // #nosec G304 -- a file in the test's own temporary folder.
		t.Errorf("the browser's mark became %q, %v", got, err)
	}
}
