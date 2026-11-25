package mcp

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess gives a process that
// runs.
const stillActive = 259

// started is when process pid began, or false when none runs. One Leaflet
// may not look into still runs, of a time it cannot tell.
func started(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return time.Time{}, true
	}
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return time.Time{}, false
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, true
	}
	return time.Unix(0, created.Nanoseconds()), true
}
