package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func (s *Service) push(ctx context.Context) error {
	entries, err := s.st.Queued()
	if err != nil {
		return err
	}
	s.forgetRetries(entries)
	uid := s.be.UserID()
	var held []store.OutboxEntry
	timeouts := 0
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
			done, err := s.pushOne(ctx, uid, &e)
			if err != nil {
				if remote.IsNetwork(err) || remote.IsBusy(err) && !s.ownTimeout(ctx, err, &timeouts) ||
					errors.Is(err, remote.ErrSignedOut) || ctx.Err() != nil {
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
		for i := range entries {
			entries[i].Payload = ""
		}
	}
	return nil
}

const ownTimeouts = 2

func (s *Service) ownTimeout(ctx context.Context, err error, seen *int) bool {
	if !remote.IsStatementTimeout(err) || *seen == ownTimeouts || !s.serverAnswers(ctx) {
		return false
	}
	*seen++
	return true
}

func (s *Service) serverAnswers(ctx context.Context) bool {
	_, err := s.be.SelectOne(ctx, "spaces", remote.Query{Select: "id"})
	return err == nil
}

func (s *Service) pushOne(ctx context.Context, uid string, entry *store.OutboxEntry) (bool, error) {
	payload, err := s.payload(entry)
	if err != nil || payload == nil {
		return err == nil, err
	}
	store.CleanRow(payload)
	e := *entry
	switch e.Entity {
	case "page_content":
		done, err := s.pushContent(ctx, e.EntityID)
		if errors.Is(err, errCannotEdit) {
			return s.takeServerContent(ctx, e, err)
		}
		return done, err
	case "storage":
		payload, err := s.keyed(entry, payload)
		if err != nil || payload == nil {
			return err == nil, err
		}
		return s.pushUpload(ctx, uid, *entry, payload)
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
		if _, whole := payload["content"]; !whole {
			h, err := s.st.HistoryEntry(e.EntityID)
			if store.IsNotFound(err) {
				return true, nil
			} else if err != nil {
				return false, err
			}
			payload = store.HistoryToRemote(h, uid)
		}
		payload["created_by"] = uid
		err := s.be.InsertNew(ctx, e.Entity, payload)
		return err == nil, err
	}
	_, err = s.be.Upsert(ctx, e.Entity, payload, "")
	return err == nil, err
}

func (s *Service) pushChanged(ctx context.Context, table string, payload map[string]any) (bool, error) {
	cols, partial := payload[store.ChangedKey].([]any)
	delete(payload, store.ChangedKey)
	if partial {
		changes := make(map[string]any, len(cols)+1)
		for _, c := range cols {
			if k, ok := c.(string); ok {
				changes[k] = payload[k]
			}
		}
		if len(changes) == 0 {
			return true, nil
		}
		withChangedAt(changes, payload)
		rows, err := s.be.Update(ctx, table, []string{remote.Eq("id", store.S(payload, "id"))}, changes)
		if err != nil || len(rows) > 0 {
			return err == nil, err
		}
	}
	_, err := s.be.Upsert(ctx, table, payload, "")
	return err == nil, err
}

func withChangedAt(changes, payload map[string]any) {
	if at, ok := payload["changed_at"]; ok && at != nil {
		changes["changed_at"] = at
	}
}

func changedAt(row map[string]any) time.Time {
	if at, ok := row["changed_at"]; ok && at != nil {
		return store.Time(at)
	}
	return store.Time(row["updated_at"])
}

func (s *Service) pushRow(ctx context.Context, e store.OutboxEntry, payload map[string]any) (bool, error) {
	if row, newer, err := s.newerThere(ctx, e, payload); err != nil || newer {
		if err != nil {
			return false, err
		}
		return s.takeServer(ctx, e, row)
	}
	if s.parentStuck(payload) {
		return false, nil
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

var parentCols = map[string]string{"parent_id": "pages", "folder_id": "folders", "parent_folder_id": "folders"}

func (s *Service) waitsForParent(payload map[string]any) bool {
	for col, table := range parentCols {
		if id, _ := payload[col].(string); id != "" && s.st.HasPending(table, id) {
			return true
		}
	}
	return false
}

func (s *Service) parentStuck(payload map[string]any) bool {
	for col, table := range parentCols {
		if id, _ := payload[col].(string); id != "" && s.st.PendingFailed(table, id) {
			return true
		}
	}
	return false
}

func (s *Service) newerThere(ctx context.Context, e store.OutboxEntry, payload map[string]any) (row map[string]any, newer bool, err error) {
	at, ok := payload[store.AdoptKey].(string)
	delete(payload, store.AdoptKey)
	here, err := time.Parse(time.RFC3339Nano, at)
	if !ok || err != nil {
		return nil, false, nil
	}
	row, err = s.be.SelectOne(ctx, e.Entity, remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	if err != nil || row == nil {
		return nil, false, err
	}
	if _, pulled := s.st.RowPulledAt(e.Entity, e.EntityID); pulled || sameAsThere(payload, row) {
		return row, true, nil
	}
	if changedAt(row).Before(here) {
		return nil, false, nil
	}
	log.Printf("outbox %s/%s: changed on the server since; taking its row", e.Entity, e.EntityID)
	return row, true, nil
}

func sameAsThere(payload, there map[string]any) bool {
	for k, v := range payload {
		if k == "changed_at" {
			continue
		}
		w, ok := there[k]
		if !ok || !sameValue(v, w) {
			return false
		}
	}
	return true
}

func sameValue(a, b any) bool {
	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok && sa != sb {
			ta, errA := time.Parse(time.RFC3339Nano, sa)
			tb, errB := time.Parse(time.RFC3339Nano, sb)
			return errA == nil && errB == nil && ta.UnixMilli() == tb.UnixMilli()
		}
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func (s *Service) giveWay(ctx context.Context, e store.OutboxEntry, refused error) (bool, error) {
	row, err := s.be.SelectOne(ctx, e.Entity, remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	if err != nil || row == nil {
		return false, refused
	}
	log.Printf("outbox %s/%s: %v; taking the server's row", e.Entity, e.EntityID, refused)
	return s.takeServer(ctx, e, row)
}

func (s *Service) takeServer(ctx context.Context, e store.OutboxEntry, row map[string]any) (bool, error) {
	if e.Entity == "comments" {
		names, err := s.authorNames(ctx, []map[string]any{row})
		if err != nil {
			return false, err
		}
		row["author_name"] = names[store.S(row, "author_id")]
	}
	take := func(r map[string]any) error { return s.st.TakeRemote(e, r) }
	var err error
	if e.Entity == "pages" {
		err = s.settleLoop(row, take)
	} else {
		err = take(row)
	}
	if err != nil {
		return false, err
	}
	s.st.Notify(e.Entity, e.PageID)
	return true, nil
}

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
	withChangedAt(changes, payload)
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

func (s *Service) pushComment(ctx context.Context, uid string, e store.OutboxEntry, payload map[string]any) (bool, error) {
	if row, newer, err := s.newerThere(ctx, e, payload); err != nil || newer {
		if err != nil {
			return false, err
		}
		return s.takeServer(ctx, e, row)
	}
	for _, k := range []string{"body", "quote"} {
		if text, ok := payload[k].(string); ok {
			payload[k] = store.ClampComment(text)
		}
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
	withChangedAt(changes, payload)
	rows, err := s.be.Update(ctx, "comments", []string{remote.Eq("id", id)}, changes)
	if err != nil || len(rows) > 0 {
		return err == nil, err
	}
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

func (s *Service) pushFile(ctx context.Context, uid string, payload map[string]any, read *atomic.Int64) (bool, error) {
	local, waits, err := s.uploadFrom(ctx, payload)
	if err != nil || waits || local == "" {
		return err == nil && !waits, err
	}
	f, err := os.Open(local) // #nosec G304
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	bucket, _ := payload["bucket"].(string)
	recID, _ := payload["recording_id"].(string)
	path, _ := payload["path"].(string)
	if path == "" {
		path = uid + "/" + recID + ".wav"
	}
	ct, _ := payload["content_type"].(string)
	err = s.be.Upload(ctx, bucket, path, timedReader{f, read}, ct)
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

func (s *Service) removeStored(ctx context.Context, payload map[string]any) (bool, error) {
	if waits, stays := s.removal(payload); waits || stays {
		return stays, nil
	}
	err := s.be.Remove(ctx, store.S(payload, "bucket"), store.S(payload, "path"))
	if remote.IsNotFound(err) || remote.IsInvalidKey(err) {
		return true, nil
	}
	return err == nil, err
}
