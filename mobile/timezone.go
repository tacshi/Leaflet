package mobile

import (
	"log"
	"sync"
	"time"
	_ "time/tzdata"
)

var zone struct {
	sync.Mutex
	fixed bool
}

var setLocal = func(loc *time.Location) { time.Local = loc }

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

func fixZone() {
	zone.Lock()
	defer zone.Unlock()
	zone.fixed = true
}
