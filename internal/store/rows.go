package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	// DefaultTemplateID is the template the folder's new pages start
	// from, or "" for blank ones.
	DefaultTemplateID string
}

const folderCols = `id, space_id, parent_folder_id, name, sort_key, created_at, updated_at, deleted_at, default_template_id`

func scanFolder(sc interface{ Scan(...any) error }) (Folder, error) {
	var f Folder
	var parent, template sql.NullString
	var c, u int64
	var d sql.NullInt64
	err := sc.Scan(&f.ID, &f.SpaceID, &parent, &f.Name, &f.SortKey, &c, &u, &d, &template)
	f.ParentFolderID, f.CreatedAt, f.UpdatedAt, f.DeletedAt = parent.String, time.UnixMilli(c), time.UnixMilli(u), fromMs(d)
	f.DefaultTemplateID = template.String
	return f, err
}

// Folders returns the folders not deleted, in sort order.
func (s *Store) Folders() ([]Folder, error) {
	rows, err := s.db.Query(`SELECT ` + folderCols + ` FROM folders WHERE deleted_at IS NULL ORDER BY sort_key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PutFolder stores a folder row.
func (s *Store) PutFolder(f Folder) error { return writeFolder(s.db, f) }

func writeFolder(q querier, f Folder) error {
	_, err := q.Exec(`INSERT INTO folders (`+folderCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id,
		parent_folder_id = excluded.parent_folder_id, name = excluded.name, sort_key = excluded.sort_key,
		updated_at = excluded.updated_at, deleted_at = excluded.deleted_at, default_template_id = excluded.default_template_id`,
		f.ID, f.SpaceID, ns(f.ParentFolderID), f.Name, f.SortKey, ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt), ns(f.DefaultTemplateID))
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
// caller's transaction, as savePage does a page.
func saveFolder(tx *sql.Tx, f Folder, label string) error {
	var old map[string]any
	switch was, err := scanFolder(tx.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, f.ID)); {
	case err == nil:
		old = FolderToRemote(was)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := writeFolder(tx, f); err != nil {
		return err
	}
	return queueChange(tx, "folders", f.ID, old, FolderToRemote(f), label, "")
}

// changeFolder changes a folder as stored, not as the caller last read it,
// so a change made since to another of its fields is kept. fn reports
// whether it changed anything.
func (s *Store) changeFolder(id, label string, fn func(*Folder) bool) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := scanFolder(tx.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if changed = fn(&f); !changed {
			return nil
		}
		f.UpdatedAt = time.Now()
		return saveFolder(tx, f, label)
	})
	if err != nil || !changed {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return nil
}

// RenameFolder renames a folder.
func (s *Store) RenameFolder(f Folder, name string) error {
	return s.changeFolder(f.ID, "renamed folder", func(f *Folder) bool {
		f.Name = name
		return true
	})
}

// SetFolderDefaultTemplate sets the template a folder's new pages start
// from, "" for blank ones.
func (s *Store) SetFolderDefaultTemplate(f Folder, templateID string) error {
	return s.changeFolder(f.ID, "set default template", func(f *Folder) bool {
		if f.DefaultTemplateID == templateID {
			return false
		}
		f.DefaultTemplateID = templateID
		return true
	})
}

