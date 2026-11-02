package icloud

import (
	"io/fs"
	"syscall"
)

// sfDataless is the flag of a file whose content iCloud has not
// downloaded (macOS 14 and iOS 17 and later list such files under their
// own names; reading one waits for the download).
const sfDataless = 0x40000000

func notReady(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}
