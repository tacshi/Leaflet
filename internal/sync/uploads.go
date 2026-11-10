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

var uploadWait = 15 * time.Second

var uploadStall = 2 * time.Minute

var drainWait = 5 * time.Minute

var (
	errUploadStalled = errors.New("the upload stopped: the server took none of it for too long")
	errUploadGone    = errors.New("the upload is no longer wanted")
)

type upload struct {
	entity             string
	stop               context.CancelCauseFunc
	read               atomic.Int64
	done               chan struct{}
	alone, ended, sent bool
	err                error
}

func (s *Service) Drain() error {
	limit := time.NewTimer(drainWait)
	defer limit.Stop()
	var seen *upload
	for {
		err := s.SyncNow()
		s.mu.Lock()
		up := s.lastUpload
		s.mu.Unlock()
		if up == seen {
			return err
		}
		seen = up
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
	s.uploading, s.lastUpload = up, up
	guard.Go(func() { s.watchUpload(ctx, up, session, stall) })
	guard.Go(func() { s.runUpload(ctx, up, uid, e, payload) })
	return up
}

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

func (s *Service) uploadRuns(entity string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uploading != nil && s.uploading.entity == entity
}

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
