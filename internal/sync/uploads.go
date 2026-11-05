package sync

import (
	"context"
	"errors"
	"io"
	"log"
	"sync/atomic"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// An upload through the server takes as long as the connection does,
// which may be hours for a recording: longer than a pass may run, and it
// starts again from nothing each time it is stopped. So it goes on beside
// the passes, one at a time. A push waits for it a little, so a small one
// and its row go together; past that the push and the pull go on, and the
// upload settles its entry itself as it ends, asking for a pass.

// uploadWait is how long a push waits for an upload it started. Tests
// shorten it.
var uploadWait = 15 * time.Second

// uploadStall is how long an upload goes on with the server taking none
// of it. Tests shorten it.
var uploadStall = 2 * time.Minute

// drainWait is the most Drain waits for uploads. Tests shorten it.
var drainWait = 5 * time.Minute

var (
	// errUploadStalled is an upload the server stopped taking.
	errUploadStalled = errors.New("the upload stopped: the server took none of it for too long")
	// errUploadGone is an upload no longer wanted, as its file was deleted
	// or the person signed out.
	errUploadGone = errors.New("the upload is no longer wanted")
)

// upload is an upload under way.
type upload struct {
	// entity is its outbox entry's ID.
	entity string
	// stop ends it, for the reason it is given.
	stop context.CancelCauseFunc
	// read is when it last read some of the file (ms), 0 before it has.
	read atomic.Int64
	// done is closed as it ends.
	done chan struct{}
	// alone says no push waits for it; ended says it ended, sent whether
	// its entry can leave the queue, and err why not. Guarded by the
	// service's mu.
	alone, ended, sent bool
	err                error
}

// Drain sends what waits to go up, as signing out does before it counts
// what has not: a pass, and for an upload that goes on past the wait of
// its push, up to drainWait for it to end, and another pass for what waited
// for it, as its file's row. An upload that ends without its file gone up,
// as one the server stopped taking (uploadStall), ends the wait: what is
// still queued then is for the caller to count.
func (s *Service) Drain() error {
	limit := time.NewTimer(drainWait)
	defer limit.Stop()
	for {
		err := s.SyncNow()
		s.mu.Lock()
		up := s.uploading
		s.mu.Unlock()
		if up == nil {
			return err
		}
		select {
		case <-up.done:
		case <-limit.C:
			return err
		}
		if up.err != nil || !up.sent {
			return err
		}
	}
}

// pushUpload sends a storage entry, and reports whether it can leave the
// queue, once the upload ended: while one goes on, this entry and any
// other stay queued, and the upload settles its own (uploadEnded).
func (s *Service) pushUpload(ctx context.Context, uid string, e store.OutboxEntry, payload map[string]any) (bool, error) {
	up := s.startUpload(uid, e, payload)
	if up == nil {
		return false, nil
	}
	wait := time.NewTimer(uploadWait)
	defer wait.Stop()
	select {
	case <-up.done:
	case <-wait.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !up.ended {
		up.alone = true
		return false, ctx.Err()
	}
	if errors.Is(up.err, errUploadGone) {
		return false, nil
	}
	return up.sent, up.err
}

// startUpload starts sending a storage entry, or returns nil while another
// upload goes on. Stop ends it, as it ends a pass.
func (s *Service) startUpload(uid string, e store.OutboxEntry, payload map[string]any) *upload {
	session, stall := s.be.Sessions(), uploadStall
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploading != nil {
		return nil
	}
	base := s.loop
	if base == nil {
		base = context.Background()
	}
	ctx, stop := context.WithCancelCause(base)
	up := &upload{entity: e.EntityID, stop: stop, done: make(chan struct{})}
	s.uploading = up
	guard.Go(func() { s.watchUpload(ctx, up, session, stall) })
	guard.Go(func() { s.runUpload(ctx, up, uid, e, payload) })
	return up
}

// runUpload sends an upload to its end, for the push that waits for it,
// or, with none left waiting, settling its entry itself.
func (s *Service) runUpload(ctx context.Context, up *upload, uid string, e store.OutboxEntry, payload map[string]any) {
	var sent bool
	var err error
	defer func() {
		if cause := context.Cause(ctx); err != nil && cause != nil {
			err = cause
		}
		up.stop(nil)
		s.mu.Lock()
		up.ended, up.sent, up.err = true, sent, err
		alone := up.alone
		s.mu.Unlock()
		// Settled before another may start, which would send it again.
		if alone {
			s.uploadEnded(e, sent, err)
		}
		s.mu.Lock()
		s.uploading = nil
		s.mu.Unlock()
		close(up.done)
		if alone {
			s.KickPush()
		}
	}()
	defer guard.RecoverAs(&err)
	sent, err = s.pushFile(ctx, uid, payload, &up.read)
}

// uploadEnded settles the entry of an upload no push waited for, as a push
// does (push). A pass then takes what waited for it, as a file's row, or
// the next upload.
func (s *Service) uploadEnded(e store.OutboxEntry, sent bool, err error) {
	switch {
	case err == nil && sent:
		_ = s.st.Dequeue(e)
	case err == nil, errors.Is(err, errUploadGone), errors.Is(err, context.Canceled):
	case remote.IsNetwork(err), remote.IsBusy(err), errors.Is(err, remote.ErrSignedOut):
		s.backOff(err)
	default:
		log.Printf("outbox %s/%s: %v", e.Entity, e.EntityID, err)
		s.failed(e, err)
	}
}

// watchUpload ends an upload the server stopped taking, which would
// otherwise wait for the connection to fail, however long that takes, or
// no longer wanted: its entry went, sync can't go now, or the session it
// began in ended.
func (s *Service) watchUpload(ctx context.Context, up *upload, session uint64, stall time.Duration) {
	tick := time.NewTicker(stall / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if read := up.read.Load(); read != 0 && time.Since(time.UnixMilli(read)) >= stall {
			up.stop(errUploadStalled)
			return
		}
		if s.be.Sessions() != session || !s.CanSync() || !s.st.HasPending("storage", up.entity) {
			up.stop(errUploadGone)
			return
		}
	}
}

// uploadRuns reports whether a storage entry's upload is under way.
func (s *Service) uploadRuns(entity string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uploading != nil && s.uploading.entity == entity
}

// keyed has a file's upload go under a key Storage takes: one an earlier
// version queued may have the file's name as it was, which Storage refuses
// every time. It returns the entry's payload as it is now, nil for a file
// that is gone.
func (s *Service) keyed(entry *store.OutboxEntry, payload map[string]any) (map[string]any, error) {
	id := store.S(payload, "file_id")
	if id == "" || store.ValidStorageKey(store.S(payload, "path")) {
		return payload, nil
	}
	if _, err := s.st.RekeyFile(id); store.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entry.Payload = ""
	return s.payload(entry)
}

// timedReader notes when a file an upload reads was last read.
type timedReader struct {
	r  io.Reader
	at *atomic.Int64
}

func (t timedReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.at.Store(time.Now().UnixMilli())
	}
	return n, err
}
