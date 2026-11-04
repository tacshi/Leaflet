package sync

import (
	"context"
	"errors"
	"log"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
)

// busyWaitMax is the most a Retry-After holds the loop back.
const busyWaitMax = 10 * time.Minute

// backOff keeps the loop's passes from the server for as long as an
// answer that it is busy asks, no longer than busyWaitMax.
func (s *Service) backOff(err error) {
	re, ok := errors.AsType[*remote.Error](err)
	if !ok || re.RetryAfter <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyUntil = time.Now().Add(min(re.RetryAfter, busyWaitMax))
}

// Start runs the loop: soon after changes, when asked, and every 30 seconds.
// The first pass after says the device is in use, and looks for what is
// looked for every so often, at once.
func (s *Service) Start() {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.loop, s.stop = ctx, cancel
	s.beat, s.providersSwept = every{}, every{}
	s.mu.Unlock()
	guard.Go(func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		s.Kick()
		for {
			pull := true
			select {
			case <-ctx.Done():
				return
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

// loopPass is a pass of the loop's, which loop ends, pulling with pull: one
// that panics is logged, and the loop goes on to the next.
func (s *Service) loopPass(loop context.Context, pull bool) {
	defer guard.Recover()
	_ = s.syncNow(loop, true, pull)
}

// Stop ends the loop Start started, and the pass running, the loop's or
// one SyncNow asked for, as an iOS app's background time runs out: what
// that pass had not done yet, the next one does. Passes asked for with
// SyncNow after it still run, and Start starts the loop again, as an iOS
// app does when its scene goes to the background and comes back.
func (s *Service) Stop() {
	s.mu.Lock()
	stop, cut := s.stop, s.cut
	s.loop, s.stop = nil, nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if cut != nil {
		cut()
	}
}

// passContext is the context a pass runs in: the loop's, which Stop
// cancels, or the background's while no loop runs.
func (s *Service) passContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loop != nil {
		return s.loop
	}
	return context.Background()
}

// startPass is the context of a pass starting, which Stop cancels until
// done ends the pass.
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

// Kick asks for a pass soon, which sends what changed here and brings what
// changed elsewhere.
func (s *Service) Kick() {
	s.wantPull.Store(true)
	s.signal()
}

// KickPush asks for a pass soon that sends what changed here, as after a
// save: what changed elsewhere comes with the passes every 30 seconds, so
// typing does not make the device ask the server for every table after
// each pause. A Kick before the pass runs has it bring that too.
func (s *Service) KickPush() { s.signal() }

func (s *Service) signal() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// openWait is how long SetOpen has to bring an opened page up to date;
// tests shorten it.
var openWait = time.Minute

// SetOpen marks a page as open, so its content is always pulled, and
// brings an opened page up to date in the background.
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
		// The time it has starts once the pass in progress is done, and
		// Stop ends it, as it ends a pass. A page closed meanwhile, as one
		// passed over on the way to another, needs nothing: it holds up
		// the passes for nothing.
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
		// Who else has one of the user's pages is known before what they
		// wrote in it comes, for the agents that run on it.
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

// CanSync reports whether a pass can run: signed in and online, or with
// iCloud sync on, and not working offline. iCloud Drive keeps what Leaflet
// writes to it until it is online itself.
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

// Hold runs fn between passes, so no pass sees its changes half made.
func (s *Service) Hold(fn func()) {
	s.running.Lock()
	defer s.running.Unlock()
	fn()
}

// SyncNow runs one pass, after the one in progress. A pass that panics
// is logged and fails, so it is never taken for one still running.
func (s *Service) SyncNow() error { return s.syncNow(context.Background(), false, true) }

// syncNow is SyncNow, which brings what changed elsewhere only with pull;
// ofLoop says it is a pass of the loop whose context is loop: one that
// waited for another, or for SetOpen, FlushPage or Hold, to be done runs
// only if the loop still does, as Stop ended it.
func (s *Service) syncNow(loop context.Context, ofLoop, pull bool) (err error) {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
	if loop.Err() != nil {
		return nil
	}
	// The loop leaves a server that asked for a wait alone till then; a
	// pass asked for, as the user's, still goes.
	s.mu.Lock()
	busy := time.Now().Before(s.busyUntil)
	s.mu.Unlock()
	if ofLoop && busy {
		return nil
	}
	if s.OnState != nil {
		s.OnState(true, nil)
		defer func() { s.OnState(false, err) }()
	}
	defer guard.RecoverAs(&err)
	base, done := s.startPass()
	defer done()
	ctx, cancel := context.WithTimeout(base, 5*time.Minute)
	defer cancel()
	if err := s.retryMoves(ctx); err != nil {
		log.Printf("sync: %v", err)
	}
	t := s.target()
	err = t.pass(ctx, pull)
	if t.needsNetwork() && remote.IsNetwork(err) {
		err = nil // offline: try again later
	}
	if err != nil && base.Err() != nil {
		err = nil // stopped: the next pass does the rest
	}
	if err != nil && !errors.Is(err, remote.ErrSignedOut) {
		log.Printf("sync: %v", err)
	}
	return err
}

// RefreshContent brings a page's content up to date with the other
// devices', merging unsynced edits made here.
func (s *Service) RefreshContent(ctx context.Context, pageID string) (*doc.Content, error) {
	if !s.CanSync() {
		return s.st.Content(pageID), nil
	}
	return s.target().refresh(ctx, pageID)
}

// FlushPage pushes a page's pending changes right away, so a cloud agent
// starts from what the user sees; for "", all of them, as a workspace
// agent's run reads all the user's pages.
func (s *Service) FlushPage(ctx context.Context, pageID string) error {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
	return s.target().flush(ctx, pageID)
}
