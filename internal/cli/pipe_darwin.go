package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// fionread is macOS's FIONREAD, which golang.org/x/sys does not name.
const fionread = 0x4004667f

// pipeHolds reports whether a pipe has bytes to read, which it leaves
// there.
func pipeHolds(f *os.File) bool {
	n, err := unix.IoctlGetInt(int(f.Fd()), fionread) // #nosec G115 -- a descriptor, small.
	return err == nil && n > 0
}
