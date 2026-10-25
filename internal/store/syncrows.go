package store

import (
	"encoding/json"
	"time"
)

// What sync writes: rows that came from the server, and the space a
// device's pages move into after sign-in.

// AllPages returns every page, deleted ones too.
func (s *Store) AllPages() ([]Page, error) { return s.queryPages(`1 = 1 ORDER BY sort_key`) }

// PutSpace stores a space row.
func (s *Store) PutSpace(id, name, owner string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO spaces (id, name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, owner_id = excluded.owner_id, updated_at = excluded.updated_at`,
		id, name, owner, now, now)
	return err
}

// MoveSpace moves everything of one space into another.
func (s *Store) MoveSpace(from, to string) error {
	for _, q := range []string{
		`UPDATE pages SET space_id = ? WHERE space_id = ?`,
		`UPDATE folders SET space_id = ? WHERE space_id = ?`,
		`UPDATE files SET space_id = ? WHERE space_id = ?`,
	} {
		if _, err := s.db.Exec(q, to, from); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`DELETE FROM spaces WHERE id = ?`, from)
	s.Notify("*", "")
	return err
}

// PutRemoteFolder stores a folder from the server.
func (s *Store) PutRemoteFolder(r map[string]any) error { return s.PutFolder(FolderFromRemote(r)) }

// PutRemoteComment stores a comment from the server.
func (s *Store) PutRemoteComment(r map[string]any, authorName string) error {
	return s.PutComment(CommentFromRemote(r, authorName))
}

// PutRemoteFile stores a file from the server, keeping its copy here.
func (s *Store) PutRemoteFile(r map[string]any) error { return s.PutFile(FileFromRemote(r)) }

// PutRemoteRecording stores a recording from the server, keeping its
// audio on this device.
func (s *Store) PutRemoteRecording(r map[string]any) error {
	rec := Recording{ID: S(r, "id"), PageID: S(r, "page_id"), BlockID: S(r, "block_id"), AudioPath: S(r, "audio_path"),
		SttProvider: S(r, "stt_provider"), SttModel: S(r, "stt_model"), Status: S(r, "status"), Language: S(r, "language"),
		CreatedAt: Time(r["created_at"]), UpdatedAt: Time(r["updated_at"])}
	if d, ok := r["duration_ms"].(float64); ok {
		rec.DurationMs = int(d)
	}
	if rec.Status == "" {
		rec.Status = "ready"
	}
	if raw, err := json.Marshal(r["transcript"]); err == nil {
		_ = json.Unmarshal(raw, &rec.Transcript)
	}
	if old, err := s.Recording(rec.ID); err == nil {
		rec.LocalPath, rec.UploadedAt = old.LocalPath, old.UploadedAt
	}
	return s.PutRecording(rec)
}

// PutRemoteProvider stores a cloud model connection from the server.
func (s *Store) PutRemoteProvider(r map[string]any) error {
	return s.PutProvider(ProviderFromRemote(r))
}

// ClearLocalPath forgets a recording's audio on this device.
func (s *Store) ClearLocalPath(id string) error {
	_, err := s.db.Exec(`UPDATE recordings SET local_path = NULL WHERE id = ?`, id)
	return err
}

// PushedContent records what the server stored for a page: the content
// this device now has, the base both agree on, and its time.
func (s *Store) PushedContent(pageID, content, plain, base string, remoteAt time.Time) error {
	_, err := s.db.Exec(`UPDATE page_contents SET content = ?, plain_text = ?, base_content = ?, remote_updated_at = ? WHERE page_id = ?`,
		content, plain, base, remoteAt.UnixMilli(), pageID)
	return err
}

// MembersOrNil returns a page's members, or nil.
func (s *Store) MembersOrNil(pageID string) []Member {
	m, _ := s.Members(pageID)
	return m
}
