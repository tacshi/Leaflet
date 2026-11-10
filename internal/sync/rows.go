package sync

import (
	"encoding/json"
	"time"

	"leaflet/internal/store"
)

type syncedTable struct {
	put func(st *store.Store, r map[string]any) error
	row func(st *store.Store, id string) (map[string]any, time.Time, error)
}

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

func synced(table string) bool {
	_, ok := syncedTables[table]
	return ok
}

func (s *Service) putRow(table string, r map[string]any, putPage func(map[string]any) error) error {
	if table == "pages" {
		return putPage(r)
	}
	if t, ok := syncedTables[table]; ok {
		return t.put(s.st, r)
	}
	return nil
}

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
