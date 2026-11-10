//go:build !darwin

package icloud

import "io/fs"

func notReady(fs.FileInfo) bool { return false }

var StartDownload = func(string) error { return nil }
