package app

import (
	"strings"

	"golang.org/x/sys/unix"
)

func translocated(exe string) bool {
	if strings.Contains(exe, "/AppTranslocation/") {
		return true
	}
	var fs unix.Statfs_t
	return unix.Statfs(exe, &fs) == nil && fs.Flags&unix.MNT_RDONLY != 0
}
