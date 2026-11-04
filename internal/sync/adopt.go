package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"leaflet/internal/store"
)

// moveInto has this device sync the pages of space, the space of the
// library it joins, as signing in or joining the Leaflet folder of iCloud
// Drive does: the pages made on this device move into it, and what is
// made from here on goes there too, its own space gone. A library another
// device started (joined) has starter pages of its own: this device's
// stay out of it, unless the user took them up.
func (s *Service) moveInto(space, name, owner string, joined bool) error {
	if err := s.st.SettleStarterPages(joined); err != nil {
		return err
	}
	local := s.st.Session().SpaceID
	if local == space {
		return nil
	}
	if err := s.st.PutSpace(space, name, owner); err != nil {
		return err
	}
	if err := s.st.MoveSpace(local, space); err != nil {
		return err
	}
	if err := s.st.SetValue("space.local", space); err != nil {
		return err
	}
	sess := s.st.Session()
	sess.SpaceID = space
	s.st.SetSession(sess)
	return nil
}

// adopted queues everything on this device to go up in space, which this
// device joined, and keeps it under key once it has: the space counts as
// adopted only then. A device that stops before, or could not queue it
// all, adopts it again on the next pass.
func (s *Service) adopted(ctx context.Context, space, key string) error {
	failed := s.requeueAll(ctx, space)
	if errors.Is(failed, errNotRequeued) {
		return failed
	}
	return errors.Join(failed, s.st.SetValue(key, space))
}

// requeueAll queues everything on this device to go up, now in the space
// spaceID: the pages and their content, folders, comments, files and
// recordings with their audio. A file or audio this device has only in
// iCloud Drive, as after syncing through it, comes here first, to go up
// from here; one that does not come yet waits, and retryMoves tries it
// again before each pass.
//
// What it could not read or queue it does all again before the next pass
// (requeueKey), so nothing is left behind for good. It waits for iCloud
// Drive no longer than ctx lets it, so stopping the pass stops it too.
func (s *Service) requeueAll(ctx context.Context, spaceID string) error {
	if err := s.st.SetValue(requeueKey, spaceID); err != nil {
		return fmt.Errorf("%w: %w", errNotRequeued, err)
	}
	ctx, cancel := context.WithTimeout(ctx, moveWait)
	defer cancel()
	// Each row goes as stored when it is queued, not as read here: a
	// change made meanwhile, as a deletion, is not undone.
	var queued error
	pages, err := s.st.AllPages()
	queued = errors.Join(queued, err)
	for _, p := range pages {
		queued = errors.Join(queued, s.st.Requeue("pages", p.ID))
	}
	folders, err := s.st.AllFolders()
	queued = errors.Join(queued, err)
	for _, f := range folders {
		queued = errors.Join(queued, s.st.Requeue("folders", f.ID))
	}
	comments, err := s.st.AllComments()
	queued = errors.Join(queued, err)
	for _, c := range comments {
		queued = errors.Join(queued, s.st.Requeue("comments", c.ID))
	}
	files, err := s.st.AllFiles()
	queued = errors.Join(queued, err)
	recordings, err := s.st.Recordings()
	queued = errors.Join(queued, err)
	var failed error
	moves := map[string]bool{}
	// Deleted files go too, as deleted pages do, so a deletion sent in a
	// batch that was lost goes again: its stored copy may be gone.
	for _, f := range files {
		if err := s.requeueFile(ctx, f, spaceID); err != nil {
			moves["files:"+f.ID] = true
			failed = errors.Join(failed, err)
		}
	}
	for _, r := range recordings {
		if err := s.requeueRecording(ctx, r); err != nil {
			moves["recordings:"+r.ID] = true
			failed = errors.Join(failed, err)
		}
	}
	queued = errors.Join(queued, s.setIDs(movesKey, moves))
	if queued != nil {
		return errors.Join(failed, fmt.Errorf("%w: %w", errNotRequeued, queued))
	}
	return errors.Join(failed, s.st.SetValue(requeueKey, ""))
}

// requeueKey keeps the space requeueAll is queuing everything in, until it
// has: one it could not finish runs again before the next pass.
const requeueKey = "sync.requeue_waiting"

// errNotRequeued is a requeueAll that did not queue everything, and runs
// again before the next pass.
var errNotRequeued = errors.New("not everything is queued to go up")

