package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
)

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
	// Kept is whether the file kept its contents as it was deleted, in the bin. One deleted by a Leaflet from before
	// the bin went with them.
	Kept bool
	// PurgedAt is when the file was deleted for good, out of the bin, its contents with it.
	PurgedAt time.Time
}

// Gone reports whether the file's contents are gone: it was deleted for good, or deleted without the bin keeping
// them.
func (f File) Gone() bool { return !f.PurgedAt.IsZero() || !f.DeletedAt.IsZero() && !f.Kept }

func (s *Store) Files() ([]File, error) {
	return s.queryFiles(`deleted_at IS NULL AND purged_at IS NULL ORDER BY created_at DESC`)
}

func (s *Store) AllFiles() ([]File, error) { return s.queryFiles(`1 = 1 ORDER BY created_at DESC`) }

func (s *Store) File(id string) (File, error) { return fileIn(s.db, s.Dir, id) }

func fileIn(q querier, dir, id string) (File, error) {
	files, err := filesIn(q, dir, `id = ?`, id)
	if err == nil && len(files) == 0 {
		err = errNotFound
	}
	if err != nil {
		return File{}, err
	}
	return files[0], nil
}

// changeFile changes a file in tx as fn has it, and queues what that changed of it, with the always columns among
// them. fn sees the file's UpdatedAt as now, and reports whether it changed it. It is the file as it is then, and
// whether fn changed it; a file not here is errNotFound.
func changeFile(tx *sql.Tx, dir, id, label string, now time.Time, fn func(tx *sql.Tx, f *File) (bool, error), always ...string) (File, bool, error) {
	f, err := fileIn(tx, dir, id)
	if err != nil {
		return File{}, false, err
	}
	old := FileToRemote(f)
	was := f.UpdatedAt
	f.UpdatedAt = now
	if changed, err := fn(tx, &f); err != nil || !changed {
		f.UpdatedAt = was
		return f, false, err
	}
	if err := putFile(tx, dir, f); err != nil {
		return f, false, err
	}
	return f, true, queueChange(tx, "files", f.ID, old, FileToRemote(f), label, f.PageID, always...)
}

// updateFile is changeFile now, in a transaction of its own, telling those who watch the outbox and the files once fn
// changed the file.
func (s *Store) updateFile(id, label string, fn func(tx *sql.Tx, f *File) (bool, error)) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		_, changed, err = changeFile(tx, s.Dir, id, label, time.Now(), fn)
		return err
	})
	if err != nil || !changed {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

func (s *Store) queryFiles(where string, args ...any) ([]File, error) {
	return filesIn(s.db, s.Dir, where, args...)
}

func filesIn(q querier, dir, where string, args ...any) ([]File, error) {
	rows, err := q.Query(`SELECT id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at, kept, purged_at FROM files WHERE `+where, args...) // #nosec G202
	return collect(rows, err, func(sc scanner) (File, error) {
		var f File
		var folder, page, sp, lp sql.NullString
		var c, u int64
		var d, purged sql.NullInt64
		err := sc.Scan(&f.ID, &f.SpaceID, &folder, &page, &f.Name, &sp, &lp, &f.MimeType, &f.SizeBytes, &c, &u, &d, &f.Kept, &purged)
		f.FolderID, f.PageID, f.StoragePath, f.LocalPath = folder.String, page.String, sp.String, absPath(dir, lp.String)
		f.CreatedAt, f.UpdatedAt, f.DeletedAt, f.PurgedAt = time.UnixMilli(c), time.UnixMilli(u), fromMs(d), fromMs(purged)
		return f, err
	})
}

func (s *Store) PutFile(f File) error { return putFile(s.db, s.Dir, f) }

func putFile(q querier, dir string, f File) error {
	// A file deleted for good was deleted no later: a restore that crossed the deletion on another device, whose row
	// came with its purged_at and not its deletion, is no file to list.
	if f.DeletedAt.IsZero() {
		f.DeletedAt = f.PurgedAt
	}
	_, err := q.Exec(`INSERT INTO files (id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at, kept, purged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id,
		folder_id = excluded.folder_id, page_id = excluded.page_id, name = excluded.name, storage_path = excluded.storage_path,
		local_path = COALESCE(excluded.local_path, files.local_path), mime_type = excluded.mime_type,
		size_bytes = excluded.size_bytes, updated_at = excluded.updated_at, deleted_at = excluded.deleted_at,
		kept = excluded.kept, purged_at = COALESCE(files.purged_at, excluded.purged_at)`,
		f.ID, f.SpaceID, ns(f.FolderID), ns(f.PageID), f.Name, ns(f.StoragePath), ns(relPath(dir, f.LocalPath)), f.MimeType, f.SizeBytes,
		ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt), f.Kept && !f.DeletedAt.IsZero(), nt(f.PurgedAt))
	return err
}

const storageKeyChars = "_/!.*'() &$=@;:+,?-"

func inStorageKey(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(storageKeyChars, r)
}

func StorageKey(spaceID, id, name string) string {
	return spaceID + "/" + id + "/" + strings.Map(func(r rune) rune {
		if r != '/' && inStorageKey(r) {
			return r
		}
		return '_'
	}, name)
}

func ValidStorageKey(key string) bool {
	return key != "" && strings.IndexFunc(key, func(r rune) bool { return !inStorageKey(r) }) < 0
}

