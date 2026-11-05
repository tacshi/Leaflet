package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/remote"
)

// pageTopic is the Realtime channel the saves of a page's content are
// told on (protocol §6).
func pageTopic(pageID string) string { return "page:" + pageID }

// listenRetry is how long listen first waits to connect to Realtime again
// after it could not, doubling each time up to listenRetryMax, and how
// soon it joins again a page's channel Realtime closed or refused for the
// moment. Tests shorten it.
var listenRetry = 5 * time.Second

const (
	listenRetryMax = 5 * time.Minute
	// unauthorizedRetry is how long a page's channel Realtime refused the
	// person, as one whose project's database does not say who may hear
	// it, waits to be joined again; the page is checked every few seconds
	// meanwhile.
	unauthorizedRetry = 5 * time.Minute
)

// listen keeps the open pages someone sees (Watched) joined to their
// channels on the project's Realtime while the loop runs, whose context
// is loop, looking again every interval: a save of one on the server has
// the loop check it at once (checkHeard), and the checks every few
// seconds leave it out (watchOpen). Pages it could not join, and all of
// them while it can't connect, those checks keep to.
func (s *Service) listen(loop context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	wait := listenRetry
	var after time.Time
	for {
		if time.Now().After(after) && len(s.listened()) > 0 {
			joined, err := s.listenOn(loop, tick)
			if joined {
				wait = listenRetry
			}
			if err != nil && loop.Err() == nil {
				log.Printf("realtime: %v", err)
				after = time.Now().Add(wait)
				wait = min(2*wait, listenRetryMax)
			}
		}
		select {
		case <-loop.Done():
			return
		case <-tick.C:
		}
	}
}

// listened are the open pages to listen for saves of: those someone sees,
// while sync goes through the server and can.
func (s *Service) listened() []string {
	if _, server := s.target().(supabaseTarget); !server || !s.CanSync() {
		return nil
	}
	return s.watched()
}

// listenOn is one connection to Realtime, kept while there are pages to
// listen for, as the session it was made for: it joins their channels as
// they come and leaves them as they go, and has a page checked once its
// channel is joined, for a save made before. It reports whether it joined
// one, and why the connection ended: nil for nothing left to listen for,
// another session, or the loop's end.
func (s *Service) listenOn(loop context.Context, tick *time.Ticker) (joinedAny bool, err error) {
	defer guard.RecoverAs(&err)
	session := s.be.Sessions()
	ctx, cancel := context.WithTimeout(loop, watchWait)
	rt, err := s.be.DialRealtime(ctx)
	cancel()
	if err != nil {
		return false, err
	}
	defer rt.Close()
	defer s.unlive(rt)
	joined := map[string]bool{}
	// again is when a page whose channel was refused, or closed by the
	// server, is joined again.
	again := map[string]time.Time{}
	for {
		want := s.listened()
		if len(want) == 0 || s.be.Sessions() != session {
			return joinedAny, nil
		}
		for id := range joined {
			if !slices.Contains(want, id) {
				rt.Leave(pageTopic(id))
				delete(joined, id)
				s.markLive(id, rt, false)
			}
		}
		for _, id := range want {
			if joined[id] || time.Now().Before(again[id]) {
				continue
			}
			ctx, cancel := context.WithTimeout(loop, watchWait)
			err := rt.Join(ctx, pageTopic(id))
			cancel()
			if refused, ok := errors.AsType[*remote.RealtimeError](err); ok {
				log.Printf("realtime: page %s: %v", id, err)
				again[id] = time.Now().Add(refusedFor(refused))
				continue
			}
			if err != nil {
				return joinedAny, err
			}
			joined[id], joinedAny = true, true
			s.markLive(id, rt, true)
			s.hearPage(id) // what was saved before the channel heard it
		}
		select {
		case <-loop.Done():
			return joinedAny, nil
		case <-tick.C:
		case e, ok := <-rt.Events():
			if !ok {
				return joinedAny, rt.Err()
			}
			id, isPage := strings.CutPrefix(e.Topic, "page:")
			switch {
			case !isPage || !joined[id]:
			case e.Closed:
				delete(joined, id)
				s.markLive(id, rt, false)
				again[id] = time.Now().Add(listenRetry)
			case e.Event == "saved":
				s.heardSave(id, e.Payload)
			}
		}
	}
}

// refusedFor is how long a page whose channel Realtime refused waits to be
// joined again: one the person may not hear a while, one refused for the
// moment, as by a project's Realtime still waking (MissingPartition), soon.
func refusedFor(e *remote.RealtimeError) time.Duration {
	if strings.HasPrefix(e.Reason, "Unauthorized") {
		return unauthorizedRetry
	}
	return listenRetry
}

// heardSave has page id checked for the save Realtime told of, unless this
// device has merged that revision already, as one it saved itself.
func (s *Service) heardSave(id string, payload json.RawMessage) {
	var saved struct {
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(payload, &saved) == nil {
		if at, revision, err := s.st.RemoteAt(id); err == nil && !at.IsZero() && revision == saved.Revision {
			return
		}
	}
	s.hearPage(id)
}

// hearPage has the loop check page id at once (checkHeard).
func (s *Service) hearPage(id string) {
	s.mu.Lock()
	s.heard[id] = true
	s.mu.Unlock()
	select {
	case s.hear <- struct{}{}:
	default:
	}
}

// takeHeard are the pages heard of, in order, which it forgets.
func (s *Service) takeHeard() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	heard := slices.Sorted(maps.Keys(s.heard))
	clear(s.heard)
	return heard
}

// markLive marks page id as told of by rt's channel, or no longer, unless
// another connection's channel tells of it.
func (s *Service) markLive(id string, rt *remote.Realtime, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case on:
		s.live[id] = rt
	case s.live[id] == rt:
		delete(s.live, id)
	}
}

// unlive unmarks the pages rt's channels told of, as it closes.
func (s *Service) unlive(rt *remote.Realtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, by := range s.live {
		if by == rt {
			delete(s.live, id)
		}
	}
}
