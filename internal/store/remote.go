package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Conversions from local rows to Supabase rows. Column names follow
// supabase/migrations.
//
// A row goes with changed_at, when it last changed here: the server keeps
// it, as its own updated_at is when the row reached it, which for a row
// sent late, as one a device that synced through iCloud Drive sends as it
// signs in, is long after the change (sync's newerThere). A row read from
// the server keeps the server's updated_at, by which the pulls tell the
// rows they have (HasRowAt).

func iso(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// PageToRemote is a page as the pages table has it, its title within the
// bound the server holds titles to (ClampTitle): one longer, as a page from
// another device's older Leaflet may have, would be refused every time it
// went up.
func PageToRemote(p Page) map[string]any {
	return map[string]any{
		"id": p.ID, "space_id": p.SpaceID, "parent_id": ns(p.ParentID), "folder_id": ns(p.FolderID),
		"title": ClampTitle(p.Title), "icon": ns(p.Icon), "sort_key": p.SortKey, "created_by": ns(p.CreatedBy),
		"created_at": iso(p.CreatedAt), "deleted_at": iso(p.DeletedAt), "is_template": p.Template,
		"tags": NormalizeTags(p.Tags), "default_template_id": ns(p.DefaultTemplateID), "changed_at": iso(p.UpdatedAt),
	}
}

// FolderToRemote is a folder as the folders table has it.
func FolderToRemote(f Folder) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "parent_folder_id": ns(f.ParentFolderID), "name": f.Name,
		"sort_key": f.SortKey, "created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt),
		"default_template_id": ns(f.DefaultTemplateID), "changed_at": iso(f.UpdatedAt),
	}
}

// CommentToRemote is a comment as the comments table has it.
func CommentToRemote(c Comment) map[string]any {
	// An agent's name, or a guest's, rides in agent_name: the row's
	// author is the person who posted it.
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

// RecordingToRemote is a recording as the recordings table has it.
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

// FileToRemote is a file as the files table has it.
func FileToRemote(f File) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "folder_id": ns(f.FolderID), "page_id": ns(f.PageID), "name": f.Name,
		"storage_path": ns(f.StoragePath), "mime_type": f.MimeType, "size_bytes": f.SizeBytes,
		"created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt), "changed_at": iso(f.UpdatedAt),
	}
}

// HistoryToRemote is a snapshot as the page_history table has it, without
// its job: a local run's job has no server row for ai_job_id to name.
func HistoryToRemote(h HistoryEntry, userID string) map[string]any {
	var content any = rawJSON(h.Content)
	var by any = userID
	if userID == LocalUserID {
		by = nil
	}
	return map[string]any{
		"id": h.ID, "page_id": h.PageID, "content": content, "reason": h.Reason, "created_by": by, "created_at": iso(h.CreatedAt),
	}
}

// rawJSON keeps stored JSON as it is inside a payload.
type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// Time parses a timestamp from Postgres, or returns now.
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

// TimeOrZero parses a nullable timestamp.
func TimeOrZero(v any) time.Time {
	if v == nil {
		return time.Time{}
	}
	return Time(v)
}

// Int reads a number column of a remote row.
func Int(r map[string]any, key string) int64 {
	n, _ := r[key].(float64)
	return int64(n)
}

// S reads a string column of a remote row.
func S(r map[string]any, key string) string {
	s, _ := r[key].(string)
	return s
}

// PageFromRemote reads a pages row, keeping the device-only keepOffline.
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

// tagsFromRemote reads a row's tags, none for a row without them.
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

// FolderFromRemote reads a folders row.
func FolderFromRemote(r map[string]any) Folder {
	sk := S(r, "sort_key")
	if sk == "" {
		sk = "V"
	}
	return Folder{ID: S(r, "id"), SpaceID: S(r, "space_id"), ParentFolderID: S(r, "parent_folder_id"), Name: S(r, "name"),
		SortKey: sk, CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"]),
		DefaultTemplateID: S(r, "default_template_id")}
}

// CommentFromRemote reads a comments row.
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

