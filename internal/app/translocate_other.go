//go:build !darwin

package app

// translocated is false here: only macOS moves an app it runs.
func translocated(string) bool { return false }
