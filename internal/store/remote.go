package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

func iso(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func PageToRemote(p Page) map[string]any {
	return map[string]any{
		"id": p.ID, "space_id": p.SpaceID, "parent_id": ns(p.ParentID), "folder_id": ns(p.FolderID),
		"title": ClampTitle(p.Title), "icon": ns(p.Icon), "sort_key": p.SortKey, "created_by": ns(p.CreatedBy),
		"created_at": iso(p.CreatedAt), "deleted_at": iso(p.DeletedAt), "is_template": p.Template,
		"tags": NormalizeTags(p.Tags), "default_template_id": ns(p.DefaultTemplateID), "changed_at": iso(p.UpdatedAt),
	}
}

func FolderToRemote(f Folder) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "parent_folder_id": ns(f.ParentFolderID), "name": f.Name,
		"sort_key": f.SortKey, "created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt),
		"default_template_id": ns(f.DefaultTemplateID), "changed_at": iso(f.UpdatedAt),
	}
}

func CommentToRemote(c Comment) map[string]any {
	var agent any
	if c.AuthorKind == "agent" || c.AuthorKind == "guest" {
		agent = c.AuthorName
	}
	return map[string]any{
		"id": c.ID, "page_id": c.PageID, "block_id": c.BlockID, "parent_comment_id": ns(c.ParentCommentID),
		"author_id": ns(c.AuthorID), "author_kind": c.AuthorKind, "agent_name": agent, "body": c.Body,
		"quote": ns(c.Quote), "resolved": c.Resolved, "created_at": iso(c.CreatedAt), "deleted_at": iso(c.DeletedAt),
		"changed_at": iso(c.UpdatedAt),
	}
}

func RecordingToRemote(r Recording) map[string]any {
	transcript := r.Transcript
	if transcript == nil {
		transcript = []Segment{}
	}
	return map[string]any{
		"id": r.ID, "page_id": r.PageID, "block_id": r.BlockID, "audio_path": ns(r.AudioPath),
		"duration_ms": r.DurationMs, "stt_provider": r.SttProvider, "stt_model": r.SttModel, "status": r.Status,
		"transcript": transcript, "language": ns(r.Language), "error": ns(r.Error), "created_at": iso(r.CreatedAt),
		"changed_at": iso(r.UpdatedAt),
	}
}

func FileToRemote(f File) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "folder_id": ns(f.FolderID), "page_id": ns(f.PageID), "name": f.Name,
		"storage_path": ns(f.StoragePath), "mime_type": f.MimeType, "size_bytes": f.SizeBytes,
		"created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt), "changed_at": iso(f.UpdatedAt),
	}
}

func HistoryToRemote(h HistoryEntry, userID string) map[string]any {
	var content any = rawJSON(CleanJSON(h.Content))
	var by any = userID
	if userID == LocalUserID {
		by = nil
	}
	return map[string]any{
		"id": h.ID, "page_id": h.PageID, "content": content, "reason": h.Reason, "created_by": by, "created_at": iso(h.CreatedAt),
	}
}

func CleanText(text string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(text, "\uFFFD"), "\x00", "\uFFFD")
}

func CleanRow(row map[string]any) {
	for k, v := range row {
		row[k] = cleanValue(v)
	}
}

func cleanValue(v any) any {
	switch v := v.(type) {
	case string:
		return CleanText(v)
	case []any:
		for i, e := range v {
			v[i] = cleanValue(e)
		}
	case map[string]any:
		CleanRow(v)
	}
	return v
}

func CleanJSON(encoded string) string {
	encoded = strings.ToValidUTF8(encoded, "\uFFFD")
	if !strings.Contains(encoded, `\u`) {
		return encoded
	}
	var b strings.Builder
	b.Grow(len(encoded))
	for i := 0; i < len(encoded); {
		if encoded[i] != '\\' || i+1 == len(encoded) {
			b.WriteByte(encoded[i])
			i++
			continue
		}
		n, ok := escapeLen(encoded[i:])
		if ok {
			b.WriteString(encoded[i : i+n])
		} else {
			b.WriteString(`\ufffd`)
		}
		i += n
	}
	return b.String()
}

func escapeLen(s string) (int, bool) {
	unit, ok := escapedUnit(s)
	switch {
	case !ok:
		return 2, true
	case unit == 0, unit >= 0xDC00 && unit <= 0xDFFF:
		return 6, false
	case unit >= 0xD800 && unit <= 0xDBFF:
		if low, ok := escapedUnit(s[6:]); ok && low >= 0xDC00 && low <= 0xDFFF {
			return 12, true
		}
		return 6, false
	}
	return 6, true
}

