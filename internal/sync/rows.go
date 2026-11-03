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
