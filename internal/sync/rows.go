package sync

import (
	"encoding/json"
	"time"

	"leaflet/internal/store"
)

// A syncedTable is a table whose rows go to the other devices, through the
// server or iCloud Drive: how a row of it from elsewhere is stored here,
// and how this device's row reads as one that goes.
type syncedTable struct {
	// put stores a row, unless it has a change waiting here. A page has
	// none: the server and iCloud Drive each settle a loop of parents
	// their own way (putServerPage, putCloudPage).
	put func(st *store.Store, r map[string]any) error
	// row reads this device's row, with when it last changed.
	row func(st *store.Store, id string) (map[string]any, time.Time, error)
}

// syncedTables are the tables whose rows go to the other devices. Page
// history, agent runs and model connections stay on each device, or go to
// the server alone.
var syncedTables = map[string]syncedTable{
	"pages": {
		row: func(st *store.Store, id string) (map[string]any, time.Time, error) {
			p, err := st.Page(id)
			return store.PageToRemote(p), p.UpdatedAt, err
		},
	},
	"folders": {
		put: (*store.Store).PutRemoteFolder,
		row: func(st *store.Store, id string) (map[string]any, time.Time, error) {
			f, err := st.Folder(id)
			return store.FolderToRemote(f), f.UpdatedAt, err
		},
	},
	"files": {
		put: (*store.Store).PutRemoteFile,
		row: func(st *store.Store, id string) (map[string]any, time.Time, error) {
			f, err := st.File(id)
			return store.FileToRemote(f), f.UpdatedAt, err
		},
	},
	// A comment goes with its author's name (author_name): the other
	// devices' rows carry it, and the server's have it looked up.
	"comments": {
		put: func(st *store.Store, r map[string]any) error {
			return st.PutRemoteComment(r, store.S(r, "author_name"))
		},
		row: func(st *store.Store, id string) (map[string]any, time.Time, error) {
			c, err := st.Comment(id)
			row := store.CommentToRemote(c)
			row["author_name"] = c.AuthorName
			return row, c.UpdatedAt, err
		},
	},
	"recordings": {
		put: (*store.Store).PutRemoteRecording,
		row: func(st *store.Store, id string) (map[string]any, time.Time, error) {
			r, err := st.Recording(id)
			return store.RecordingToRemote(r), r.UpdatedAt, err
		},
	},
}

// synced reports whether a table's rows go to the other devices.
func synced(table string) bool {
	_, ok := syncedTables[table]
	return ok
}

// putRow stores a row of a synced table that came from elsewhere, unless
// it has a change waiting here; a page goes in by putPage. A table not
// synced stores nothing.
func (s *Service) putRow(table string, r map[string]any, putPage func(map[string]any) error) error {
	if table == "pages" {
		return putPage(r)
	}
	if t, ok := syncedTables[table]; ok {
		return t.put(s.st, r)
	}
	return nil
}

// localRow is this device's row of a synced table as the other devices
// have it, decoded from JSON; ok is false when it has none.
func (s *Service) localRow(table, id string) (row map[string]any, ok bool, err error) {
	t, synced := syncedTables[table]
	if !synced {
		return nil, false, nil
	}
	row, at, err := t.row(s.st, id)
	if store.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	row["updated_at"] = at.UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, false, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// rowSpace is the space a row of a synced table is of, by the pages this
// device has: a page's or a folder's own; a file's own, or the space of
// the page it is on when that is another, as an upload to a page someone
// shared is; and a comment's or a recording's page's. known is false for
// a comment or a recording on a page this device does not have.
func (s *Service) rowSpace(table string, row map[string]any) (space string, known bool, err error) {
	pageSpace := func() (string, bool, error) {
		id := store.S(row, "page_id")
		if id == "" {
			return "", false, nil
		}
		p, err := s.st.Page(id)
		if store.IsNotFound(err) {
			return "", false, nil
		}
		return p.SpaceID, err == nil, err
	}
	switch table {
	case "pages", "folders":
		return store.S(row, "space_id"), true, nil
	case "files":
		on, known, err := pageSpace()
		if err != nil || known && on != store.S(row, "space_id") {
			return on, known, err
		}
		return store.S(row, "space_id"), true, nil
	case "comments", "recordings":
		return pageSpace()
	}
	return "", false, nil
}

// otherSpace reports whether a row of a synced table is of a space other
// than the one this device syncs, as a page another account shared with
// the user, kept here after their session ended, is: as row has it, and
// as this device has it, when it does. A comment or a recording on a page
// this device does not have counts as of another space when strict, as
// what goes from here does, and as of this one otherwise, as what comes
// before its page may.
func (s *Service) otherSpace(table, id string, row map[string]any, strict bool) (bool, error) {
	space := s.st.Session().SpaceID
	local, here, err := s.localRow(table, id)
	if err != nil {
		return true, err
	}
	rows := []map[string]any{row}
	if here {
		rows = append(rows, local)
	}
	for _, r := range rows {
		if r == nil {
			continue
		}
		of, known, err := s.rowSpace(table, r)
		if err != nil {
			return true, err
		}
		if known && of != space || !known && strict {
			return true, nil
		}
	}
	return false, nil
}