func (s *Store) RekeyFile(id string) (string, error) {
	var key, pageID string
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := fileIn(tx, s.Dir, id)
		if err != nil {
			return err
		}
		if key = f.StoragePath; ValidStorageKey(key) {
			return nil
		}
		old := FileToRemote(f)
		f.StoragePath = StorageKey(f.SpaceID, f.ID, f.Name)
		key, pageID, changed = f.StoragePath, f.PageID, true
		if err := putFile(tx, s.Dir, f); err != nil {
			return err
		}
		row := FileToRemote(f)
		_, queued, err := queuedCols(tx, "files", f.ID)
		if err != nil {
			return err
		}
		if queued {
			err = requeueRow(tx, "files", f.ID, old, adopting(row, f.UpdatedAt), "", f.PageID)
		} else {
			err = queueChange(tx, "files", f.ID, old, row, "file", f.PageID)
		}
		if err != nil {
			return err
		}
		return rekeyUpload(tx, f)
	})
	if err == nil && changed {
		s.Notify("outbox", pageID)
		s.Notify("files", "")
	}
	return key, err
}

func rekeyUpload(tx *sql.Tx, f File) error {
	var raw string
	err := tx.QueryRow(`SELECT payload FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+f.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var upload map[string]any
	if err := json.Unmarshal([]byte(raw), &upload); err != nil {
		return err
	}
	upload["path"] = f.StoragePath
	return queue(tx, "storage", "files/"+f.ID, upload, "", f.PageID)
}

func (s *Store) AddFile(source, folderID, pageID, mime string) (File, error) {
	id := doc.NewRowID()
	name := fileName(source)
	dir := filepath.Join(s.Dir, "files", id)
	local := filepath.Join(dir, name)
	size, err := copyFile(source, local)
	if err != nil {
		_ = os.RemoveAll(dir)
		return File{}, err
	}
	now := time.Now()
	f := File{ID: id, SpaceID: s.Session().SpaceID, FolderID: folderID, PageID: pageID, Name: name,
		LocalPath: local, MimeType: mime, SizeBytes: size, CreatedAt: now, UpdatedAt: now}
	f.StoragePath = StorageKey(f.SpaceID, id, name)
	err = s.inTx(func(tx *sql.Tx) error {
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		if err := putFile(tx, s.Dir, f); err != nil {
			return err
		}
		if err := queue(tx, "files", id, FileToRemote(f), "new file", pageID); err != nil {
			return err
		}
		return queue(tx, "storage", "files/"+id, fileUpload(s.Dir, f), "file waiting to upload", pageID)
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return File{}, err
	}
	s.Notify("outbox", pageID)
	s.Notify("files", "")
	return f, nil
}

func fileUpload(dir string, f File) map[string]any {
	ct := f.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	return map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": relPath(dir, f.LocalPath), "content_type": ct, "file_id": f.ID}
}

// FileURL is the url of a block of a file the app keeps: the file_id finds
// the file, and its place on this device stays out of the page, which
// other devices and people read.
func FileURL(id, name string) string { return "file://" + id + "/" + name }

// FileBlock is the block that puts a file the app keeps in a page: a picture
// for an image, a file card for any other file.
func FileBlock(f File) *doc.Node {
	if doc.ImageExts[strings.ToLower(filepath.Ext(f.Name))] {
		return &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
			"url": FileURL(f.ID, f.Name), "align": "center", "alt": f.Name, "file_id": f.ID,
		}}
	}
	return &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{
		"url": FileURL(f.ID, f.Name), "name": f.Name, "file_id": f.ID, "size": f.SizeBytes, "mime": f.MimeType,
	}}
}

func fileName(name string) string {
	name = filepath.Base(name)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "file"
	}
	if name = icloud.PortableName(name); !filepath.IsLocal(name) {
		return "file"
	}
	return name
}

func (s *Store) DownloadedFile(id string, data []byte) (string, error) {
	return s.DownloadedFileFrom(id, bytes.NewReader(data))
}

func (s *Store) DownloadedFileFrom(id string, r io.Reader) (string, error) {
	var name string
	if err := s.db.QueryRow(`SELECT name FROM files WHERE id = ? AND purged_at IS NULL`, id).Scan(&name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir, "files", id)
	local := filepath.Join(dir, fileName(name))
	if err := writeFrom(local, r); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	res, err := s.db.Exec(`UPDATE files SET local_path = ? WHERE id = ? AND purged_at IS NULL`, relPath(s.Dir, local), id)
	if err == nil {
		var n int64
		if n, err = res.RowsAffected(); err == nil && n == 0 {
			err = errNotFound
		}
	}
	if err != nil {
		s.removeLocalCopy(local)
		return "", err
	}
	s.Notify("files", "")
	return local, nil
}

// DeleteFile deletes a file for good, its contents with it, at once, as one just added that could not be placed: in
// the bin or not.
func (s *Store) DeleteFile(f File) error {
	var copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		copies, err = purgeFile(tx, s.Dir, f.ID, time.Now(), WipeNow, func(File) bool { return true })
		return err
	})
	if err != nil {
		return err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

// removeCopy removes the copy here of a file whose contents are gone (Gone), and reports whether it did: a file in the
// bin keeps it, to come back with it.
func (s *Store) removeCopy(id string) bool {
	var local sql.NullString
	if err := s.db.QueryRow(`SELECT local_path FROM files WHERE id = ? AND (purged_at IS NOT NULL OR deleted_at IS NOT NULL AND NOT kept)`,
		id).Scan(&local); err != nil || local.String == "" {
		return false
	}
	if !s.removeLocalCopy(s.Path(local.String)) {
		return false
	}
	_, err := s.db.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, id)
	return err == nil
}
