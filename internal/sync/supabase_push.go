package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
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
		purge := s.purgeSeen(e, payload)
		done, err := s.pushRow(ctx, e, payload)
		if err != nil || !done || !purge {
			return done, err
		}
		return s.purgeTaken(ctx, e.EntityID)
	case "files":
		if s.contentsWait(e) {
			return false, nil
		}
		purge := s.purgeSeen(e, payload)
		done, err := s.pushRow(ctx, e, payload)
		if err != nil || !done || !purge {
			return done, err
		}
		// The copy here goes once the server has the deletion; one it refused came back with the file.
		return true, s.st.WipePurgedFile(e.EntityID)
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
	// Nothing else is queued to go up: an entry of another kind, as from a Leaflet that queued one this one does not
	// know, is no table's to write.
	log.Printf("outbox %s/%s: nothing goes up for it here; dropped", e.Entity, e.EntityID)
	return true, nil
}

// seenKey is the column a deletion for good sends the server's updated_at of the row with, as this device last had
// it: the server refuses the deletion of a row that changed there since, as one another device restored, or deleted
// again, whatever the devices' clocks say.
const seenKey = "seen_updated_at"

// purgeSeen reports whether the payload deletes its row for good, and has it send the row's updated_at as this device
// last had it from the server, if it had it.
func (s *Service) purgeSeen(e store.OutboxEntry, payload map[string]any) bool {
	if _, purge := payload["purged_at"]; !purge {
		return false
	}
	at, ok := s.st.SeenAt(e.Entity, e.EntityID)
	if !ok {
		return true
	}
	cols, partial := payload[store.ChangedKey].([]any)
	if !partial {
		// A whole row goes as a change of every column of it first: an upsert tries its insert first, which leaves
		// the update it falls to on a row the server has without what this device saw.
		for _, k := range slices.Sorted(maps.Keys(payload)) {
			if k != "id" && !strings.HasPrefix(k, "_") {
				cols = append(cols, k)
			}
		}
	}
	payload[seenKey] = at.UTC().Format(time.RFC3339Nano)
	if !slices.Contains(cols, any(seenKey)) {
		cols = append(cols, seenKey)
	}
	payload[store.ChangedKey] = cols
	return true
}

// purgeTaken takes what was on a page off this device once the server has taken its deletion for good, and has it
// removed where it is stored: what this device knew of, and what the server listed of the page, as another device, or
// someone it was shared with, put there. Until the list is read, the deletion stays queued: the server takes it again
// as it is. A deletion the server refused brought the page back as the server has it (putRemotePage), with what was
// on it, which waited here; what came for it meanwhile, which a pull passed by as of a page deleted for good, comes
// again.
func (s *Service) purgeTaken(ctx context.Context, pageID string) (bool, error) {
	if !s.st.IsPurged(pageID) {
		if err := s.pullTreeAgain(ctx, pageID); err != nil {
			log.Printf("page %s, back from the bin: %v; pulled again later", pageID, err)
		}
		return true, nil
	}
	listed, err := s.purgedObjects(ctx, pageID)
	if err != nil {
		return false, err
	}
	if err := s.st.WipePurged(pageID, listed); err != nil {
		return false, err
	}
	s.KickPush()
	return true, nil
}

// purgedObjects is every object the server lists of a page deleted for good, a page of the list at a time, in the order
// of its key.
func (s *Service) purgedObjects(ctx context.Context, pageID string) ([]store.StoredObject, error) {
	var listed []store.StoredObject
	var after *store.StoredObject
	for {
		filters := []string{remote.Eq("page_id", pageID)}
		if after != nil {
			filters = append(filters, "or=(bucket_id.gt."+quote(after.Bucket)+",and(bucket_id.eq."+quote(after.Bucket)+
				",name.gt."+quote(after.Path)+"))")
		}
		rows, err := s.be.Select(ctx, "purged_objects", remote.Query{Select: "bucket_id,name", Filters: filters,
			Order: "bucket_id,name", Limit: treeRows})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			listed = append(listed, store.StoredObject{Bucket: store.S(r, "bucket_id"), Path: store.S(r, "name")})
		}
		if len(rows) < treeRows {
			return listed, nil
		}
		last := listed[len(listed)-1]
		after = &last
	}
}

