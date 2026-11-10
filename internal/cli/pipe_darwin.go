package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

const fionread = 0x4004667f

func pipeHolds(f *os.File) bool {
	n, err := unix.IoctlGetInt(int(f.Fd()), fionread) // #nosec G115
	return err == nil && n > 0
}
