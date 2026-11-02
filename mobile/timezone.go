package mobile

import (
	"log"
	"sync"
	"time"
	// The app's sandbox has no zone files for Go to read, so the core
	// carries its own.
	_ "time/tzdata"
)

// zone is the zone time.Local is, as SetTimeZone set it. Once a core is
// open, time.Local stays as it is: everything reads it, from goroutines of
// their own, and changing it under them would race. A zone the device
// moves into meanwhile is the core's when the app opens it next.
var zone struct {
	sync.Mutex
	fixed bool
}

// setLocal makes loc time.Local; a test sees it instead.
var setLocal = func(loc *time.Location) { time.Local = loc }

// SetTimeZone makes name, an IANA zone such as "Pacific/Auckland", the
// core's local time, given before Open. Go can't read the device's zone in
// the app's sandbox and would keep to UTC, so bylines, due dates and
// "today" would be off by the zone's offset. offset, in seconds east of
// UTC, stands in for a name the core doesn't know. A zone loaded by name
// follows its clock changes itself.
func SetTimeZone(name string, offset int) {
	defer catch(nil)
	loc, err := time.LoadLocation(name)
	if err != nil || name == "" {
		loc = time.FixedZone(name, offset)
	}
	zone.Lock()
	defer zone.Unlock()
	if zone.fixed {
		if loc.String() != time.Local.String() {
			log.Printf("mobile: the time zone %s applies once Leaflet opens again", loc)
		}
		return
	}
	setLocal(loc)
}

// fixZone keeps time.Local as it is from now on, as a core opens.
func fixZone() {
	zone.Lock()
	defer zone.Unlock()
	zone.fixed = true
}
