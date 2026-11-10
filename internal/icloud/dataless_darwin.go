package icloud

import (
	"io/fs"
	"syscall"
)

const sfDataless = 0x40000000

func notReady(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}
