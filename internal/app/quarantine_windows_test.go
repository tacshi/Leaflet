package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	got, err := os.ReadFile(path + ":Zone.Identifier") // #nosec G304
	if err != nil || !strings.Contains(string(got), "ZoneId=3") {
		t.Errorf("mark %q, %v", got, err)
	}
	if got, err := os.ReadFile(marked + ":Zone.Identifier"); err != nil || string(got) != browsers { // #nosec G304
		t.Errorf("the browser's mark became %q, %v", got, err)
	}
}
