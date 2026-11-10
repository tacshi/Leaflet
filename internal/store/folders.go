package store

import (
	"database/sql"
	"errors"
	"time"

	"leaflet/internal/doc"
)

type Folder struct {
	ID                string
	SpaceID           string
	ParentFolderID    string
	Name              string
	SortKey           string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         time.Time
	DefaultTemplateID string
}

const folderCols = `id, space_id, parent_folder_id, name, sort_key, created_at, updated_at, deleted_at, default_template_id`

func scanFolder(sc scanner) (Folder, error) {
	var f Folder
	var parent, template sql.NullString
	var c, u int64
	var d sql.NullInt64
	err := sc.Scan(&f.ID, &f.SpaceID, &parent, &f.Name, &f.SortKey, &c, &u, &d, &template)
	f.ParentFolderID, f.CreatedAt, f.UpdatedAt, f.DeletedAt = parent.String, time.UnixMilli(c), time.UnixMilli(u), fromMs(d)
	f.DefaultTemplateID = template.String
	return f, err
}

func (s *Store) Folders() ([]Folder, error) {
	return s.queryFolders(`deleted_at IS NULL ORDER BY sort_key, id`)
}

func (s *Store) AllFolders() ([]Folder, error) { return s.queryFolders(`1 = 1 ORDER BY sort_key, id`) }

func (s *Store) queryFolders(where string) ([]Folder, error) {
	rows, err := s.db.Query(`SELECT ` + folderCols + ` FROM folders WHERE ` + where) // #nosec G202
	return collect(rows, err, scanFolder)
}

func (s *Store) Folder(id string) (Folder, error) { return folderIn(s.db, id) }

func folderIn(q querier, id string) (Folder, error) {
	return scanFolder(q.QueryRow(`SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
}

func (s *Store) PutFolder(f Folder) error { return writeFolder(s.db, f) }

func writeFolder(q querier, f Folder) error {
	_, err := q.Exec(`INSERT INTO folders (`+folderCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET space_id = excluded.space_id,
		parent_folder_id = excluded.parent_folder_id, name = excluded.name, sort_key = excluded.sort_key,
		updated_at = excluded.updated_at, deleted_at = excluded.deleted_at, default_template_id = excluded.default_template_id`,
		f.ID, f.SpaceID, ns(f.ParentFolderID), f.Name, f.SortKey, ms(f.CreatedAt), ms(f.UpdatedAt), nt(f.DeletedAt), ns(f.DefaultTemplateID))
	return err
}

func (s *Store) CreateFolder(name string) (Folder, error) {
	now := time.Now()
	f := Folder{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, Name: name, CreatedAt: now, UpdatedAt: now}
	err := s.inTx(func(tx *sql.Tx) error {
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		var last sql.NullString
		if err := tx.QueryRow(`SELECT MAX(sort_key) FROM folders WHERE deleted_at IS NULL`).Scan(&last); err != nil {
			return err
		}
		f.SortKey = NewKey(last.String, "")
		return saveFolder(tx, f, "new folder")
	})
	if err != nil {
		return Folder{}, err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return f, nil
}

func saveFolder(tx *sql.Tx, f Folder, label string) error {
	var old map[string]any
	switch was, err := folderIn(tx, f.ID); {
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

func (s *Store) changeFolder(id, label string, fn func(*Folder) bool) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := folderIn(tx, id)
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

func (s *Store) RestoreFolder(id string) error {
	return s.changeFolder(id, "restored folder", func(f *Folder) bool {
		if f.DeletedAt.IsZero() {
			return false
		}
		f.DeletedAt = time.Time{}
		return true
	})
}

func (s *Store) RenameFolder(f Folder, name string) error {
	return s.changeFolder(f.ID, "renamed folder", func(f *Folder) bool {
		f.Name = name
		return true
	})
}

func (s *Store) SetFolderDefaultTemplate(f Folder, templateID string) error {
	return s.changeFolder(f.ID, "set default template", func(f *Folder) bool {
		if f.DefaultTemplateID == templateID {
			return false
		}
		f.DefaultTemplateID = templateID
		return true
	})
}

func (s *Store) DeleteFolder(f Folder) error {
	now := time.Now()
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
		files, err := filesIn(tx, s.Dir, `folder_id = ? AND deleted_at IS NULL`, f.ID)
		if err != nil {
			return err
		}
		for _, file := range files {
			old := FileToRemote(file)
			file.FolderID, file.UpdatedAt = "", now
			if err := putFile(tx, s.Dir, file); err != nil {
				return err
			}
			if err := queueChange(tx, "files", file.ID, old, FileToRemote(file), "moved file", file.PageID); err != nil {
				return err
			}
		}
		f, err := folderIn(tx, f.ID)
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