// FileFromRemote reads a files row. Its name is one the file can be kept
// by here, whatever the server has.
func FileFromRemote(r map[string]any) File {
	size, _ := r["size_bytes"].(float64)
	return File{ID: S(r, "id"), SpaceID: S(r, "space_id"), FolderID: S(r, "folder_id"), PageID: S(r, "page_id"), Name: fileName(S(r, "name")),
		StoragePath: S(r, "storage_path"), MimeType: S(r, "mime_type"), SizeBytes: int64(size),
		CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"]), DeletedAt: TimeOrZero(r["deleted_at"])}
}

// ProviderFromRemote reads a model_providers row.
func ProviderFromRemote(r map[string]any) Provider {
	p := Provider{ID: S(r, "id"), Preset: S(r, "preset"), Name: S(r, "name"), BaseURL: S(r, "base_url"),
		AuthKind: S(r, "auth_kind"), DefaultModel: S(r, "default_model"), CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"])}
	if p.Name == "" {
		p.Name = p.Preset
	}
	if p.AuthKind == "" {
		p.AuthKind = "api_key"
	}
	if b, ok := r["supports_tools"].(bool); ok {
		p.SupportsTools = &b
	}
	if ms, ok := r["models"].([]any); ok {
		for _, m := range ms {
			if s, ok := m.(string); ok {
				p.Models = append(p.Models, s)
			}
		}
	}
	return p
}

// pulled are the tables a pull from the server stores rows of, by id,
// with the time each last changed there.
var pulled = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true, "model_providers": true}

// HasRowAt reports whether this device has a row pulled from the server as
// it was at a time, to the millisecond kept here. One that cannot be read
// is not had: the row is stored again, as any other is (putRemote).
func (s *Store) HasRowAt(table, id string, at time.Time) bool {
	if !pulled[table] {
		return false
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ? AND updated_at = ?`, id, at.UnixMilli()).Scan(&n) // #nosec G202 -- table is one of pulled.
	return n > 0
}

// pulledTables are the tables whose rows keep, as pulled_at, the server's
// time of the row this device last took from the server, until the row
// changes here (unpulled): sync tells by it the rows of the server's it
// still has as they came, which it does not send again as its pages join
// the account (sync's newerThere).
var pulledTables = []string{"pages", "folders", "comments", "recordings", "files"}

// PeerKey marks, in a row stored with a PutRemote function, one that came
// from another device through iCloud Drive, not from the server: it is no
// copy of the server's, so it keeps no pulled_at, and counts as changed
// here.
const PeerKey = "_peer"

// putRemote stores a row that came from the server, or another device
// (PeerKey), with write, unless it has a change waiting here: that change
// wins and goes up on the next push. The check and the write are one
// transaction, so a change made in between, here or by leaflet mcp, is
// never stored over, and nor is one the check could not tell of. A row
// from the server keeps its time there as pulled_at, in the same
// transaction. It reports whether it stored the row.
func (s *Store) putRemote(entity, id string, r map[string]any, write func(tx *sql.Tx) error) (bool, error) {
	stored := false
	err := s.inTx(func(tx *sql.Tx) error {
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
		_, err = tx.Exec(`UPDATE `+entity+` SET pulled_at = ? WHERE id = ?`, at, id) // #nosec G202 -- entity is one of pulledTables.
		return err
	})
	return stored && err == nil, err
}

// unpulled has a row changed here no longer count as the server's copy
// (pulledTables), in the transaction of the change: its change goes up
// as the row's, whenever it goes. A table that keeps no such time is left.
func unpulled(q querier, entity, id string) error {
	if !slices.Contains(pulledTables, entity) {
		return nil
	}
	_, err := q.Exec(`UPDATE `+entity+` SET pulled_at = NULL WHERE id = ? AND pulled_at IS NOT NULL`, id) // #nosec G202 -- entity is one of pulledTables.
	return err
}

// RowPulledAt reports the server's time of a row this device took from the
// server, as it took it, and false once the row changed here since, came
// from another device, or was never the server's: a database from before
// kept no such time.
func (s *Store) RowPulledAt(table, id string) (time.Time, bool) {
	if !slices.Contains(pulledTables, table) {
		return time.Time{}, false
	}
	var at sql.NullInt64
	if err := s.db.QueryRow(`SELECT pulled_at FROM `+table+` WHERE id = ?`, id).Scan(&at); err != nil || !at.Valid { // #nosec G202 -- table is one of pulledTables.
		return time.Time{}, false
	}
	return time.UnixMilli(at.Int64), true
}

// PutRemotePage stores a page that came from the server, unless it has a
// change waiting here (putRemote), keeping whether it is kept offline.
func (s *Store) PutRemotePage(r map[string]any) error {
	id := S(r, "id")
	_, err := s.putRemote("pages", id, r, func(tx *sql.Tx) error {
		keep := true
		if err := tx.QueryRow(`SELECT keep_offline FROM pages WHERE id = ?`, id).Scan(&keep); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return writePage(tx, PageFromRemote(r, keep))
	})
	return err
}

// PutRemoteFolder stores a folder from the server, unless it has a change
// waiting here (putRemote).
func (s *Store) PutRemoteFolder(r map[string]any) error {
	f := FolderFromRemote(r)
	_, err := s.putRemote("folders", f.ID, r, func(tx *sql.Tx) error { return writeFolder(tx, f) })
	return err
}

// PutRemoteComment stores a comment from the server, unless it has a
// change waiting here (putRemote).
func (s *Store) PutRemoteComment(r map[string]any, authorName string) error {
	c := CommentFromRemote(r, authorName)
	_, err := s.putRemote("comments", c.ID, r, func(tx *sql.Tx) error { return putComment(tx, c) })
	return err
}

// PutRemoteFile stores a file from the server, unless it has a change
// waiting here (putRemote), keeping its copy here until the file is
// deleted.
func (s *Store) PutRemoteFile(r map[string]any) error {
	f := FileFromRemote(r)
	stored, err := s.putRemote("files", f.ID, r, func(tx *sql.Tx) error {
		if err := putFile(tx, s.Dir, f); err != nil || !f.DeletedAt.IsZero() {
			return err
		}
		// A file that lives keeps its stored copy: its deletion here gave
		// way to the server's row, as one the user may no longer delete
		// (sync's pushRow), and the removal queued with it would take
		// the copy the row still names.
		_, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage_delete' AND entity_id = ?`, "files/"+f.ID)
		return err
	})
	if stored && !f.DeletedAt.IsZero() {
		s.removeCopy(f.ID)
	}
	return err
}

// RecordingFromRemote reads a recordings row.
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

// PutRemoteRecording stores a recording from the server, unless it has a
// change waiting here (putRemote), keeping its audio on this device.
func (s *Store) PutRemoteRecording(r map[string]any) error {
	rec := RecordingFromRemote(r)
	_, err := s.putRemote("recordings", rec.ID, r, func(tx *sql.Tx) error {
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

// PutRemoteProvider stores a cloud model connection from the server.
func (s *Store) PutRemoteProvider(r map[string]any) error {
	return s.PutProvider(ProviderFromRemote(r))
}
