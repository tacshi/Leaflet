//go:build !darwin && !windows

package cli

import "os"

func pipeHolds(*os.File) bool { return false }
