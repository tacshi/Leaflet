package store

import (
	"database/sql"
	"time"
)

// Conversions from local rows to Supabase rows. Column names follow
// supabase/migrations.

func iso(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// PageToRemote is a page as the pages table has it.
func PageToRemote(p Page) map[string]any {
	return map[string]any{
		"id": p.ID, "space_id": p.SpaceID, "parent_id": ns(p.ParentID), "folder_id": ns(p.FolderID),
		"title": p.Title, "icon": ns(p.Icon), "sort_key": p.SortKey, "created_by": ns(p.CreatedBy),
		"created_at": iso(p.CreatedAt), "deleted_at": iso(p.DeletedAt), "is_template": p.Template,
		"tags": NormalizeTags(p.Tags), "default_template_id": ns(p.DefaultTemplateID),
	}
}

// FolderToRemote is a folder as the folders table has it.
func FolderToRemote(f Folder) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "parent_folder_id": ns(f.ParentFolderID), "name": f.Name,
		"sort_key": f.SortKey, "created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt),
		"default_template_id": ns(f.DefaultTemplateID),
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
		"transcript": transcript, "language": ns(r.Language), "created_at": iso(r.CreatedAt),
	}
}

// FileToRemote is a file as the files table has it.
func FileToRemote(f File) map[string]any {
	return map[string]any{
		"id": f.ID, "space_id": f.SpaceID, "folder_id": ns(f.FolderID), "page_id": ns(f.PageID), "name": f.Name,
		"storage_path": ns(f.StoragePath), "mime_type": f.MimeType, "size_bytes": f.SizeBytes,
		"created_at": iso(f.CreatedAt), "deleted_at": iso(f.DeletedAt),
	}
}

// JobToRemote is an agent run as the ai_jobs table has it.
func JobToRemote(j Job, userID string) map[string]any {
	focus := j.FocusBlockIDs
	if focus == nil {
		focus = []string{}
	}
	var result any
	if j.Result != nil {
		result = j.Result
	}
	return map[string]any{
		"id": j.ID, "page_id": j.PageID, "user_id": userID, "surface": j.Surface, "focus_block_ids": focus,
		"comment_id": ns(j.CommentID), "conversation_id": ns(j.ConversationID), "provider_id": j.ProviderID,
		"model": j.Model, "status": j.Status, "request": j.Request, "result": result, "error": ns(j.Error),
		"created_at": iso(j.CreatedAt),
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

// PutRemotePage stores a page that came from the server, unless it has a
// change waiting here: that change wins and goes up on the next push. The
// check and the write are one transaction, so a change made in between,
// here or by leaflet mcp, is never overwritten.
func (s *Store) PutRemotePage(r map[string]any) error {
	id := S(r, "id")
	return s.inTx(func(tx *sql.Tx) error {
		if pending(tx, "pages", id) {
			return nil
		}
		keep := true
		_ = tx.QueryRow(`SELECT keep_offline FROM pages WHERE id = ?`, id).Scan(&keep)
		return writePage(tx, PageFromRemote(r, keep))
	})
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