func escapedUnit(s string) (uint16, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:6], 16, 16)
	return uint16(n), err == nil
}

type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

func Time(v any) time.Time {
	s, _ := v.(string)
	if s == "" {
		return time.Now()
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999-07"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now()
}

func TimeOrZero(v any) time.Time {
	if v == nil {
		return time.Time{}
	}
	return Time(v)
}

func Int(r map[string]any, key string) int64 {
	n, _ := r[key].(float64)
	return int64(n)
}

func S(r map[string]any, key string) string {
	s, _ := r[key].(string)
	return s
}

func PageFromRemote(r map[string]any, keepOffline bool) Page {
	sk := S(r, "sort_key")
	if sk == "" {
		sk = "V"
	}
	return Page{
		ID: S(r, "id"), SpaceID: S(r, "space_id"), ParentID: S(r, "parent_id"), FolderID: S(r, "folder_id"),
		Title: S(r, "title"), Icon: S(r, "icon"), SortKey: sk, CreatedBy: S(r, "created_by"),
		CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"]),
		KeepOffline: keepOffline, Template: r["is_template"] == true, Tags: tagsFromRemote(r["tags"]),
		DefaultTemplateID: S(r, "default_template_id"),
	}
}

func tagsFromRemote(v any) []string {
	var tags []string
	switch v := v.(type) {
	case []any:
		for _, t := range v {
			if s, ok := t.(string); ok {
				tags = append(tags, s)
			}
		}
	case []string:
		tags = v
	}
	return NormalizeTags(tags)
}

func FolderFromRemote(r map[string]any) Folder {
	sk := S(r, "sort_key")
	if sk == "" {
		sk = "V"
	}
	return Folder{ID: S(r, "id"), SpaceID: S(r, "space_id"), ParentFolderID: S(r, "parent_folder_id"), Name: S(r, "name"),
		SortKey: sk, CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"]),
		DefaultTemplateID: S(r, "default_template_id")}
}

func CommentFromRemote(r map[string]any, authorName string) Comment {
	kind := S(r, "author_kind")
	if kind == "" {
		kind = "user"
	}
	if n := S(r, "agent_name"); n != "" {
		authorName = n
	}
	resolved, _ := r["resolved"].(bool)
	return Comment{ID: S(r, "id"), PageID: S(r, "page_id"), BlockID: S(r, "block_id"), ParentCommentID: S(r, "parent_comment_id"),
		AuthorID: S(r, "author_id"), AuthorKind: kind, AuthorName: authorName, Body: S(r, "body"), Quote: S(r, "quote"),
		Resolved: resolved, CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"])}
}

func FileFromRemote(r map[string]any) File {
	size, _ := r["size_bytes"].(float64)
	return File{ID: S(r, "id"), SpaceID: S(r, "space_id"), FolderID: S(r, "folder_id"), PageID: S(r, "page_id"), Name: fileName(S(r, "name")),
		StoragePath: S(r, "storage_path"), MimeType: S(r, "mime_type"), SizeBytes: int64(size),
		CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"])}
}

var pulled = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true}

func (s *Store) HasRowAt(table, id string, at time.Time) bool {
	if !pulled[table] {
		return false
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND updated_at = ?`, id, at.UnixMilli()).Scan(&n) // #nosec G202
	return n > 0
}

var pulledTables = []string{"pages", "folders", "comments", "recordings", "files"}

const PeerKey = "_peer"

func (s *Store) putRemote(entity, id string, r map[string]any, over *OutboxEntry, write func(tx *sql.Tx) error) (bool, error) {
	stored := false
	err := s.inTx(func(tx *sql.Tx) error {
		if err := dequeueIn(tx, over); err != nil {
			return err
		}
		waits, err := pending(tx, entity, id)
		if err != nil || waits {
			return err
		}
		stored = true
		if err := write(tx); err != nil {
			return err
		}
		var at any
		if _, peer := r[PeerKey]; !peer {
			at = Time(r["updated_at"]).UnixMilli()
		}
		_, err = tx.Exec(`UPDATE `+entity+` SET pulled_at = ? WHERE id = ?`, at, id) // #nosec G202
		return err
	})
	if err == nil && over != nil {
		s.Notify("outbox", "")
	}
	return stored && err == nil, err
}

func dequeueIn(tx *sql.Tx, e *OutboxEntry) error {
	if e == nil {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM outbox WHERE id = ? AND revision = ?`, e.ID, e.Revision)
	return err
}

func (s *Store) TakeRemote(e OutboxEntry, r map[string]any) error {
	switch e.Entity {
	case "pages":
		return s.putRemotePage(r, &e)
	case "folders":
		return s.putRemoteFolder(r, &e)
	case "comments":
		return s.putRemoteComment(r, S(r, "author_name"), &e)
	case "files":
		return s.putRemoteFile(r, &e)
	case "recordings":
		return s.putRemoteRecording(r, &e)
	}
	return s.Dequeue(e)
}

func unpulled(q querier, entity, id string) error {
	if !slices.Contains(pulledTables, entity) {
		return nil
	}
	_, err := q.Exec(`UPDATE `+entity+` SET pulled_at = NULL WHERE id = ? AND pulled_at IS NOT NULL`, id) // #nosec G202
	return err
}

func (s *Store) RowPulledAt(table, id string) (time.Time, bool) {
	if !slices.Contains(pulledTables, table) {
		return time.Time{}, false
	}
	var at sql.NullInt64
	if err := s.db.QueryRow(`SELECT pulled_at FROM `+table+` WHERE id = ?`, id).Scan(&at); err != nil || !at.Valid { // #nosec G202
		return time.Time{}, false
	}
	return time.UnixMilli(at.Int64), true
}

func (s *Store) PutRemotePage(r map[string]any) error { return s.putRemotePage(r, nil) }

func (s *Store) putRemotePage(r map[string]any, over *OutboxEntry) error {
	id := S(r, "id")
	_, err := s.putRemote("pages", id, r, over, func(tx *sql.Tx) error {
		keep := true
		if err := tx.QueryRow(`SELECT keep_offline FROM pages WHERE id = ?`, id).Scan(&keep); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return writePage(tx, PageFromRemote(r, keep))
	})
	return err
}

func (s *Store) PutRemoteFolder(r map[string]any) error { return s.putRemoteFolder(r, nil) }

func (s *Store) putRemoteFolder(r map[string]any, over *OutboxEntry) error {
	f := FolderFromRemote(r)
	_, err := s.putRemote("folders", f.ID, r, over, func(tx *sql.Tx) error { return writeFolder(tx, f) })
	return err
}

func (s *Store) PutRemoteComment(r map[string]any, authorName string) error {
	return s.putRemoteComment(r, authorName, nil)
}

func (s *Store) putRemoteComment(r map[string]any, authorName string, over *OutboxEntry) error {
	c := CommentFromRemote(r, authorName)
	_, err := s.putRemote("comments", c.ID, r, over, func(tx *sql.Tx) error { return putComment(tx, c) })
	return err
}

func (s *Store) PutRemoteFile(r map[string]any) error { return s.putRemoteFile(r, nil) }

func (s *Store) putRemoteFile(r map[string]any, over *OutboxEntry) error {
	f := FileFromRemote(r)
	stored, err := s.putRemote("files", f.ID, r, over, func(tx *sql.Tx) error {
		if err := putFile(tx, s.Dir, f); err != nil || !f.DeletedAt.IsZero() {
			return err
		}
		_, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage_delete' AND entity_id = ?`, "files/"+f.ID)
		return err
	})
	if stored && !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	return err
}

func RecordingFromRemote(r map[string]any) Recording {
	rec := Recording{ID: S(r, "id"), PageID: S(r, "page_id"), BlockID: S(r, "block_id"), AudioPath: S(r, "audio_path"),
		SttProvider: S(r, "stt_provider"), SttModel: S(r, "stt_model"), Status: S(r, "status"), Language: S(r, "language"),
		Error: S(r, "error"), CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"])}
	if d, ok := r["duration_ms"].(float64); ok {
		rec.DurationMs = int(d)
	}
	if rec.Status == "" {
		rec.Status = "ready"
	}
	if raw, err := json.Marshal(r["transcript"]); err == nil {
		_ = json.Unmarshal(raw, &rec.Transcript)
	}
	return rec
}

func (s *Store) PutRemoteRecording(r map[string]any) error { return s.putRemoteRecording(r, nil) }

func (s *Store) putRemoteRecording(r map[string]any, over *OutboxEntry) error {
	rec := RecordingFromRemote(r)
	_, err := s.putRemote("recordings", rec.ID, r, over, func(tx *sql.Tx) error {
		switch old, err := recordingIn(tx, s.Dir, rec.ID); {
		case err == nil:
			rec.LocalPath, rec.UploadedAt = old.LocalPath, old.UploadedAt
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		return putRecording(tx, s.Dir, rec)
	})
	return err
}
