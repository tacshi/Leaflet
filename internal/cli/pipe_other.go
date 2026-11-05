//go:build !darwin && !windows

package cli

import "os"

// pipeHolds says nothing of a pipe here: Leaflet runs on macOS and
// Windows.
func pipeHolds(*os.File) bool { return false }
