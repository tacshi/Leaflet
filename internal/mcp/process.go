package mcp

import "time"

// running is whether process pid runs and is the one that began about when
// began says: one that began later, as a process given the number of an app
// that ended since, is another. A pid of 0, as a view of an earlier version
// keeps, is taken to run.
func running(pid int, began time.Time) bool {
	if pid <= 0 {
		return true
	}
	at, ok := started(pid)
	return ok && !at.After(began.Add(time.Second))
}
