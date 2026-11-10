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
}

func (s *Store) Files() ([]File, error) {
	return s.queryFiles(`deleted_at IS NULL ORDER BY created_at DESC`)
}

func (s *Store) AllFiles() ([]File, error) { return s.queryFiles(`1 = 1 ORDER BY created_at DESC`) }

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

func filesIn(q querier, dir, where string, args ...any) ([]File, error) {
	rows, err := q.Query(`SELECT id, space_id, folder_id, page_id, name, storage_path, local_path, mime_type, size_bytes, created_at, updated_at, deleted_at FROM files WHERE `+where, args...) // #nosec G202
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
		files, err := filesIn(tx, s.Dir, `id = ?`, id)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return errNotFound
		}
		f := files[0]
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
	if err := s.db.QueryRow(`SELECT name FROM files WHERE id = ? AND deleted_at IS NULL`, id).Scan(&name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir, "files", id)
	local := filepath.Join(dir, fileName(name))
	if err := writeFrom(local, r); err != nil {
		_ = os.Remove(dir)
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

func (s *Store) DeleteFile(f File) error {
	if err := s.inTx(func(tx *sql.Tx) error { return deleteFile(tx, s.Dir, f.ID, time.Now()) }); err != nil {
		return err
	}
	s.removeCopy(f.ID)
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

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

func (s *Store) removeCopy(id string) {
	var local sql.NullString
	if err := s.db.QueryRow(`SELECT local_path FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(&local); err != nil || local.String == "" {
		return
	}
	if s.removeLocalCopy(s.Path(local.String)) {
		_, _ = s.db.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, id)
	}
}
