//go:build !darwin && !linux && !windows

package mcp

import "time"

// started is when process pid began: where Leaflet cannot tell, a process
// is taken to run, from before any view of it.
func started(int) (time.Time, bool) { return time.Time{}, true }
