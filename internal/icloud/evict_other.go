//go:build !darwin

package icloud

import "io/fs"

// Elsewhere, as with iCloud for Windows, reading a file iCloud has not
// downloaded downloads it.
func notReady(fs.FileInfo) bool { return false }

// StartDownload has nothing to ask for. Tests stand in for it.
var StartDownload = func(string) error { return nil }
