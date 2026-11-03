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

// outboxKey names an outbox entry as queued: a new payload is a new key.
type outboxKey struct{ id, revision int64 }

// retryBase is how long an outbox entry that failed waits before it goes
// again, doubling with each failure up to retryMax: a pass runs after
// every save, and an entry the server keeps refusing would otherwise go
// with each of them. Tests shorten them.
var retryBase, retryMax = 15 * time.Second, 30 * time.Minute

// backoff is how long an entry waits after its attempts-th failure.
func backoff(attempts int) time.Duration {
	return min(retryBase<<min(max(attempts-1, 0), 16), retryMax)
}

// due reports whether an outbox entry may go now: it never failed, or
// waited long enough since it last did.
func (s *Service) due(e store.OutboxEntry) bool {
	if e.Attempts == 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.retryAt[outboxKey{e.ID, e.Revision}]
	return !ok || !time.Now().Before(at)
}

// failed records an outbox entry's failure, and when it may go again. One
// the server refuses for good (remote.IsInvalid), as a file larger than
// it takes, waits the longest at once: sent again soon, it fails again,
// and an upload goes whole each time.
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

// forgetRetries drops the waits of entries no longer queued as they were.
func (s *Service) forgetRetries(entries []store.OutboxEntry) {
	queued := make(map[outboxKey]bool, len(entries))
	for _, e := range entries {
		queued[outboxKey{e.ID, e.Revision}] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.retryAt, func(k outboxKey, _ time.Time) bool { return !queued[k] })
}

// contentsWait reports whether an entry is a file's row that waits for
// its contents, queued to go up first: a device that has the row can
// fetch them, and never takes a file still uploading for one that is
// gone. One whose upload waits, as for what a copy was made from, waits
// with it.
func (s *Service) contentsWait(e store.OutboxEntry) bool {
	return e.Entity == "files" && s.st.HasPending("storage", "files/"+e.EntityID)
}

// removal tells what becomes of a deleted file's stored contents, queued
// to go (storage_delete). They wait until the row that says the file is
// deleted has gone, as till then the file is still there for the other
// devices, and while a copy of it waits to go up from them (copyWaits).
// They stay for good when the file lives here, as when its deletion gave
// way to another's row: the entry is done then.
func (s *Service) removal(payload map[string]any) (waits, stays bool) {
	id := store.S(payload, "file_id")
	if s.st.HasPending("files", id) || s.copyWaits(store.S(payload, "bucket"), store.S(payload, "path")) {
		return true, false
	}
	f, err := s.st.File(id)
	return false, err == nil && f.DeletedAt.IsZero()
}

// copyWaits reports whether a copy's upload still waits to read a deleted
// file's stored contents (fetchCopied): until it has, they stay, as the
// copy would go up with nothing in it. One it cannot tell of waits too.
func (s *Service) copyWaits(bucket, path string) bool {
	entries, err := s.st.Pending()
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Entity != "storage" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(e.Payload), &payload) == nil &&
			store.S(payload, "bucket") == bucket && store.S(payload, "from_path") == path {
			return true
		}
	}
	return false
}

// uploadFrom is where on this device the file or audio a storage entry
// uploads is: its own, or, for a copy whose contents are not here, what it
// was made from, brought here first (fetchCopied). It is "" when there is
// nothing to upload, as what the copy was made from is gone. waits is true
// while that is in iCloud Drive but not downloaded yet: the entry stays,
// and goes on a later pass once iCloud has it, rather than hold this one
// up.
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

// fetchCopied brings to this device the file or audio a copy was made
// from, which goes up again as the copy's own: only those who can view
// the page of the row a stored copy belongs to can read it. It returns
// where the copy is kept here now, or "" when there is nothing to copy:
// the copy had what it needs, or what it copies or the copy is gone.
// Copies are made from what is on the device now (store.CopyNeeds): only
// one an earlier version queued still comes from the original's. One in
// iCloud Drive that iCloud has not downloaded yet is icloud.ErrNotReady:
// iCloud is asked for it, and not waited for.
func (s *Service) fetchCopied(ctx context.Context, payload map[string]any) (string, error) {
	from := store.S(payload, "from_path")
	if from == "" {
		return "", nil
	}
	data, err := s.fetch(ctx, store.S(payload, "bucket"), from, false)
	if remote.IsNotFound(err) || errors.Is(err, fs.ErrNotExist) {
		// iCloud Drive may not list it yet, as while the device that has
		// it still uploads it: it is waited for while the original lives.
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
		local, err = s.st.DownloadedAudio(recID, data)
	} else {
		local, err = s.st.DownloadedFile(store.S(payload, "file_id"), data)
	}
	if store.IsNotFound(err) {
		return "", nil
	}
	return local, err
}

// uploadedAudio records that a recording's audio, local here, went up to
// path at at, and reports false when the recording went meanwhile. The
// upload waits until the recording has its path: else nothing would send
// it again, and the other devices could not play it. Audio kept only until
// it is uploaded goes from this device then: the page plays it from where
// it went.
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
