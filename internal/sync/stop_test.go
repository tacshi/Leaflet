package sync

import (
	"testing"

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
