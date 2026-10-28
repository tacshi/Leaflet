package app

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// markDownloaded quarantines a file, as browsers do what they download,
// so Gatekeeper checks it before an app or script in it runs. A file
// quarantined already keeps what it has.
func markDownloaded(path string) {
	const attr = "com.apple.quarantine"
	if _, err := unix.Getxattr(path, attr, nil); err == nil {
		return
	}
	_ = unix.Setxattr(path, attr, fmt.Appendf(nil, "0081;%08x;Leaflet;", time.Now().Unix()), 0)
}
