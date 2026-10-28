package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"leaflet/internal/doc"
)

// Folder groups pages and uploaded files in the space.
type Folder struct {
	ID             string
	SpaceID        string
	ParentFolderID string
	Name           string
	SortKey        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      time.Time
}

// Folders returns the folders not deleted, in sort order.
func (s *Store) Folders() ([]Folder, error) {
	rows, err := s.db.Query(`SELECT id, space_id, parent_folder_id, name, sort_key, created_at, updated_at, deleted_at FROM folders WHERE deleted_at IS NULL ORDER BY sort_key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Folder
	for rows.Next() {
		var f Folder
		var parent sql.NullString
		var c, u int64
		var d sql.NullInt64
		if err := rows.Scan(&f.ID, &f.SpaceID, &parent, &f.Name, &f.SortKey, &c, &u, &d); err != nil {
			return nil, err
		}
		f.ParentFolderID, f.CreatedAt, f.UpdatedAt, f.DeletedAt = parent.String, time.UnixMilli(c), time.UnixMilli(u), fromMs(d)
		out = append(out, f)
	}
	return out, rows.Err()
}

// PutFolder stores a folder row.
func (s *Store) PutFolder(f Folder) error { return writeFolder(s.db, f) }

func writeFolder(q querier, f Folder) error {
	_, err := q.Exec(`INSERT INTO folders (id, space_id, parent_folder_id, name, sort_key, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id,
		parent_folder_id = excluded.parent_folder_id, name = excluded.name, sort_key = excluded.sort_key,
		updated_at = excluded.updated_at, deleted_at = excluded.deleted_at`,
		f.ID, f.SpaceID, ns(f.ParentFolderID), f.Name, f.SortKey, ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt))
	return err
}

// CreateFolder creates a folder last in the space.
func (s *Store) CreateFolder(name string) (Folder, error) {
	existing, err := s.Folders()
	if err != nil {
		return Folder{}, err
	}
	last := ""
	if len(existing) > 0 {
		last = existing[len(existing)-1].SortKey
	}
	now := time.Now()
	f := Folder{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, Name: name, SortKey: KeyBetween(last, ""), CreatedAt: now, UpdatedAt: now}
	err = s.inTx(func(tx *sql.Tx) error {
		// As in CreatePage: the app may have changed accounts meanwhile.
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		return saveFolder(tx, f, "new folder")
	})
	if err != nil {
		return Folder{}, err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return f, nil
}

// saveFolder stores a folder changed here and queues it to go up, in the
// caller's transaction, as savePages does pages.
func saveFolder(tx *sql.Tx, f Folder, label string) error {
	if err := writeFolder(tx, f); err != nil {
		return err
	}
	return queue(tx, "folders", f.ID, FolderToRemote(f), label, "")
}

// RenameFolder renames a folder.
func (s *Store) RenameFolder(f Folder, name string) error {
	f.Name, f.UpdatedAt = name, time.Now()
	if err := s.inTx(func(tx *sql.Tx) error { return saveFolder(tx, f, "renamed folder") }); err != nil {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return nil
}

// DeleteFolder deletes a folder; its pages move to the top level.
func (s *Store) DeleteFolder(f Folder) error {
	now := time.Now()
	pages, err := s.queryPages(`folder_id = ?`, f.ID)
	if err != nil {
		return err
	}
	f.DeletedAt, f.UpdatedAt = now, now
	err = s.inTx(func(tx *sql.Tx) error {
		for _, p := range pages {
			p.FolderID, p.UpdatedAt = "", now
			if err := writePage(tx, p); err != nil {
				return err
			}
			if err := queue(tx, "pages", p.ID, PageToRemote(p), "moved", p.ID); err != nil {
				return err
			}
		}
		return saveFolder(tx, f, "deleted folder")
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	s.Notify("pages", "")
	return nil
}

// File is an uploaded file.
type File struct {
	ID          string
	SpaceID     string
	FolderID    string
	PageID      string
	Name        string
	StoragePath string
	LocalPath   string
	MimeType    string
	SizeBytes   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   time.Time
}

// Files returns the files not deleted, newest first.
func (s *Store) Files() ([]File, error) {
	return s.queryFiles(`deleted_at IS NULL ORDER BY created_at DESC`)
}

// AllFiles returns every file, deleted ones too.
func (s *Store) AllFiles() ([]File, error) { return s.queryFiles(`1 = 1 ORDER BY created_at DESC`) }

func (s *Store) queryFiles(where string) ([]File, error) {
	rows, err := s.db.Query(`SELECT id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at FROM files WHERE ` + where)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []File
	for rows.Next() {
		var f File
		var folder, page, sp, lp sql.NullString
		var c, u int64
		var d sql.NullInt64
		if err := rows.Scan(&f.ID, &f.SpaceID, &folder, &page, &f.Name, &sp, &lp, &f.MimeType, &f.SizeBytes, &c, &u, &d); err != nil {
			return nil, err
		}
		f.FolderID, f.PageID, f.StoragePath, f.LocalPath = folder.String, page.String, sp.String, lp.String
		f.CreatedAt, f.UpdatedAt, f.DeletedAt = time.UnixMilli(c), time.UnixMilli(u), fromMs(d)
		out = append(out, f)
	}
	return out, rows.Err()
}

// PutFile stores a file row.
func (s *Store) PutFile(f File) error { return putFile(s.db, f) }

func putFile(q querier, f File) error {
	_, err := q.Exec(`INSERT INTO files (id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET folder_id = excluded.folder_id,
		page_id = excluded.page_id, name = excluded.name, storage_path = excluded.storage_path,
		local_path = COALESCE(excluded.local_path, files.local_path), mime_type = excluded.mime_type,
		size_bytes = excluded.size_bytes, updated_at = excluded.updated_at, deleted_at = excluded.deleted_at`,
		f.ID, f.SpaceID, ns(f.FolderID), ns(f.PageID), f.Name, ns(f.StoragePath), ns(f.LocalPath), f.MimeType, f.SizeBytes,
		ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt))
	return err
}

// AddFile copies a file into the app and adds it to a folder (or page).
func (s *Store) AddFile(source, folderID, pageID, mime string) (File, error) {
	id := doc.NewRowID()
	name := filepath.Base(source)
	dir := filepath.Join(s.Dir, "files", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return File{}, err
	}
	local := filepath.Join(dir, name)
	if err := copyFile(source, local); err != nil {
		return File{}, err
	}
	info, _ := os.Stat(local)
	now := time.Now()
	f := File{ID: id, SpaceID: s.Session().SpaceID, FolderID: folderID, PageID: pageID, Name: name,
		LocalPath: local, MimeType: mime, CreatedAt: now, UpdatedAt: now}
	if info != nil {
		f.SizeBytes = info.Size()
	}
	f.StoragePath = f.SpaceID + "/" + id + "/" + name
	if err := s.PutFile(f); err != nil {
		return File{}, err
	}
	if err := s.Queue("files", id, FileToRemote(f), "new file", pageID); err != nil {
		return File{}, err
	}
	ct := mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	if err := s.Queue("storage", "files/"+id, map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": local, "content_type": ct}, "file waiting to upload", pageID); err != nil {
		return File{}, err
	}
	s.Notify("files", "")
	return f, nil
}

// DownloadedFile keeps a file downloaded from the server at path, which
// is this device's alone.
func (s *Store) DownloadedFile(id string, data []byte) (string, error) {
	var name string
	if err := s.db.QueryRow(`SELECT name FROM files WHERE id = ?`, id).Scan(&name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir, "files", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	local := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(local, data, 0o600); err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`UPDATE files SET local_path = ? WHERE id = ?`, local, id); err != nil {
		return "", err
	}
	s.Notify("files", "")
	return local, nil
}

// DeleteFile deletes a file, which cannot be undone: its copy here goes
// now, and its stored copy (in Storage, or iCloud Drive) once the
// deletion has gone up. An upload still waiting is dropped.
func (s *Store) DeleteFile(f File) error {
	now := time.Now()
	f.DeletedAt, f.UpdatedAt = now, now
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putFile(tx, f); err != nil {
			return err
		}
		if err := queue(tx, "files", f.ID, FileToRemote(f), "deleted file", ""); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+f.ID); err != nil {
			return err
		}
		if f.StoragePath == "" {
			return nil
		}
		return queue(tx, "storage_delete", "files/"+f.ID, map[string]any{"bucket": "files", "path": f.StoragePath, "file_id": f.ID}, "deleted file", "")
	})
	if err != nil {
		return err
	}
	s.removeCopy(f.ID)
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

// removeCopy removes a deleted file's copy on this device, if it is one
// the app made in its data folder: a path anywhere else is left alone.
func (s *Store) removeCopy(id string) {
	var local sql.NullString
	if err := s.db.QueryRow(`SELECT local_path FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(&local); err != nil || local.String == "" {
		return
	}
	dir := filepath.Join(s.Dir, "files", id)
	if rel, err := filepath.Rel(dir, local.String); err != nil || !filepath.IsLocal(rel) {
		return
	}
	if err := os.Remove(local.String); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	_ = os.Remove(dir) // only once it is empty
	_, _ = s.db.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, id)
}

func copyFile(from, to string) error {
	in, err := os.Open(from) // #nosec G304 -- the file the user picked to add.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(to) // #nosec G304 -- a path in the app's own files folder.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Comment is one comment of a thread anchored to a block.
type Comment struct {
	ID              string
	PageID          string
	BlockID         string
	ParentCommentID string
	AuthorID        string
	AuthorKind      string // user | agent
	AuthorName      string
	Body            string
	Quote           string
	Resolved        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       time.Time
}

// Thread is a first comment and its replies.
type Thread struct {
	Root    Comment
	Replies []Comment
}

// All is the root then the replies.
func (t Thread) All() []Comment { return append([]Comment{t.Root}, t.Replies...) }

const commentCols = `id, page_id, block_id, parent_comment_id, author_id, author_kind, author_name, body, quote, resolved, created_at, updated_at, deleted_at`

func scanComment(sc interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var parent, author, quote sql.NullString
	var cr, up int64
	var del sql.NullInt64
	err := sc.Scan(&c.ID, &c.PageID, &c.BlockID, &parent, &author, &c.AuthorKind, &c.AuthorName, &c.Body, &quote, &c.Resolved, &cr, &up, &del)
	c.ParentCommentID, c.AuthorID, c.Quote = parent.String, author.String, quote.String
	c.CreatedAt, c.UpdatedAt, c.DeletedAt = time.UnixMilli(cr), time.UnixMilli(up), fromMs(del)
	return c, err
}

// Comment returns one comment.
func (s *Store) Comment(id string) (Comment, error) {
	return scanComment(s.db.QueryRow(`SELECT `+commentCols+` FROM comments WHERE id = ?`, id))
}

// AllComments returns every comment, deleted ones too.
func (s *Store) AllComments() ([]Comment, error) {
	rows, err := s.db.Query(`SELECT ` + commentCols + ` FROM comments ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Threads returns a page's comment threads, oldest first.
func (s *Store) Threads(pageID string) ([]Thread, error) {
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM comments WHERE page_id = ? AND deleted_at IS NULL ORDER BY created_at`, pageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var all []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, c)
	}
	var out []Thread
	for _, c := range all {
		if c.ParentCommentID != "" {
			continue
		}
		t := Thread{Root: c}
		for _, r := range all {
			if r.ParentCommentID == c.ID {
				t.Replies = append(t.Replies, r)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutComment stores a comment row.
func (s *Store) PutComment(c Comment) error {
	_, err := s.db.Exec(`INSERT INTO comments (`+commentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET block_id = excluded.block_id, parent_comment_id = excluded.parent_comment_id,
		author_id = excluded.author_id, author_kind = excluded.author_kind, author_name = excluded.author_name,
		body = excluded.body, quote = excluded.quote, resolved = excluded.resolved, updated_at = excluded.updated_at,
		deleted_at = excluded.deleted_at`,
		c.ID, c.PageID, c.BlockID, ns(c.ParentCommentID), ns(c.AuthorID), c.AuthorKind, c.AuthorName, c.Body, ns(c.Quote),
		c.Resolved, ms(c.CreatedAt), ms(c.UpdatedAt), nt(c.DeletedAt))
	return err
}

func (s *Store) saveComment(c Comment, label string) error {
	if err := s.PutComment(c); err != nil {
		return err
	}
	if err := s.Queue("comments", c.ID, CommentToRemote(c), label, c.PageID); err != nil {
		return err
	}
	s.Notify("comments", c.PageID)
	return nil
}

// AddComment adds a comment, or a reply when parentID is set. An empty
// authorName is the user's.
func (s *Store) AddComment(pageID, blockID, body, parentID, quote, authorKind, authorName string) (Comment, error) {
	sess := s.Session()
	if authorKind == "" {
		authorKind = "user"
	}
	if authorName == "" {
		authorName = sess.DisplayName
	}
	now := time.Now()
	c := Comment{ID: doc.NewRowID(), PageID: pageID, BlockID: blockID, ParentCommentID: parentID, AuthorKind: authorKind,
		AuthorName: authorName, Body: body, Quote: quote, CreatedAt: now, UpdatedAt: now}
	if authorKind == "user" && sess.SignedIn {
		c.AuthorID = sess.UserID
	}
	label := "new comment"
	if parentID != "" {
		label = "reply"
	}
	return c, s.saveComment(c, label)
}

// SetResolved resolves or reopens a thread.
func (s *Store) SetResolved(rootID string, resolved bool) error {
	c, err := s.Comment(rootID)
	if err != nil {
		return err
	}
	c.Resolved, c.UpdatedAt = resolved, time.Now()
	label := "reopened"
	if resolved {
		label = "resolved thread"
	}
	return s.saveComment(c, label)
}

// DeleteComment deletes a comment.
func (s *Store) DeleteComment(id string) error {
	c, err := s.Comment(id)
	if err != nil {
		return err
	}
	now := time.Now()
	c.DeletedAt, c.UpdatedAt = now, now
	return s.saveComment(c, "deleted comment")
}

// Reanchor moves threads anchored to one block onto another.
func (s *Store) Reanchor(pageID, from, to string) error {
	threads, err := s.Threads(pageID)
	if err != nil {
		return err
	}
	for _, t := range threads {
		for _, c := range t.All() {
			if c.BlockID == from {
				c.BlockID, c.UpdatedAt = to, time.Now()
				if err := s.saveComment(c, "comment moved"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Segment is one timestamped piece of a transcript.
type Segment struct {
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

// Recording is the audio and transcript of a recording block.
type Recording struct {
	ID          string
	PageID      string
	BlockID     string
	AudioPath   string
	LocalPath   string
	DurationMs  int
	SttProvider string
	SttModel    string
	Status      string // recording | uploading | transcribing | ready | failed
	Transcript  []Segment
	Language    string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UploadedAt  time.Time
}

const recordingCols = `id, page_id, block_id, audio_path, local_path, duration_ms, stt_provider, stt_model, status, transcript, language, error, created_at, updated_at, uploaded_at`

func scanRecording(sc interface{ Scan(...any) error }) (Recording, error) {
	var r Recording
	var audio, local, lang, errText sql.NullString
	var transcript string
	var c, u int64
	var up sql.NullInt64
	err := sc.Scan(&r.ID, &r.PageID, &r.BlockID, &audio, &local, &r.DurationMs, &r.SttProvider, &r.SttModel, &r.Status, &transcript, &lang, &errText, &c, &u, &up)
	r.AudioPath, r.LocalPath, r.Language, r.Error = audio.String, local.String, lang.String, errText.String
	r.CreatedAt, r.UpdatedAt, r.UploadedAt = time.UnixMilli(c), time.UnixMilli(u), fromMs(up)
	_ = json.Unmarshal([]byte(transcript), &r.Transcript)
	return r, err
}

// Recording returns one recording.
func (s *Store) Recording(id string) (Recording, error) {
	return scanRecording(s.db.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id))
}

// Recordings returns every recording, newest first.
func (s *Store) Recordings() ([]Recording, error) {
	rows, err := s.db.Query(`SELECT ` + recordingCols + ` FROM recordings ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutRecording stores a recording row.
func (s *Store) PutRecording(r Recording) error {
	transcript, _ := json.Marshal(r.Transcript)
	if r.Transcript == nil {
		transcript = []byte("[]")
	}
	_, err := s.db.Exec(`INSERT INTO recordings (`+recordingCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET block_id = excluded.block_id, audio_path = excluded.audio_path,
		local_path = COALESCE(excluded.local_path, recordings.local_path), duration_ms = excluded.duration_ms,
		stt_provider = excluded.stt_provider, stt_model = excluded.stt_model, status = excluded.status,
		transcript = excluded.transcript, language = excluded.language, error = excluded.error,
		updated_at = excluded.updated_at, uploaded_at = excluded.uploaded_at`,
		r.ID, r.PageID, r.BlockID, ns(r.AudioPath), ns(r.LocalPath), r.DurationMs, r.SttProvider, r.SttModel, r.Status,
		string(transcript), ns(r.Language), ns(r.Error), ms(r.CreatedAt), ms(r.UpdatedAt), nt(r.UploadedAt))
	return err
}

// CreateRecording starts a recording.
func (s *Store) CreateRecording(pageID, blockID, provider, model, localPath, language string) (Recording, error) {
	now := time.Now()
	r := Recording{ID: doc.NewRowID(), PageID: pageID, BlockID: blockID, LocalPath: localPath, SttProvider: provider,
		SttModel: model, Status: "recording", Language: language, CreatedAt: now, UpdatedAt: now}
	if err := s.PutRecording(r); err != nil {
		return Recording{}, err
	}
	s.Notify("recordings", pageID)
	return r, nil
}

// UpdateRecording changes a recording and queues it once it is no longer
// recording.
func (s *Store) UpdateRecording(id string, fn func(*Recording)) (Recording, error) {
	r, err := s.Recording(id)
	if err != nil {
		return Recording{}, err
	}
	fn(&r)
	r.UpdatedAt = time.Now()
	if err := s.PutRecording(r); err != nil {
		return Recording{}, err
	}
	if r.Status != "recording" {
		label := "transcript"
		if r.AudioPath == "" && r.LocalPath != "" {
			label = "audio waiting to upload"
		}
		if err := s.Queue("recordings", r.ID, RecordingToRemote(r), label, r.PageID); err != nil {
			return Recording{}, err
		}
	}
	s.Notify("recordings", r.PageID)
	return r, nil
}

// QueueUpload queues a recording's audio for upload.
func (s *Store) QueueUpload(r Recording) error {
	if r.LocalPath == "" {
		return nil
	}
	return s.Queue("storage", "recordings/"+r.ID, map[string]any{
		"bucket": "recordings", "local_path": r.LocalPath, "content_type": "audio/wav", "recording_id": r.ID,
	}, "audio waiting to upload", r.PageID)
}
