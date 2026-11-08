package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A file opened from a page is quarantined first, once.
func TestMarkDownloaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	markDownloaded(path)
	buf := make([]byte, 128)
	n, err := unix.Getxattr(path, "com.apple.quarantine", buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "0081;") || !strings.HasSuffix(string(buf[:n]), ";Leaflet;") {
		t.Fatalf("quarantine %q, %v", buf[:max(n, 0)], err)
	}
}

// An exported zip is quarantined, so what is unpacked from it, the pages'
// files from other devices and people among it, is too.
func TestExportIsQuarantined(t *testing.T) {
	a := templatesApp(t)
	out := filepath.Join(t.TempDir(), "export.zip")
	if _, err := a.writeExport(out, a.keeper(), a.folders, a.files); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Getxattr(out, "com.apple.quarantine", nil); err != nil {
		t.Errorf("not quarantined: %v", err)
	}
}
