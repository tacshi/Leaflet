package app

import (
	"strings"

	"golang.org/x/sys/unix"
)

// translocated says whether macOS runs this copy from where it cannot
// stay: a random folder it moved a download to, or a volume that cannot
// be written to, as a disk image is. An app's settings would lose it. One
// installed on another drive, under /Volumes, stays where it is.
func translocated(exe string) bool {
	if strings.Contains(exe, "/AppTranslocation/") {
		return true
	}
	var fs unix.Statfs_t
	return unix.Statfs(exe, &fs) == nil && fs.Flags&unix.MNT_RDONLY != 0
}
