package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// pipeHolds reports whether a pipe has bytes to read, which it leaves
// there.
func pipeHolds(f *os.File) bool {
	var n uint32
	ok, _, _ := peekNamedPipe.Call(f.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0) // #nosec G103 -- a count the call writes, alive until it returns.
	return ok != 0 && n > 0
}