// DeleteFolder deletes a folder; its pages and files move to the top
// level.
func (s *Store) DeleteFolder(f Folder) error {
	now := time.Now()
	// What the folder holds is read in the transaction that changes it,
	// so a change made to a page meanwhile, as a rename, is kept.
	err := s.inTx(func(tx *sql.Tx) error {
		pages, err := pagesIn(tx, `folder_id = ?`, f.ID)
		if err != nil {
			return err
		}
		for _, p := range pages {
			p.FolderID, p.UpdatedAt = "", now
			if err := savePage(tx, p, "moved"); err != nil {
				return err
			}
		}
		files, err := filesIn(tx, `folder_id = ? AND deleted_at IS NULL`, f.ID)
		if err != nil {
			return err
		}
		// Its files go to the top level too, where the sidebar shows them.
		// Only the move goes up, so it does not undo a deletion made
		// elsewhere meanwhile.
		for _, file := range files {
			old := FileToRemote(file)
			file.FolderID, file.UpdatedAt = "", now
			if err := putFile(tx, file); err != nil {
				return err
			}
			if err := queueChange(tx, "files", file.ID, old, FileToRemote(file), "moved file", file.PageID); err != nil {
				return err
			}
		}
		// As stored, as in changeFolder.
		f, err := scanFolder(tx.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, f.ID))
		if err != nil {
			return err
		}
		f.DeletedAt, f.UpdatedAt = now, now
		return saveFolder(tx, f, "deleted folder")
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	s.Notify("pages", "")
	s.Notify("files", "")
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

// File returns a file, deleted or not.
func (s *Store) File(id string) (File, error) {
	files, err := s.queryFiles(`id = ?`, id)
	if err == nil && len(files) == 0 {
		err = errNotFound
	}
	if err != nil {
		return File{}, err
	}
	return files[0], nil
}

func (s *Store) queryFiles(where string, args ...any) ([]File, error) {
	return filesIn(s.db, where, args...)
}

// filesIn is queryFiles in the database or a transaction.
func filesIn(q querier, where string, args ...any) ([]File, error) {
	rows, err := q.Query(`SELECT id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at FROM files WHERE `+where, args...)
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
	name := fileName(source)
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
	ct := mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	// The file and its entries in the outbox are stored together, as a
	// page is: a file without them would never go to the server.
	err := s.inTx(func(tx *sql.Tx) error {
		// As in CreatePage: the app may have changed accounts meanwhile.
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		if err := putFile(tx, f); err != nil {
			return err
		}
		if err := queue(tx, "files", id, FileToRemote(f), "new file", pageID); err != nil {
			return err
		}
		return queue(tx, "storage", "files/"+id, map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": local, "content_type": ct}, "file waiting to upload", pageID)
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return File{}, err
	}
	s.Notify("outbox", pageID)
	s.Notify("files", "")
	return f, nil
}

// fileName is a file's name as it may be kept in its folder here: its last
// element, so a name from the server such as "../../x" never puts the file
// outside the app's files folder, or "file" when that is none. A
// backslash, which a name may have on macOS, becomes "_": the iCloud
// Drive folder takes no name with one (icloud.Name), so the file would
// never sync through it.
func fileName(name string) string {
	name = strings.ReplaceAll(filepath.Base(name), `\`, "_")
	if name == "." || !filepath.IsLocal(name) {
		return "file"
	}
	return name
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
	local := filepath.Join(dir, fileName(name))
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
// deletion has gone up. An upload still waiting is dropped. The file is
// deleted as stored, not as the caller last read it, and only its
// deletion goes up, so a change made since, here or elsewhere, is kept.
func (s *Store) DeleteFile(f File) error {
	if err := s.inTx(func(tx *sql.Tx) error { return deleteFile(tx, f.ID, time.Now()) }); err != nil {
		return err
	}
	s.removeCopy(f.ID)
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

// deleteFile is DeleteFile in the caller's transaction, but for the copy
// here, which the caller removes once it commits. A file that is gone has
// nothing to delete.
func deleteFile(tx *sql.Tx, id string, now time.Time) error {
	files, err := filesIn(tx, `id = ?`, id)
	if err != nil || len(files) == 0 {
		return err
	}
	f := files[0]
	old := FileToRemote(f)
	f.DeletedAt, f.UpdatedAt = now, now
	if err := putFile(tx, f); err != nil {
		return err
	}
	// Its entries go with its page's, so removing a shared page removes
	// them too.
	if err := queueChange(tx, "files", f.ID, old, FileToRemote(f), "deleted file", f.PageID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+f.ID); err != nil {
		return err
	}
	if f.StoragePath == "" {
		return nil
	}
	return queue(tx, "storage_delete", "files/"+f.ID, map[string]any{"bucket": "files", "path": f.StoragePath, "file_id": f.ID}, "deleted file", f.PageID)
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

// HasGuestComments reports whether a page has a comment a visitor of its
// public link posted, deleted ones too.
func (s *Store) HasGuestComments(pageID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE page_id = ? AND author_kind = 'guest'`, pageID).Scan(&n)
	return n > 0
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

// Threads returns a page's comment threads, oldest first. A thread whose
// first comment was deleted stays while it has replies, that comment with
// DeletedAt set, so the replies stay readable: show it as deleted, never
// its text.
func (s *Store) Threads(pageID string) ([]Thread, error) {
	rows, err := s.db.Query(`SELECT `+commentCols+` FROM comments WHERE page_id = ? ORDER BY created_at`, pageID)
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
			if r.ParentCommentID == c.ID && r.DeletedAt.IsZero() {
				t.Replies = append(t.Replies, r)
			}
		}
		if !c.DeletedAt.IsZero() && len(t.Replies) == 0 {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutComment stores a comment row.
func (s *Store) PutComment(c Comment) error { return putComment(s.db, c) }

func putComment(q querier, c Comment) error {
	_, err := q.Exec(`INSERT INTO comments (`+commentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET block_id = excluded.block_id, parent_comment_id = excluded.parent_comment_id,
		author_id = excluded.author_id, author_kind = excluded.author_kind, author_name = excluded.author_name,
		body = excluded.body, quote = excluded.quote, resolved = excluded.resolved, updated_at = excluded.updated_at,
		deleted_at = excluded.deleted_at`,
		c.ID, c.PageID, c.BlockID, ns(c.ParentCommentID), ns(c.AuthorID), c.AuthorKind, c.AuthorName, c.Body, ns(c.Quote),
		c.Resolved, ms(c.CreatedAt), ms(c.UpdatedAt), nt(c.DeletedAt))
	return err
}

// changeComments changes the comments where says, as stored, and queues
// the columns fn changed, in one transaction: a resolve made offline then
// no longer undoes a delete made elsewhere, nor a delete a resolve. It
// returns how many it changed.
func (s *Store) changeComments(label string, fn func(*Comment), where string, args ...any) (int, error) {
	var all []Comment
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT `+commentCols+` FROM comments WHERE `+where, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			c, err := scanComment(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			all = append(all, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		now := time.Now()
		for _, c := range all {
			old := CommentToRemote(c)
			fn(&c)
			c.UpdatedAt = now
			if err := putComment(tx, c); err != nil {
				return err
			}
			if err := queueChange(tx, "comments", c.ID, old, CommentToRemote(c), label, c.PageID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(all) == 0 {
		return 0, err
	}
	s.Notify("outbox", all[0].PageID)
	s.Notify("comments", all[0].PageID)
	return len(all), nil
}

// saveComment stores a comment changed here and queues it to go up, in one
// transaction.
func (s *Store) saveComment(c Comment, label string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := putComment(tx, c); err != nil {
			return err
		}
		return queue(tx, "comments", c.ID, CommentToRemote(c), label, c.PageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", c.PageID)
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
	label := "reopened"
	if resolved {
		label = "resolved thread"
	}
	n, err := s.changeComments(label, func(c *Comment) { c.Resolved = resolved }, `id = ?`, rootID)
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	return err
}

// DeleteComment deletes a comment.
func (s *Store) DeleteComment(id string) error {
	n, err := s.changeComments("deleted comment", func(c *Comment) { c.DeletedAt = time.Now() }, `id = ?`, id)
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	return err
}

// Reanchor moves threads anchored to one block onto another.
func (s *Store) Reanchor(pageID, from, to string) error {
	_, err := s.changeComments("comment moved", func(c *Comment) { c.BlockID = to }, `page_id = ? AND block_id = ?`, pageID, from)
	return err
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
func (s *Store) PutRecording(r Recording) error { return putRecording(s.db, r) }

func putRecording(q querier, r Recording) error {
	transcript, _ := json.Marshal(r.Transcript)
	if r.Transcript == nil {
		transcript = []byte("[]")
	}
	_, err := q.Exec(`INSERT INTO recordings (`+recordingCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
// recording, in one transaction, over the recording as stored then.
func (s *Store) UpdateRecording(id string, fn func(*Recording)) (Recording, error) {
	var r Recording
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		if r, err = scanRecording(tx.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id)); err != nil {
			return err
		}
		fn(&r)
		r.UpdatedAt = time.Now()
		if err := putRecording(tx, r); err != nil {
			return err
		}
		if r.Status == "recording" {
			return nil
		}
		label := "transcript"
		if r.AudioPath == "" && r.LocalPath != "" {
			label = "audio waiting to upload"
		}
		return queue(tx, "recordings", r.ID, RecordingToRemote(r), label, r.PageID)
	})
	if err != nil {
		return Recording{}, err
	}
	if r.Status != "recording" {
		s.Notify("outbox", r.PageID)
	}
	s.Notify("recordings", r.PageID)
	return r, nil
}

// QueueUpload queues a recording's audio for upload.
func (s *Store) QueueUpload(r Recording) error {
	if r.LocalPath == "" {
		return nil
	}
	return s.Queue("storage", "recordings/"+r.ID, uploadOf(r), "audio waiting to upload", r.PageID)
}

// uploadOf is the entry that uploads a recording's audio on this device.
func uploadOf(r Recording) map[string]any {
	return map[string]any{"bucket": "recordings", "local_path": r.LocalPath, "content_type": "audio/wav", "recording_id": r.ID}
}
