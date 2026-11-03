package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"slices"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func (s *Service) push(ctx context.Context) error {
	entries, err := s.st.Pending()
	if err != nil {
		return err
	}
	s.forgetRetries(entries)
	uid := s.be.UserID()
	// A file's row waits for its contents (contentsWait), and a row for the
	// page or folder it is in (waitsForParent), which come later in the
	// queue: it goes once they have, in the same pass.
	var held []store.OutboxEntry
	for range 2 {
		for _, e := range entries {
			if !s.CanSync() {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !s.due(e) {
				continue
			}
			done, err := s.pushOne(ctx, uid, e)
			if err != nil {
				// Offline, signed out, stopped, or the server failing or
				// asking for a wait: the rest would fail alike, and none of
				// it is the entry's to count against it, which would leave
				// it waiting long after the server is back. Anything else
				// is this entry's own, which waits while the others go.
				if remote.IsNetwork(err) || remote.IsBusy(err) || errors.Is(err, remote.ErrSignedOut) || ctx.Err() != nil {
					s.backOff(err)
					return err
				}
				log.Printf("outbox %s/%s: %v", e.Entity, e.EntityID, err)
				s.failed(e, err)
				continue
			}
			if done {
				_ = s.st.Dequeue(e)
			} else if e.Entity == "files" || e.Entity == "pages" || e.Entity == "folders" {
				held = append(held, e)
			}
		}
		if len(held) == 0 {
			break
		}
		entries, held = held, nil
	}
	return nil
}

// pushOne sends one entry and reports whether it can leave the queue.
func (s *Service) pushOne(ctx context.Context, uid string, e store.OutboxEntry) (bool, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return false, err
	}
	switch e.Entity {
	case "page_content":
		done, err := s.pushContent(ctx, e.EntityID)
		if errors.Is(err, errCannotEdit) {
			return s.takeServerContent(ctx, e, err)
		}
		return done, err
	case "storage":
		return s.pushFile(ctx, uid, payload)
	case "storage_delete":
		return s.removeStored(ctx, payload)
	case "pages":
		if payload["created_by"] == nil {
			payload["created_by"] = uid
		}
		return s.pushRow(ctx, e, payload)
	case "files":
		if s.contentsWait(e) {
			return false, nil
		}
		return s.pushRow(ctx, e, payload)
	case "folders":
		return s.pushRow(ctx, e, payload)
	case "recordings":
		return s.pushRecording(ctx, e, payload)
	case "comments":
		return s.pushComment(ctx, uid, e, payload)
	case "page_history":
		// A snapshot never changes: one the server has, as an upload whose
		// answer was lost, is done. As an upsert, sent again it would ask
		// to change it, which no one may, and to read it back, which
		// another's may not yet, and fail for good.
		payload["created_by"] = uid
		err := s.be.InsertNew(ctx, e.Entity, payload)
		return err == nil, err
	}
	_, err := s.be.Upsert(ctx, e.Entity, payload, "")
	return err == nil, err
}

// pushChanged sends a row changed here: only the columns it changed when
// the server has the row, so a change another device made meanwhile to
// another column stays, and the whole row when the server has none.
func (s *Service) pushChanged(ctx context.Context, table string, payload map[string]any) (bool, error) {
	cols, partial := payload[store.ChangedKey].([]any)
	delete(payload, store.ChangedKey)
	if partial {
		changes := make(map[string]any, len(cols))
		for _, c := range cols {
			if k, ok := c.(string); ok {
				changes[k] = payload[k]
			}
		}
		if len(changes) == 0 {
			return true, nil
		}
		rows, err := s.be.Update(ctx, table, []string{remote.Eq("id", store.S(payload, "id"))}, changes)
		if err != nil || len(rows) > 0 {
			return err == nil, err
		}
	}
	_, err := s.be.Upsert(ctx, table, payload, "")
	return err == nil, err
}

// pushRow is pushChanged for a row the server may refuse to change: one
// the user can see there but no longer change, as a page shared with them
// to view since they changed it, or one whose change breaks a rule of the
// server's for good, as a move under a page another device moved under it
// meanwhile (remote.IsInvalid). Its change would never go, and the pulls
// pass over the row while it waits, so every change made to it elsewhere
// would be missed: the change gives way to the server's row instead. A
// row the server does not show stays queued, as one under a page not up
// yet.
func (s *Service) pushRow(ctx context.Context, e store.OutboxEntry, payload map[string]any) (bool, error) {
	if row, newer, err := s.newerThere(ctx, e, payload); err != nil || newer {
		if err != nil {
			return false, err
		}
		return s.takeServer(ctx, e, row)
	}
	done, err := s.pushChanged(ctx, e.Entity, payload)
	switch {
	case err == nil || remote.IsNetwork(err) || errors.Is(err, remote.ErrSignedOut) || ctx.Err() != nil:
		return done, err
	case s.waitsForParent(payload):
		return false, nil
	case remote.IsForbidden(err) || remote.IsInvalid(err):
		return s.giveWay(ctx, e, err)
	}
	return false, err
}

