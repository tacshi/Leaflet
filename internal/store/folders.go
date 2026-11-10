package store

import (
	"cmp"
	"database/sql"
	"errors"
	"maps"
	"slices"
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

func (s *Store) CreateFolder(name string) (Folder, error) { return s.CreateFolderIn(name, "") }

func (s *Store) CreateFolderIn(name, parentID string) (Folder, error) {
	now := time.Now()
	f := Folder{ID: doc.NewRowID(), SpaceID: s.Session().SpaceID, ParentFolderID: parentID, Name: ClampTitle(name), CreatedAt: now, UpdatedAt: now}
	err := s.inTx(func(tx *sql.Tx) error {
		if f.SpaceID != "" && !hasSpace(tx, f.SpaceID) {
			return ErrNoSpace
		}
		if err := folderParentOK(tx, f.SpaceID, parentID); err != nil {
			return err
		}
		var err error
		f.SortKey, err = folderKeyAfter(tx, f.SpaceID, parentID, "", f.ID)
		if err != nil {
			return err
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

func (s *Store) changeFolder(id, label string, fn func(tx *sql.Tx, f *Folder) (bool, error)) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := folderIn(tx, id)
		if err != nil {
			return err
		}
		if changed, err = fn(tx, &f); err != nil || !changed {
			return err
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
	return s.changeFolder(id, "restored folder", func(tx *sql.Tx, f *Folder) (bool, error) {
		if f.DeletedAt.IsZero() {
			return false, nil
		}
		// A folder that came to hold its own parent while it was deleted comes back at the top level.
		if under, err := folderUnder(tx, f.ParentFolderID, f.ID); err != nil {
			return false, err
		} else if under {
			f.ParentFolderID = ""
		}
		f.DeletedAt = time.Time{}
		return true, nil
	})
}

func (s *Store) RenameFolder(f Folder, name string) error {
	name = ClampTitle(name)
	return s.changeFolder(f.ID, "renamed folder", func(_ *sql.Tx, f *Folder) (bool, error) {
		f.Name = name
		return true, nil
	})
}

func (s *Store) SetFolderDefaultTemplate(f Folder, templateID string) error {
	return s.changeFolder(f.ID, "set default template", func(_ *sql.Tx, f *Folder) (bool, error) {
		if f.DefaultTemplateID == templateID {
			return false, nil
		}
		f.DefaultTemplateID = templateID
		return true, nil
	})
}

func (s *Store) DeleteFolder(f Folder) error {
	now := time.Now()
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := folderIn(tx, f.ID)
		if err != nil {
			return err
		}
		up := f.ParentFolderID
		if up != "" {
			if parent, err := folderIn(tx, up); err != nil || !parent.DeletedAt.IsZero() || parent.SpaceID != f.SpaceID {
				up = ""
			}
		}
		// In a loop of folders the parent is also inside f, and what f held must not go back under f.
		if under, err := folderUnder(tx, up, f.ID); err != nil {
			return err
		} else if under {
			up = ""
		}
		pages, err := pagesIn(tx, `folder_id = ?`, f.ID)
		if err != nil {
			return err
		}
		for _, p := range pages {
			p.FolderID, p.UpdatedAt = up, now
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
			file.FolderID, file.UpdatedAt = up, now
			if err := putFile(tx, s.Dir, file); err != nil {
				return err
			}
			if err := queueChange(tx, "files", file.ID, old, FileToRemote(file), "moved file", file.PageID); err != nil {
				return err
			}
		}
		children, err := foldersIn(tx, `deleted_at IS NULL AND parent_folder_id = ? ORDER BY sort_key, id`, f.ID)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			siblings, err := folderSiblings(tx, f.SpaceID, up, f.ID)
			if err != nil {
				return err
			}
			before, after := f.SortKey, ""
			for _, x := range siblings {
				if x.SortKey > f.SortKey {
					after = x.SortKey
					break
				}
			}
			for _, child := range children {
				before = NewKey(before, after)
				child.ParentFolderID, child.SortKey, child.UpdatedAt = up, before, now
				if err := saveFolder(tx, child, "moved folder"); err != nil {
					return err
				}
			}
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

var ErrFolderIntoSelf = errors.New("a folder cannot move inside itself")

var ErrFolderAcrossSpaces = errors.New("a folder cannot move into another space")

var ErrFolderIntoDeleted = errors.New("a folder cannot move inside a deleted folder")

func (s *Store) MoveFolder(id, parentID, afterID string) error {
	return s.moveFolder(id, parentID, func(q querier, f Folder) (string, error) {
		before, after, err := folderGapAfter(q, f.SpaceID, parentID, afterID, id)
		return keyIn(f, parentID, before, after), err
	})
}

func (s *Store) MoveFolderBefore(id, parentID, beforeID string) error {
	return s.moveFolder(id, parentID, func(q querier, f Folder) (string, error) {
		before, after, err := folderGapBefore(q, f.SpaceID, parentID, beforeID, id)
		return keyIn(f, parentID, before, after), err
	})
}

// keyIn is the sort key that puts f in parentID between the keys before and after: the one it has, when it is there already.
func keyIn(f Folder, parentID, before, after string) string {
	if f.ParentFolderID == parentID && f.SortKey > before && (after == "" || f.SortKey < after) {
		return f.SortKey
	}
	return NewKey(before, after)
}

func (s *Store) moveFolder(id, parentID string, key func(q querier, f Folder) (string, error)) error {
	changed := false
	err := s.inTx(func(tx *sql.Tx) error {
		f, err := folderIn(tx, id)
		if err != nil {
			return err
		}
		if parentID != "" {
			if under, err := folderUnder(tx, parentID, id); err != nil || under {
				return cmp.Or(err, ErrFolderIntoSelf)
			}
		}
		if err := folderParentOK(tx, f.SpaceID, parentID); err != nil {
			return err
		}
		k, err := key(tx, f)
		if err != nil {
			return err
		}
		if f.ParentFolderID == parentID && f.SortKey == k {
			return nil
		}
		changed = true
		f.ParentFolderID, f.SortKey, f.UpdatedAt = parentID, k, time.Now()
		return saveFolder(tx, f, "moved folder")
	})
	if err != nil || !changed {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return nil
}

func (s *Store) PlaceFolder(id, parentID, sortKey string) error {
	return s.moveFolder(id, parentID, func(q querier, f Folder) (string, error) {
		if sortKey != "" {
			return sortKey, nil
		}
		return folderKeyAfter(q, f.SpaceID, parentID, "", id)
	})
}

func folderParentOK(q querier, spaceID, parentID string) error {
	if parentID == "" {
		return nil
	}
	switch parent, err := folderIn(q, parentID); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrFolderIntoDeleted
	case err != nil:
		return err
	case parent.SpaceID != spaceID:
		return ErrFolderAcrossSpaces
	case !parent.DeletedAt.IsZero():
		return ErrFolderIntoDeleted
	}
	return nil
}

func (s *Store) FolderUnder(candidate, of string) bool {
	under, err := folderUnder(s.db, candidate, of)
	return under || err != nil
}

// folderUnder reports whether candidate is the folder of or inside it, as the folders are shown: a folder in a deleted
// one is at the top level, so the walk up ends there.
func folderUnder(q querier, candidate, of string) (bool, error) {
	seen := map[string]bool{}
	for cursor := candidate; cursor != "" && !seen[cursor]; {
		if cursor == of {
			return true, nil
		}
		seen[cursor] = true
		f, err := folderIn(q, cursor)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !f.DeletedAt.IsZero() {
			return false, nil
		} else if err != nil {
			return false, err
		}
		cursor = f.ParentFolderID
	}
	return false, nil
}

// SettleFolderLoops lifts one folder of every loop of folders to the top level: the smallest id of the loop, so every
// device lifts the same one, and the lift syncs like any other move. Moves on two devices can make such a loop. A loop
// with a folder whose change has yet to sync is left as it is, until that change has gone up or given way. Call it
// once all the rows of a pull are in: between two rows of one change the folders can pass through a loop.
func (s *Store) SettleFolderLoops() error {
	settled := false
	err := s.inTx(func(tx *sql.Tx) error {
		folders, err := foldersIn(tx, `deleted_at IS NULL`)
		if err != nil {
			return err
		}
		up := make(map[string]string, len(folders))
		byID := make(map[string]Folder, len(folders))
		for _, f := range folders {
			up[f.ID], byID[f.ID] = f.ParentFolderID, f
		}
		now := time.Now()
		for _, loop := range loopsOf(up) {
			if waits, err := anyPending(tx, "folders", loop); err != nil || waits {
				if err != nil {
					return err
				}
				continue
			}
			f := byID[slices.Min(loop)]
			f.ParentFolderID, f.UpdatedAt = "", now
			if err := saveFolder(tx, f, "moved folder"); err != nil {
				return err
			}
			settled = true
		}
		return nil
	})
	if err != nil || !settled {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("folders", "")
	return nil
}

func anyPending(q querier, entity string, ids []string) (bool, error) {
	for _, id := range ids {
		if waits, err := pending(q, entity, id); err != nil || waits {
			return waits, err
		}
	}
	return false, nil
}

// loopsOf is the loops in up, which maps each row to its parent: a parent that is not a key ends the walk, as a row
// whose parent is deleted or not here is shown at the top level.
func loopsOf(up map[string]string) [][]string {
	var loops [][]string
	done := make(map[string]bool, len(up))
	for _, start := range slices.Sorted(maps.Keys(up)) {
		var path []string
		at := map[string]int{}
		for cursor := start; !done[cursor]; cursor = up[cursor] {
			if _, here := up[cursor]; !here {
				break
			}
			if i, again := at[cursor]; again {
				loops = append(loops, path[i:])
				break
			}
			at[cursor] = len(path)
			path = append(path, cursor)
		}
		for _, id := range path {
			done[id] = true
		}
	}
	return loops
}

func foldersIn(q querier, where string, args ...any) ([]Folder, error) {
	rows, err := q.Query(`SELECT `+folderCols+` FROM folders WHERE `+where, args...) // #nosec G202
	return collect(rows, err, scanFolder)
}

func folderSiblings(q querier, spaceID, parentID, skip string) ([]Folder, error) {
	all, err := foldersIn(q, `deleted_at IS NULL AND space_id = ? ORDER BY sort_key, id`, spaceID)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(all))
	for _, f := range all {
		live[f.ID] = true
	}
	shown := func(parent string) string {
		if live[parent] {
			return parent
		}
		return ""
	}
	parentID = shown(parentID)
	return slices.DeleteFunc(all, func(f Folder) bool { return f.ID == skip || shown(f.ParentFolderID) != parentID }), nil
}

func folderKeyAfter(q querier, spaceID, parentID, afterID, skip string) (string, error) {
	before, after, err := folderGapAfter(q, spaceID, parentID, afterID, skip)
	return NewKey(before, after), err
}

// folderGapAfter is the sort keys a folder goes between to come after the folder afterID in parentID, or last.
func folderGapAfter(q querier, spaceID, parentID, afterID, skip string) (before, after string, err error) {
	siblings, err := folderSiblings(q, spaceID, parentID, skip)
	if err != nil {
		return "", "", err
	}
	i := slices.IndexFunc(siblings, func(f Folder) bool { return f.ID == afterID })
	if afterID == "" || i < 0 {
		i = len(siblings) - 1
	}
	if i >= 0 {
		before = siblings[i].SortKey
		if i+1 < len(siblings) {
			after = siblings[i+1].SortKey
		}
	}
	if before != "" && after != "" && before >= after {
		after = ""
	}
	return before, after, nil
}

// folderGapBefore is the sort keys a folder goes between to come before the folder beforeID in parentID, or first.
func folderGapBefore(q querier, spaceID, parentID, beforeID, skip string) (before, after string, err error) {
	siblings, err := folderSiblings(q, spaceID, parentID, skip)
	if err != nil {
		return "", "", err
	}
	for i, f := range siblings {
		if f.ID == beforeID {
			after = f.SortKey
			if i > 0 {
				before = siblings[i-1].SortKey
			}
		}
	}
	if after == "" && len(siblings) > 0 {
		after = siblings[0].SortKey
	}
	if before != "" && after != "" && before >= after {
		before = ""
	}
	return before, after, nil
}
