package sync

import (
	"testing"
	"time"

	"leaflet/internal/remote"
)

// Stop ends the loop and the pass it runs, as an iOS app's scene going to
// the background does; a pass asked for after it runs to its end, and
// Start starts the loop again.
func TestStopAndStartAgain(t *testing.T) {
	st := openStore(t)
	s := New(st, remote.New(remote.Config(), st))
	s.Start()
	loop := s.passContext()
	if loop.Err() != nil {
		t.Fatal("the loop's passes start stopped")
	}
	s.Stop()
	if loop.Err() == nil {
		t.Error("Stop left the loop's pass running")
	}
	if s.passContext().Err() != nil {
		t.Error("a pass asked for after Stop is stopped")
	}
	s.Stop()
	s.Start()
	defer s.Stop()
	if again := s.passContext(); again == loop || again.Err() != nil {
		t.Error("Start did not start the loop again")
	}
}

// Stop cuts short a pass SyncNow asked for while no loop runs, as an iOS
// app's background refresh does when its time runs out.
func TestStopCutsAPassAskedFor(t *testing.T) {
	st := openStore(t)
	s := New(st, remote.New(remote.Config(), st))
	pass, done := s.startPass()
	defer done()
	s.Stop()
	if pass.Err() == nil {
		t.Error("Stop left the pass running")
	}
	done()
	if again, end := s.startPass(); again.Err() != nil {
		t.Error("a pass after Stop starts stopped")
	} else {
		end()
	}
}

// A pass of the loop's that panics is logged, and the loop runs the next.
func TestTheLoopOutlastsAPassThatPanics(t *testing.T) {
	root := cloudRoot(t)
	d := newCloudDevice(t, root)
	starts, ends := make(chan int, 4), make(chan bool, 4)
	n := 0
	d.svc.OnState = func(running bool, _ error) {
		if !running {
			ends <- true
			return
		}
		n++
		starts <- n
		if n == 1 {
			panic("a pass's bug")
		}
	}
	d.svc.Start()
	defer d.svc.Stop()
	if got := <-starts; got != 1 {
		t.Fatalf("pass %d first", got)
	}
	d.svc.Kick()
	select {
	case <-ends:
	case <-time.After(10 * time.Second):
		t.Fatal("no pass after the one that panicked")
	}
	// The loop is stopped before its folders go.
	d.svc.Stop()
	d.svc.Hold(func() {})
}

// A pass that panics ends as one that failed: the app is told it stopped,
// and never shows it syncing for good.
func TestAPassThatPanicsEnds(t *testing.T) {
	d := newCloudDevice(t, cloudRoot(t))
	d.svc.DeviceName = func() string { panic("a pass's bug") }
	var states []bool
	var last error
	d.svc.OnState = func(running bool, err error) { states, last = append(states, running), err }
	if err := d.svc.SyncNow(); err == nil {
		t.Error("the pass that panicked did not fail")
	}
	if len(states) != 2 || states[1] || last == nil {
		t.Errorf("states %v, last error %v", states, last)
	}
}