// moveWait is how long requeueAll waits for iCloud Drive to bring what
// this device has only there; a retry waits a quarter of it. Tests
// shorten it.
var moveWait = 2 * time.Minute

// movesKey keeps the files and recordings requeueAll could not bring
// from iCloud Drive yet, as "files:<id>" and "recordings:<id>".
const movesKey = "sync.moves_waiting"

// retryMoves queues the files and recordings requeueAll could not bring
// from iCloud Drive, once they come, and runs again a requeueAll that did
// not finish.
func (s *Service) retryMoves(ctx context.Context) error {
	if space, _ := s.st.Value(requeueKey); space != "" {
		return s.requeueAll(ctx, space)
	}
	moves := s.ids(movesKey)
	if len(moves) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, moveWait/4)
	defer cancel()
	var failed error
	left := map[string]bool{}
	files, _ := s.st.Files()
	for _, f := range files {
		if key := "files:" + f.ID; moves[key] {
			if err := s.requeueFile(ctx, f, s.st.Session().SpaceID); err != nil {
				left[key] = true
				failed = errors.Join(failed, err)
			}
		}
	}
	recordings, _ := s.st.Recordings()
	for _, r := range recordings {
		if key := "recordings:" + r.ID; moves[key] {
			if err := s.requeueRecording(ctx, r); err != nil {
				left[key] = true
				failed = errors.Join(failed, err)
			}
		}
	}
	return errors.Join(failed, s.setIDs(movesKey, left))
}

// requeueFile queues a file to go up in space spaceID, with its contents
// when this device has them (a deleted file's row goes alone). It changes
// nothing while the file is in iCloud Drive but does not come here
// (fromICloud). What it queues is the file as stored once it has come, as
// a change made while it waited for it is not undone.
func (s *Service) requeueFile(ctx context.Context, f store.File, spaceID string) error {
	if f.DeletedAt.IsZero() {
		data, err := s.fromICloud(ctx, "files", f.StoragePath, f.LocalPath)
		if err != nil {
			return fmt.Errorf("file %s: %w", f.Name, err)
		}
		if data != nil {
			_, err := s.st.DownloadedFileFrom(f.ID, data)
			_ = data.Close()
			if err != nil && !store.IsNotFound(err) {
				return err
			}
		}
	}
	return s.st.RequeueFile(f.ID, spaceID)
}

// requeueRecording queues a finished recording to go up with its audio,
// which keeps the path it has until the upload gives it another. It
// changes nothing while the audio is in iCloud Drive but does not come
// here, as requeueFile does a file.
func (s *Service) requeueRecording(ctx context.Context, r store.Recording) error {
	if r.Status != "ready" && r.Status != "failed" {
		return nil
	}
	data, err := s.fromICloud(ctx, "recordings", r.AudioPath, r.LocalPath)
	if err != nil {
		return fmt.Errorf("recording %s: %w", r.ID, err)
	}
	if data != nil {
		_, err := s.st.DownloadedAudioFrom(r.ID, data)
		_ = data.Close()
		if err != nil && !store.IsNotFound(err) {
			return err
		}
	}
	return s.st.RequeueRecording(r.ID)
}

// fromICloud opens a file or audio, by its storage path, that this device
// has only in the Leaflet folder of iCloud Drive it synced through, so it
// comes here to go up elsewhere, read as it comes; it is nil when nothing
// has to come, and the caller closes it otherwise. One that went up there
// but is not listed yet, as while the device that has it still copies it
// there, is errNotInICloud, and waits: queued now, it would go up with a
// path of its own and nothing stored there. While sync goes through
// iCloud Drive, as when a device sends everything again there, it stays
// where the other devices read it: waiting for it would hold up the pass
// for nothing.
func (s *Service) fromICloud(ctx context.Context, bucket, storagePath, local string) (io.ReadCloser, error) {
	if s.cloudOn.Load() || store.OnDevice(local) || storagePath == "" || !s.joinedICloud() {
		return nil, nil
	}
	if !s.keptInICloud(bucket, storagePath) {
		return nil, errNotInICloud
	}
	data, err := s.cloudFetch(ctx, bucket, storagePath)
	if err != nil {
		return nil, fmt.Errorf("from iCloud Drive: %w", err)
	}
	return data, nil
}