// waitsForParent reports whether a row names, as the page or folder it is
// in, one still waiting to go up: the server refuses a move under a page
// it does not have yet, but this one goes once its parent has, later in the
// same push, and counts no failure meanwhile. An entry changed again keeps
// its place in the queue, so a page renamed, then moved under a page made
// after, comes before that page.
func (s *Service) waitsForParent(payload map[string]any) bool {
	for col, table := range map[string]string{"parent_id": "pages", "folder_id": "folders", "parent_folder_id": "folders"} {
		if id, _ := payload[col].(string); id != "" && s.st.HasPending(table, id) {
			return true
		}
	}
	return false
}

// newerThere reports whether the server's row of a row sent again whole as
// this device's pages joined the account (store.AdoptKey) changed there
// after it last did here, as another device that synced with this one
// through iCloud Drive renamed, moved or deleted it since: sent whole, this
// device's older copy would undo that. It returns that row then. The row
// goes up when the server has none, or it changed here later.
func (s *Service) newerThere(ctx context.Context, e store.OutboxEntry, payload map[string]any) (row map[string]any, newer bool, err error) {
	at, ok := payload[store.AdoptKey].(string)
	delete(payload, store.AdoptKey)
	here, err := time.Parse(time.RFC3339Nano, at)
	if !ok || err != nil {
		return nil, false, nil
	}
	row, err = s.be.SelectOne(ctx, e.Entity, remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	if err != nil || row == nil || store.Time(row["updated_at"]).Before(here) {
		return nil, false, err
	}
	log.Printf("outbox %s/%s: changed on the server since; taking its row", e.Entity, e.EntityID)
	return row, true, nil
}

// giveWay has a change the server refused give way to the server's row,
// when the server shows it (pushRow), and returns refused otherwise.
func (s *Service) giveWay(ctx context.Context, e store.OutboxEntry, refused error) (bool, error) {
	row, err := s.be.SelectOne(ctx, e.Entity, remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	if err != nil || row == nil {
		return false, refused
	}
	log.Printf("outbox %s/%s: %v; taking the server's row", e.Entity, e.EntityID, refused)
	return s.takeServer(ctx, e, row)
}

// takeServer stores a row from the server over this device's change to
// it, as the pulls do, and reports the change done.
func (s *Service) takeServer(ctx context.Context, e store.OutboxEntry, row map[string]any) (bool, error) {
	// Off the queue first, so the row goes in; a change made here since
	// keeps it queued, and the row stays out, as the pulls leave it.
	if err := s.st.Dequeue(e); err != nil {
		return false, err
	}
	if e.Entity == "comments" {
		row["author_name"] = s.authorNames(ctx, []map[string]any{row})[store.S(row, "author_id")]
	}
	if err := s.putRow(e.Entity, row, s.putServerPage); err != nil {
		return false, err
	}
	s.st.Notify(e.Entity, e.PageID)
	return true, nil
}

// pushRecording sends a recording as pushRow does. One settled after a
// quit stopped its transcription (store.IfStatusKey) changes the server's
// only while that is still at the status it was here: the server, or the
// device that started the transcription, may have finished it meanwhile,
// and the recording here takes theirs then. One the server does not have
// goes up whole.
func (s *Service) pushRecording(ctx context.Context, e store.OutboxEntry, payload map[string]any) (bool, error) {
	status, _ := payload[store.IfStatusKey].(string)
	delete(payload, store.IfStatusKey)
	if status == "" {
		return s.pushRow(ctx, e, payload)
	}
	changes := map[string]any{}
	for _, k := range []string{"status", "error", "transcript", "duration_ms"} {
		changes[k] = payload[k]
	}
	rows, err := s.be.Update(ctx, "recordings", []string{remote.Eq("id", e.EntityID), remote.Eq("status", status)}, changes)
	if err != nil && !remote.IsForbidden(err) || len(rows) > 0 {
		return err == nil, err
	}
	row, rerr := s.be.SelectOne(ctx, "recordings", remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	switch {
	case rerr != nil:
		return false, rerr
	case row == nil && err != nil:
		return false, err
	case row == nil:
		return s.pushRow(ctx, e, payload)
	}
	return s.takeServer(ctx, e, row)
}

// pushComment updates a comment the server has with what may change, and
// inserts one it has not. An upsert would not do: its insert stamps the
// caller as the author, which the server then refuses as a change to the
// comment, so resolving someone else's comment never went up.
//
// A change the server refuses, as resolving someone else's comment on a
// page the user may only view, gives way to the server's comment, as
// pushRow's does to its row: the pulls pass over a comment while it
// waits, so its author's edits and deletion would never come.
func (s *Service) pushComment(ctx context.Context, uid string, e store.OutboxEntry, payload map[string]any) (bool, error) {
	if row, newer, err := s.newerThere(ctx, e, payload); err != nil || newer {
		if err != nil {
			return false, err
		}
		return s.takeServer(ctx, e, row)
	}
	cols, partial := payload[store.ChangedKey].([]any)
	delete(payload, store.ChangedKey)
	changes := map[string]any{"resolved": payload["resolved"]}
	if author, _ := payload["author_id"].(string); author == "" || author == uid {
		for _, k := range []string{"body", "quote", "agent_name", "deleted_at"} {
			changes[k] = payload[k]
		}
	} else if payload["deleted_at"] != nil {
		changes["deleted_at"] = payload["deleted_at"]
	}
	// Of a comment changed here, only what changed: a resolve made offline
	// does not undo a delete made elsewhere.
	if partial {
		for k := range changes {
			if !slices.Contains(cols, any(k)) {
				delete(changes, k)
			}
		}
	}
	id, _ := payload["id"].(string)
	if partial && len(changes) == 0 {
		return true, nil
	}
	rows, err := s.be.Update(ctx, "comments", []string{remote.Eq("id", id)}, changes)
	if err != nil || len(rows) > 0 {
		return err == nil, err
	}
	// The server stamps the author on insert (for agent replies, the user
	// who asked).
	if payload["author_id"] == nil {
		payload["author_id"] = uid
	}
	if _, err := s.be.Insert(ctx, "comments", payload); err != nil {
		if remote.IsConflict(err) || remote.IsForbidden(err) {
			return s.giveWay(ctx, e, errors.New("the comment is someone else's, and you cannot change it"))
		}
		return false, err
	}
	return true, nil
}

func (s *Service) pushFile(ctx context.Context, uid string, payload map[string]any) (bool, error) {
	local, waits, err := s.uploadFrom(ctx, payload)
	if err != nil || waits || local == "" {
		return err == nil && !waits, err // gone: nothing to upload
	}
	f, err := os.Open(local) // #nosec G304 -- a file in the app's own data folder.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil // gone: nothing to upload
		}
		return false, err // there, but not to be read now: tried again
	}
	bucket, _ := payload["bucket"].(string)
	recID, _ := payload["recording_id"].(string)
	path, _ := payload["path"].(string)
	if path == "" {
		path = uid + "/" + recID + ".wav"
	}
	ct, _ := payload["content_type"].(string)
	err = s.be.Upload(ctx, bucket, path, f, ct)
	_ = f.Close()
	if err != nil || recID == "" {
		return err == nil, err
	}
	rec, ok, err := s.uploadedAudio(recID, path, local, time.Now())
	if err != nil || !ok {
		return err == nil, err
	}
	_, _ = s.be.Upsert(ctx, "recordings", store.RecordingToRemote(rec), "")
	return true, nil
}

// removeStored removes a deleted file's object from Storage, once it may
// go (removal). An object the server does not have is gone already.
func (s *Service) removeStored(ctx context.Context, payload map[string]any) (bool, error) {
	if waits, stays := s.removal(payload); waits || stays {
		return stays, nil
	}
	err := s.be.Remove(ctx, store.S(payload, "bucket"), store.S(payload, "path"))
	if remote.IsNotFound(err) {
		return true, nil
	}
	return err == nil, err
}

func (s *Service) flushRemote(ctx context.Context, pageID string) error {
	if err := s.resolveSpace(ctx); err != nil {
		return err
	}
	entries, err := s.st.Pending()
	if err != nil {
		return err
	}
	// What the job's row refers to must be on the server before it: the
	// page, under the pages it is inside, in the folders they are in, its
	// content, and its comments, as the thread a comment's run answers in.
	// The folders and pages go from the top.
	var chain, folders []string
	seen := map[string]bool{}
	for id := pageID; id != "" && !seen[id]; {
		seen[id] = true
		chain = append(chain, id)
		p, err := s.st.Page(id)
		if err != nil {
			break
		}
		for f := p.FolderID; f != "" && !seen[f]; {
			seen[f] = true
			folders = append(folders, f)
			folder, err := s.st.Folder(f)
			if err != nil {
				break
			}
			f = folder.ParentFolderID
		}
		id = p.ParentID
	}
	slices.Reverse(chain)
	slices.Reverse(folders)
	var order []store.OutboxEntry
	queue := func(entity string, ids []string) {
		for _, id := range ids {
			for _, e := range entries {
				if e.Entity == entity && e.EntityID == id {
					order = append(order, e)
				}
			}
		}
	}
	queue("folders", folders)
	queue("pages", chain)
	for _, e := range entries {
		if e.Entity == "page_content" && e.EntityID == pageID || e.Entity == "comments" && e.PageID == pageID {
			order = append(order, e)
		}
	}
	uid := s.be.UserID()
	for _, e := range order {
		done, err := s.pushOne(ctx, uid, e)
		if err != nil {
			return err
		}
		if done {
			_ = s.st.Dequeue(e)
		}
	}
	return nil
}
