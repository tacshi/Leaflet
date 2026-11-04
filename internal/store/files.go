package store

import (
	"bytes"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
)

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
	return filesIn(s.db, s.Dir, where, args...)
}

// filesIn is queryFiles in the database or a transaction.
func filesIn(q querier, dir, where string, args ...any) ([]File, error) {
	rows, err := q.Query(`SELECT id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at FROM files WHERE `+where, args...) // #nosec G202 -- where is a literal of this package's, its values args.
	return collect(rows, err, func(sc scanner) (File, error) {
		var f File
		var folder, page, sp, lp sql.NullString
		var c, u int64
		var d sql.NullInt64
		err := sc.Scan(&f.ID, &f.SpaceID, &folder, &page, &f.Name, &sp, &lp, &f.MimeType, &f.SizeBytes, &c, &u, &d)
		f.FolderID, f.PageID, f.StoragePath, f.LocalPath = folder.String, page.String, sp.String, absPath(dir, lp.String)
		f.CreatedAt, f.UpdatedAt, f.DeletedAt = time.UnixMilli(c), time.UnixMilli(u), fromMs(d)
		return f, err
	})
}

// PutFile stores a file row as it is, without queueing it to go up: for
// tests, which set rows up so.
func (s *Store) PutFile(f File) error { return putFile(s.db, s.Dir, f) }

func putFile(q querier, dir string, f File) error {
	_, err := q.Exec(`INSERT INTO files (id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id,
		folder_id = excluded.folder_id, page_id = excluded.page_id, name = excluded.name, storage_path = excluded.storage_path,
		local_path = COALESCE(excluded.local_path, files.local_path), mime_type = excluded.mime_type,
		size_bytes = excluded.size_bytes, updated_at = excluded.updated_at, deleted_at = excluded.deleted_at`,
		f.ID, f.SpaceID, ns(f.FolderID), ns(f.PageID), f.Name, ns(f.StoragePath), ns(relPath(dir, f.LocalPath)), f.MimeType, f.SizeBytes,
		ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt))
	return err
}

// AddFile copies a file into the app and adds it to a folder (or page).
// The copy is whole on the disk before the row naming it is stored, and
// is removed if the row is not.
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
	f.StoragePath = f.SpaceID + "/" + id + "/" + name
	// The file and its entries in the outbox are stored together, as a
	// page is: a file without them would never go to the server.
	err = s.inTx(func(tx *sql.Tx) error {
		// As in CreatePage: the app may have changed accounts meanwhile.
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

// fileUpload is the entry that uploads a file's contents from this device.
func fileUpload(dir string, f File) map[string]any {
	ct := f.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	return map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": relPath(dir, f.LocalPath), "content_type": ct, "file_id": f.ID}
}

// fileName is a file's name as it may be kept in its folder here: its last
// element, so a name from the server such as "../../x" never puts the file
// outside the app's files folder, or "file" when that is none; and one
// every system the app runs on can hold (icloud.PortableName), so a name
// a Mac takes, as "Q3: report.pdf", is kept on Windows too, and the file
// syncs there through iCloud Drive.
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

// DownloadedFile keeps a file downloaded from the server at path, which
// is this device's alone, whole on the disk before its row names it. A
// file deleted or removed, before or while it came, keeps no copy:
// nothing would remove it again.
func (s *Store) DownloadedFile(id string, data []byte) (string, error) {
	return s.DownloadedFileFrom(id, bytes.NewReader(data))
}

// DownloadedFileFrom is DownloadedFile for a file read from r as it comes,
// such as a download still under way, which is written to the disk as it
// is read, never all in memory. One that fails part way keeps nothing.
func (s *Store) DownloadedFileFrom(id string, r io.Reader) (string, error) {
	var name string
	if err := s.db.QueryRow(`SELECT name FROM files WHERE id = ? AND deleted_at IS NULL`, id).Scan(&name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir, "files", id)
	local := filepath.Join(dir, fileName(name))
	if err := writeFrom(local, r); err != nil {
		_ = os.Remove(dir) // only once it is empty
		return "", err
	}
	res, err := s.db.Exec(`UPDATE files SET local_path = ? WHERE id = ? AND deleted_at IS NULL`, relPath(s.Dir, local), id)
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

// DeleteFile deletes a file, which cannot be undone: its copy here goes
// now, and its stored copy (in Storage, or iCloud Drive) once the
// deletion has gone up. An upload still waiting is dropped. The file is
// deleted as stored, not as the caller last read it, and only its
// deletion goes up, so a change made since, here or elsewhere, is kept.
func (s *Store) DeleteFile(f File) error {
	if err := s.inTx(func(tx *sql.Tx) error { return deleteFile(tx, s.Dir, f.ID, time.Now()) }); err != nil {
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
func deleteFile(tx *sql.Tx, dir, id string, now time.Time) error {
	files, err := filesIn(tx, dir, `id = ?`, id)
	if err != nil || len(files) == 0 {
		return err
	}
	f := files[0]
	old := FileToRemote(f)
	f.DeletedAt, f.UpdatedAt = now, now
	if err := putFile(tx, dir, f); err != nil {
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
	if s.removeLocalCopy(s.Path(local.String)) {
		_, _ = s.db.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, id)
	}
}
