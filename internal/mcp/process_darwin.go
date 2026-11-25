package mcp

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// started is when process pid began, or false when none runs. One Leaflet
// may not look into still runs, of a time it cannot tell.
func started(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EPERM) {
		return time.Time{}, true
	}
	if err != nil || int(kp.Proc.P_pid) != pid {
		return time.Time{}, false
	}
	return time.Unix(kp.Proc.P_starttime.Unix()), true
}
