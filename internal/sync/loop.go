package sync

import (
	"context"
	"errors"
	"log"
	"maps"
	"slices"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
)

const busyWaitMax = 10 * time.Minute

func (s *Service) backOff(err error) {
	re, ok := errors.AsType[*remote.Error](err)
	if !ok || re.RetryAfter <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyUntil = time.Now().Add(min(re.RetryAfter, busyWaitMax))
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.loop, s.stop = ctx, cancel
	s.beat = every{}
	s.mu.Unlock()
	interval := watchEvery
	guard.Go(func() { s.listen(ctx, interval) })
	guard.Go(func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		watch := time.NewTicker(interval)
		defer watch.Stop()
		s.Kick()
		for {
			pull := true
			select {
			case <-ctx.Done():
				return
			case <-s.hear:
				s.checkHeard(ctx)
				continue
			case <-watch.C:
				s.watchOpen(ctx)
				watch.Reset(interval)
				continue
			case <-s.kick:
				select {
				case <-ctx.Done():
					return
				case <-time.After(1200 * time.Millisecond):
				}
				pull = s.wantPull.Swap(false)
			case <-tick.C:
				s.wantPull.Store(false)
			}
			s.loopPass(ctx, pull)
		}
	})
}

func (s *Service) loopPass(loop context.Context, pull bool) {
	defer guard.Recover()
	_ = s.syncNow(loop, true, pull)
}

func (s *Service) Stop() {
	s.mu.Lock()
	stop, cut, up := s.stop, s.cut, s.uploading
	s.loop, s.stop = nil, nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if cut != nil {
		cut()
	}
	if up != nil {
		up.stop(context.Canceled)
	}
}

func (s *Service) passContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loop != nil {
		return s.loop
	}
	return context.Background()
}

func (s *Service) startPass() (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(s.passContext())
	s.mu.Lock()
	s.cut = cancel
	s.mu.Unlock()
	return ctx, func() {
		s.mu.Lock()
		s.cut = nil
		s.mu.Unlock()
		cancel()
	}
}

func (s *Service) Kick() {
	s.wantPull.Store(true)
	s.signal()
}

func (s *Service) KickPush() { s.signal() }

func (s *Service) signal() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

var openWait = time.Minute

var watchEvery = 5 * time.Second

const (
	watchWait = 10 * time.Second
	watchRest = 30 * time.Second
)

func (s *Service) watchOpen(loop context.Context) {
	defer guard.Recover()
	if _, server := s.target().(supabaseTarget); !server || !s.CanSync() || s.resting() {
		return
	}
	open := s.watched()
	s.mu.Lock()
	open = slices.DeleteFunc(open, func(id string) bool { return s.live[id] != nil })
	s.mu.Unlock()
	if len(open) == 0 || !s.running.TryLock() {
		return
	}
	defer s.running.Unlock()
	if loop.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(loop, watchWait)
	defer cancel()
	s.checkPages(loop, ctx, open)
}

func (s *Service) checkHeard(loop context.Context) {
	defer guard.Recover()
	if _, server := s.target().(supabaseTarget); !server || !s.CanSync() || s.resting() {
		s.takeHeard()
		return
	}
	s.running.Lock()
	defer s.running.Unlock()
	heard := s.takeHeard()
	if loop.Err() != nil {
		return
	}
	s.mu.Lock()
	heard = slices.DeleteFunc(heard, func(id string) bool { return !s.open[id] })
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(loop, watchWait)
	defer cancel()
	s.checkPages(loop, ctx, heard)
}

func (s *Service) checkPages(loop, ctx context.Context, ids []string) {
	for _, id := range ids {
		err := s.checkRemote(ctx, id)
		switch {
		case err == nil, unreadable(err):
		case loop.Err() != nil:
			return
		case ofServer(err) || ctx.Err() != nil:
			s.rest(err)
			return
		default:
			log.Printf("check open page %s: %v", id, err)
		}
	}
}

func (s *Service) watched() []string {
	s.mu.Lock()
	open := slices.Sorted(maps.Keys(s.open))
	s.mu.Unlock()
	if s.Watched != nil {
		open = slices.DeleteFunc(open, func(id string) bool { return !s.Watched(id) })
	}
	return open
}

func ofServer(err error) bool {
	_, answered := errors.AsType[*remote.Error](err)
	return answered || remote.IsNetwork(err) || errors.Is(err, remote.ErrSignedOut)
}

func (s *Service) rest(err error) {
	s.backOff(err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchAfter = time.Now().Add(watchRest)
}

func (s *Service) resting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	return now.Before(s.busyUntil) || now.Before(s.watchAfter)
}

func (s *Service) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.busyUntil)
}

