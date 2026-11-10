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

func pageTopic(pageID string) string { return "page:" + pageID }

var listenRetry = 5 * time.Second

const (
	listenRetryMax    = 5 * time.Minute
	unauthorizedRetry = 5 * time.Minute
)

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

func (s *Service) listened() []string {
	if _, server := s.target().(supabaseTarget); !server || !s.CanSync() {
		return nil
	}
	return s.watched()
}

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
			s.hearPage(id)
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

func refusedFor(e *remote.RealtimeError) time.Duration {
	if strings.HasPrefix(e.Reason, "Unauthorized") {
		return unauthorizedRetry
	}
	return listenRetry
}

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

func (s *Service) hearPage(id string) {
	s.mu.Lock()
	s.heard[id] = true
	s.mu.Unlock()
	select {
	case s.hear <- struct{}{}:
	default:
	}
}

func (s *Service) takeHeard() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	heard := slices.Sorted(maps.Keys(s.heard))
	clear(s.heard)
	return heard
}

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

func (s *Service) unlive(rt *remote.Realtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, by := range s.live {
		if by == rt {
			delete(s.live, id)
		}
	}
}
