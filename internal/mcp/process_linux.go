package mcp

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// started is when process pid began, or false when none runs: its start in
// clock ticks after the boot, 100 a second as Linux gives them, from
// /proc/<pid>/stat, after the boot's time in /proc/stat.
func started(pid int) (time.Time, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	// The fields after the name, which may hold spaces, begin with the
	// third, the state; the start is the 22nd.
	at := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw[at+1:]))
	if at < 0 || len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, true
	}
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, true
	}
	for line := range strings.Lines(string(stat)) {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			boot, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				break
			}
			return time.Unix(boot, 0).Add(time.Duration(ticks) * time.Second / 100), true
		}
	}
	return time.Time{}, true
}
