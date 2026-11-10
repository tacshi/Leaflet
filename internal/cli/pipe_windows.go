package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

func pipeHolds(f *os.File) bool {
	var n uint32
	ok, _, _ := peekNamedPipe.Call(f.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0) // #nosec G103
	return ok != 0 && n > 0
}
