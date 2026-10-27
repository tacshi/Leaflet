//go:build !darwin

package icloud

import "io/fs"

// Elsewhere, as with iCloud for Windows, reading a file iCloud has not
// downloaded downloads it.
func notReady(fs.FileInfo) bool { return false }

var startDownload = func(string) error { return nil }