func (s *Service) SetOpen(pageID string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !open {
		delete(s.open, pageID)
		return
	}
	s.open[pageID] = true
	guard.Go(func() {
		if !s.CanSync() {
			return
		}
		s.running.Lock()
		defer s.running.Unlock()
		s.mu.Lock()
		stillOpen := s.open[pageID]
		s.mu.Unlock()
		if !stillOpen {
			return
		}
		ctx, cancel := context.WithTimeout(s.passContext(), openWait)
		defer cancel()
		if _, server := s.target().(supabaseTarget); server {
			if p, err := s.st.Page(pageID); err == nil && p.SpaceID == s.st.Session().SpaceID {
				if err := s.LoadMembers(ctx, pageID); err != nil {
					log.Printf("open page %s: members: %v", pageID, err)
				}
			}
		}
		if _, err := s.RefreshContent(ctx, pageID); err != nil {
			log.Printf("open page %s: %v", pageID, err)
		}
	})
}

func (s *Service) CanSync() bool {
	t := s.target()
	if !t.ready() {
		return false
	}
	if s.Offline != nil && s.Offline() {
		return false
	}
	return !t.needsNetwork() || s.Online == nil || s.Online()
}

func (s *Service) Hold(fn func()) {
	s.running.Lock()
	defer s.running.Unlock()
	fn()
}

// passFor is how long one pass may take, and pushFor how much of it the push may, so that a long outbox, as after the
// first sign-in with a large library, leaves the pull its turn.
var passFor, pushFor = 5 * time.Minute, 3 * time.Minute

// stalledPasses is how many passes in a row may run out of time with nothing to show for it before the app is told.
const stalledPasses = 3

// ranOut reports whether err is ctx's own deadline passing, and nothing else that failed around then.
func ranOut(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// errPushRanOut is the push using up its share of a pass that went on to pull.
var errPushRanOut = errors.New("more changes wait to go up")

// progress is what a pass moves when it gets anything done: the changes that wait to go up, and how far each pull
// has come.
func (s *Service) progress() (queued map[outboxKey]bool, cursors map[string]string) {
	entries, _ := s.st.Queued()
	queued = make(map[outboxKey]bool, len(entries))
	for _, e := range entries {
		queued[outboxKey{e.ID, e.Revision}] = true
	}
	cursors = map[string]string{}
	for _, prefix := range []string{"sync.cursor.", "icloud.cursor."} {
		for k, v := range s.st.ValuesWithPrefix(prefix) {
			cursors[prefix+k] = v
		}
	}
	return queued, cursors
}

// progressed reports whether a change that waited has gone up, or a pull has come further, since the pass began.
func (s *Service) progressed(queued map[outboxKey]bool, cursors map[string]string) bool {
	now, at := s.progress()
	for k := range queued {
		if !now[k] {
			return true
		}
	}
	return !maps.Equal(cursors, at)
}

func (s *Service) SyncNow() error { return s.syncNow(context.Background(), false, true) }

func (s *Service) syncNow(loop context.Context, ofLoop, pull bool) (err error) {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
	if loop.Err() != nil {
		return nil
	}
	if ofLoop && s.busy() {
		return nil
	}
	// What the app is told may differ from what the caller is: see below.
	shown, told := error(nil), false
	if s.OnState != nil {
		s.OnState(true, nil)
		defer func() {
			if !told {
				shown = err
			}
			s.OnState(false, shown)
		}()
	}
	defer guard.RecoverAs(&err)
	base, done := s.startPass()
	defer done()
	ctx, cancel := context.WithTimeout(base, passFor)
	defer cancel()
	if err := s.retryMoves(ctx); err != nil {
		log.Printf("sync: %v", err)
	}
	t := s.target()
	queued, cursors := s.progress()
	err = t.pass(ctx, pull)
	if t.needsNetwork() && remote.IsNetwork(err) {
		err = nil
	}
	if err != nil && base.Err() != nil {
		err = nil
	}
	shown, told = err, true
	// A pass that ran out of its time has more to do, which the next one takes up. The caller is told, as not all
	// went up; the app is not, unless pass after pass ends so with nothing to show for it.
	unfinished := errors.Is(err, errPushRanOut)
	switch {
	case !unfinished && !ranOut(ctx, err):
		s.stalled = 0
	case s.progressed(queued, cursors):
		s.stalled, shown = 0, nil
	default:
		if s.stalled++; s.stalled < stalledPasses {
			shown = nil
		}
	}
	if shown != err && !unfinished {
		s.Kick()
	}
	if shown != nil && !errors.Is(shown, remote.ErrSignedOut) {
		log.Printf("sync: %v", shown)
	}
	return err
}

func (s *Service) RefreshContent(ctx context.Context, pageID string) (*doc.Content, error) {
	if !s.CanSync() {
		return s.st.Content(pageID), nil
	}
	return s.target().refresh(ctx, pageID)
}
