package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"time"

	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

type outboxKey struct{ id, revision int64 }

var retryBase, retryMax = 15 * time.Second, 30 * time.Minute

func backoff(attempts int) time.Duration {
	return min(retryBase<<min(max(attempts-1, 0), 16), retryMax)
}

func (s *Service) due(e store.OutboxEntry) bool {
	if e.Attempts == 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.retryAt[outboxKey{e.ID, e.Revision}]
	return !ok || !time.Now().Before(at)
}

func (s *Service) failed(e store.OutboxEntry, err error) {
	_ = s.st.MarkFailed(e, err.Error())
	wait := backoff(e.Attempts + 1)
	if remote.IsInvalid(err) {
		wait = retryMax
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAt[outboxKey{e.ID, e.Revision}] = time.Now().Add(wait)
}

func (s *Service) forgetRetries(entries []store.OutboxEntry) {
	queued := make(map[outboxKey]bool, len(entries))
	for _, e := range entries {
		queued[outboxKey{e.ID, e.Revision}] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.retryAt, func(k outboxKey, _ time.Time) bool { return !queued[k] })
}

func (s *Service) contentsWait(e store.OutboxEntry) bool {
	return e.Entity == "files" && s.st.HasPending("storage", "files/"+e.EntityID)
}

func (s *Service) removal(payload map[string]any) (waits, stays bool) {
	id := store.S(payload, "file_id")
	if s.st.HasPending("files", id) || s.uploadRuns("files/"+id) || s.copyWaits(store.S(payload, "bucket"), store.S(payload, "path")) {
		return true, false
	}
	f, err := s.st.File(id)
	return false, err == nil && f.DeletedAt.IsZero()
}

func (s *Service) copyWaits(bucket, path string) bool {
	entries, err := s.st.Queued()
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Entity != "storage" {
			continue
		}
		payload, err := s.payload(&e)
		if err != nil {
			return true
		}
		if store.S(payload, "bucket") == bucket && store.S(payload, "from_path") == path {
			return true
		}
	}
	return false
}

func (s *Service) payload(e *store.OutboxEntry) (map[string]any, error) {
	if e.Payload == "" {
		if queued, err := s.st.LoadPayload(e); err != nil || !queued {
			return nil, err
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Service) uploadFrom(ctx context.Context, payload map[string]any) (local string, waits bool, err error) {
	if local := s.st.Path(store.S(payload, "local_path")); store.OnDevice(local) {
		return local, false, nil
	}
	local, err = s.fetchCopied(ctx, payload)
	if errors.Is(err, icloud.ErrNotReady) {
		return "", true, nil
	}
	return local, false, err
}

func (s *Service) fetchCopied(ctx context.Context, payload map[string]any) (string, error) {
	from := store.S(payload, "from_path")
	if from == "" {
		return "", nil
	}
	data, err := s.fetch(ctx, store.S(payload, "bucket"), from, false)
	if err == nil {
		defer func() { _ = data.Close() }()
	}
	if remote.IsNotFound(err) || errors.Is(err, fs.ErrNotExist) {
		if s.cloudOn.Load() && s.originalLives(payload) {
			return "", fmt.Errorf("%w: what this copies is not in iCloud Drive yet", icloud.ErrNotReady)
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var local string
	if recID := store.S(payload, "recording_id"); recID != "" {
		local, err = s.st.DownloadedAudioFrom(recID, data)
	} else {
		local, err = s.st.DownloadedFileFrom(store.S(payload, "file_id"), data)
	}
	if store.IsNotFound(err) {
		return "", nil
	}
	return local, err
}

func (s *Service) uploadedAudio(recID, path, local string, at time.Time) (store.Recording, bool, error) {
	rec, err := s.st.UpdateRecording(recID, func(r *store.Recording) { r.AudioPath, r.UploadedAt = path, at })
	if store.IsNotFound(err) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	if s.KeepAudio != nil && s.KeepAudio() == "until_uploaded" {
		_ = os.Remove(local)
		_ = s.st.ClearLocalPath(recID)
	}
	return rec, true, nil
}
