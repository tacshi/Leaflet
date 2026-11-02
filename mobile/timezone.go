package mobile

import (
	"time"
	// The app's sandbox has no zone files for Go to read, so the core
	// carries its own.
	_ "time/tzdata"
)

// SetTimeZone makes name, an IANA zone such as "Pacific/Auckland", the
// core's local time. Go can't read the device's zone in the app's sandbox
// and would keep to UTC, so bylines, due dates and "today" would be off by
// the zone's offset. offset, in seconds east of UTC, stands in for a name
// the core doesn't know.
func SetTimeZone(name string, offset int) {
	defer catch(nil)
	if loc, err := time.LoadLocation(name); err == nil && name != "" {
		time.Local = loc
		return
	}
	time.Local = time.FixedZone(name, offset)
}
