package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"leaflet/internal/store"
)

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

func (s *Service) adopted(ctx context.Context, space, key string) error {
	failed := s.requeueAll(ctx, space)
	if errors.Is(failed, errNotRequeued) {
		return failed
	}
	return errors.Join(failed, s.st.SetValue(key, space))
}

func (s *Service) requeueAll(ctx context.Context, spaceID string) error {
	if err := s.st.SetValue(requeueKey, spaceID); err != nil {
		return fmt.Errorf("%w: %w", errNotRequeued, err)
	}
	ctx, cancel := context.WithTimeout(ctx, moveWait)
	defer cancel()
	var queued error
	pages, err := s.st.AllPages()
	queued = errors.Join(queued, err)
	ofSpace := map[string]bool{}
	for _, p := range pages {
		if p.SpaceID == spaceID {
			ofSpace[p.ID] = true
			queued = errors.Join(queued, s.st.Requeue("pages", p.ID))
		}
	}
	folders, err := s.st.AllFolders()
	queued = errors.Join(queued, err)
	for _, f := range folders {
		if f.SpaceID == spaceID {
			queued = errors.Join(queued, s.st.Requeue("folders", f.ID))
		}
	}
	comments, err := s.st.AllComments()
	queued = errors.Join(queued, err)
	for _, c := range comments {
		if ofSpace[c.PageID] {
			queued = errors.Join(queued, s.st.Requeue("comments", c.ID))
		}
	}
	files, err := s.st.AllFiles()
	queued = errors.Join(queued, err)
	files = slices.DeleteFunc(files, func(f store.File) bool {
		return f.SpaceID != spaceID || f.PageID != "" && !ofSpace[f.PageID]
	})
	recordings, err := s.st.Recordings()
	queued = errors.Join(queued, err)
	recordings = slices.DeleteFunc(recordings, func(r store.Recording) bool { return !ofSpace[r.PageID] })
	var failed error
	moves := map[string]bool{}
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

const requeueKey = "sync.requeue_waiting"

var errNotRequeued = errors.New("not everything is queued to go up")

var moveWait = 2 * time.Minute

const movesKey = "sync.moves_waiting"

func (s *Service) retryMoves(ctx context.Context) error {
	if space, _ := s.st.Value(requeueKey); space != "" {
		return s.requeueAll(ctx, space)
	}
	moves, err := s.ids(movesKey)
	if err != nil || len(moves) == 0 {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, moveWait/4)
	defer cancel()
	var failed error
	left := map[string]bool{}
	keep := func(table string, err error) {
		failed = errors.Join(failed, err)
		for key := range moves {
			if strings.HasPrefix(key, table+":") {
				left[key] = true
			}
		}
	}
	files, err := s.st.AllFiles()
	if err != nil {
		keep("files", err)
	}
	for _, f := range files {
		if key := "files:" + f.ID; moves[key] {
			if err := s.requeueFile(ctx, f, s.st.Session().SpaceID); err != nil {
				left[key] = true
				failed = errors.Join(failed, err)
			}
		}
	}
	recordings, err := s.st.Recordings()
	if err != nil {
		keep("recordings", err)
	}
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
