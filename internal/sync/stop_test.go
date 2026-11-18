package sync

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

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
	d.svc.Stop()
	d.svc.Hold(func() {})
}

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

func TestStopEndsAPassWaitingToRun(t *testing.T) {
	d := newCloudDevice(t, cloudRoot(t))
	var runs atomic.Int32
	d.svc.OnState = func(running bool, _ error) {
		if running {
			runs.Add(1)
		}
	}
	held, release := make(chan struct{}), make(chan struct{})
	go d.svc.Hold(func() {
		close(held)
		<-release
	})
	<-held
	d.svc.Start()
	loop := d.svc.passContext()
	done := make(chan struct{})
	go func() {
		d.svc.loopPass(loop, true)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	d.svc.Stop()
	close(release)
	<-done
	if n := runs.Load(); n != 0 {
		t.Errorf("%d passes ran after Stop", n)
	}
}

func TestInterruptCutsAPageCheckAndAsksNothingMeanwhile(t *testing.T) {
	st := openStore(t)
	page, err := st.CreatePage(store.NewPageOptions{Title: "Open"})
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan struct{}, 1)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/v1/rpc/page_members" {
			_, _ = io.Copy(io.Discard, r.Body)
			asked <- struct{}{}
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer hs.Close()
	svc := New(st, remote.SignedInForTest(hs.URL, st, "u1"))
	var interrupting, askedMeanwhile atomic.Bool
	svc.Online = func() bool {
		if interrupting.Load() {
			askedMeanwhile.Store(true)
		}
		return true
	}
	svc.SetOpen(page.ID, true)
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the open page was never checked")
	}
	interrupting.Store(true)
	ran := make(chan bool, 1)
	go svc.Interrupt(func() { ran <- svc.CanSync() })
	select {
	case can := <-ran:
		if can {
			t.Error("sync could start while interrupted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Interrupt waited on the page check rather than cut it")
	}
	interrupting.Store(false)
	if askedMeanwhile.Load() {
		t.Error("the cut work asked whether the app is online")
	}
	if !svc.CanSync() {
		t.Error("sync stayed off after Interrupt")
	}
}

func TestForgetStartsAnotherAccountAfresh(t *testing.T) {
	st := openStore(t)
	svc := New(st, remote.SignedInForTest("http://127.0.0.1:1", st, "u1"))
	svc.backOff(&remote.Error{Status: http.StatusTooManyRequests, RetryAfter: time.Minute})
	svc.rest(nil)
	svc.mu.Lock()
	svc.membersAt[ownSwept] = time.Now()
	svc.unread["p"] = 3
	svc.waitingAfter = "p"
	svc.retryAt[outboxKey{1, 1}] = time.Now().Add(time.Hour)
	svc.mu.Unlock()
	svc.ownLogChecked = true
	if !svc.busy() || !svc.resting() {
		t.Fatal("the server's wait was not kept")
	}
	svc.Hold(svc.Forget)
	if svc.busy() || svc.resting() {
		t.Error("the server's wait outlasted the account")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.membersAt) != 0 || len(svc.unread) != 0 || svc.waitingAfter != "" || len(svc.retryAt) != 0 || svc.ownLogChecked {
		t.Errorf("kept members %v, unread %v, waiting after %q, retries %v, own log checked %v",
			svc.membersAt, svc.unread, svc.waitingAfter, svc.retryAt, svc.ownLogChecked)
	}
}
