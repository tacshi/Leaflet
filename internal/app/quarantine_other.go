//go:build !darwin && !windows

package app

// markDownloaded does nothing here: the system keeps no mark of where a
// file came from.
func markDownloaded(string) {}