// pushChanged writes what the payload changed of its row, or the whole row when the server has none of it, and is the
// row as the server took it, when it says.
func (s *Service) pushChanged(ctx context.Context, table string, payload map[string]any) (map[string]any, bool, error) {
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
			return nil, true, nil
		}
		withChangedAt(changes, payload)
		rows, err := s.be.Update(ctx, table, []string{remote.Eq("id", store.S(payload, "id"))}, changes)
		if err != nil || len(rows) > 0 {
			return firstRow(rows), err == nil, err
		}
	}
	rows, err := s.be.Upsert(ctx, table, payload, "")
	return firstRow(rows), err == nil, err
}

func firstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
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
	if s.parentStuck(e, payload) {
		return false, nil
	}
	row, done, err := s.pushChanged(ctx, e.Entity, payload)
	if at := store.S(row, "updated_at"); err == nil && at != "" {
		// The server has the row as this device wrote it, as of then: a deletion for good of it from here on is of
		// what the server had then, and of nothing another device did since.
		if err := s.st.NoteSeen(e.Entity, e.EntityID, store.Time(at)); err != nil {
			log.Printf("outbox %s/%s: %v", e.Entity, e.EntityID, err)
		}
	}
	switch {
	case err == nil || remote.IsNetwork(err) || errors.Is(err, remote.ErrSignedOut) || ctx.Err() != nil:
		return done, err
	case s.waitsForParent(e, payload):
		return false, nil
	case remote.IsForbidden(err) || remote.IsInvalid(err):
		return s.giveWay(ctx, e, err)
	}
	return false, err
}

var parentCols = map[string]string{"parent_id": "pages", "folder_id": "folders", "parent_folder_id": "folders"}

func (s *Service) waitsForParent(e store.OutboxEntry, payload map[string]any) bool {
	for _, p := range parentsOf(e, payload) {
		if s.st.HasPending(p.table, p.id) {
			return true
		}
	}
	return false
}

func (s *Service) parentStuck(e store.OutboxEntry, payload map[string]any) bool {
	for _, p := range parentsOf(e, payload) {
		if s.st.PendingFailed(p.table, p.id) {
			return true
		}
	}
	return false
}

type rowRef struct{ table, id string }

// parentsOf is the rows the payload hangs under. A row named as its own parent is left out: its own entry in the
// outbox is not a parent that has yet to go up.
func parentsOf(e store.OutboxEntry, payload map[string]any) []rowRef {
	var out []rowRef
	for col, table := range parentCols {
		if id, _ := payload[col].(string); id != "" && (table != e.Entity || id != e.EntityID) {
			out = append(out, rowRef{table, id})
		}
	}
	return out
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
	if err := s.st.TakeRemote(e, row); err != nil {
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
	// A change made here before the settle, which waits with it, goes with it, as its audio_path.
	if cols, ok := payload[store.ChangedKey].([]any); ok {
		for _, c := range cols {
			if k, ok := c.(string); ok {
				changes[k] = payload[k]
			}
		}
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
	local, waits, err := s.uploadFrom(ctx, payload, read)
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
		path = recordingPath(uid, recID)
	}
	ct, _ := payload["content_type"].(string)
	err = s.be.Upload(ctx, bucket, path, timedReader{f, read}, ct)
	_ = f.Close()
	if err != nil || recID == "" {
		return err == nil, err
	}
	_, ok, err := s.uploadedAudio(recID, path, local, time.Now())
	if err != nil || !ok {
		return err == nil, err
	}
	// The recording's audio_path is queued as the one column it changed, which the next push sends.
	s.KickPush()
	return true, nil
}

// recordingPath is where a recording's audio is stored: under its recorder's id, by its own.
func recordingPath(uid, recordingID string) string { return uid + "/" + recordingID + ".wav" }

func (s *Service) removeStored(ctx context.Context, payload map[string]any) (bool, error) {
	if waits, stays := s.removal(payload); waits || stays {
		return stays, nil
	}
	bucket, path := store.S(payload, "bucket"), store.S(payload, "path")
	// A recording deleted before its audio_path came here is at the path its upload took: this account's, unless
	// someone else made it on a page of this account deleted for good, whose path the server listed to remove.
	if recID, uid := store.S(payload, "recording_id"), s.be.UserID(); path == "" && bucket == "recordings" && recID != "" && uid != "" {
		path = recordingPath(uid, recID)
		listed, err := s.be.SelectOne(ctx, "purged_objects", remote.Query{Select: "name",
			Filters: []string{remote.Eq("bucket_id", bucket), remote.Like("name", "*/"+recID+".*")}})
		if err != nil {
			return false, err
		}
		if listed != nil {
			path = store.S(listed, "name")
		}
	}
	err := s.be.Remove(ctx, bucket, path)
	if remote.IsNotFound(err) || remote.IsInvalidKey(err) {
		return true, nil
	}
	return err == nil, err
}
