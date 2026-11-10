package app

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func markDownloaded(path string) {
	const attr = "com.apple.quarantine"
	if _, err := unix.Getxattr(path, attr, nil); err == nil {
		return
	}
	_ = unix.Setxattr(path, attr, fmt.Appendf(nil, "0081;%08x;Leaflet;", time.Now().Unix()), 0)
}
