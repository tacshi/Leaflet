package store

import (
	"database/sql"
	"errors"
	"slices"
)

var tables = []struct{ name, page string }{
	{"spaces", ""}, {"folders", ""}, {"pages", "id"}, {"page_contents", "page_id"}, {"comments", "page_id"},
	{"recordings", "page_id"}, {"files", "page_id"}, {"ai_jobs", "page_id"}, {"model_providers", ""},
	{"page_history", "page_id"}, {"outbox", "page_id"}, {"conflicts", "page_id"}, {"page_members", "page_id"},
	{"key_values", ""}, {"peer_contents", "page_id"}, {"row_versions", ""},
}

var accountRows = map[string]string{
	"model_providers": `FALSE`,
	"key_values": `key IN ('account', 'profile.name', 'last.page', 'device.id', 'sync.content_waiting', 'sync.own_members_at', 'sync.requeue_waiting', 'sync.moves_waiting', 'sync.trees_waiting')
		OR key LIKE 'space.%' OR key LIKE 'sync.cursor.%' OR key LIKE 'chat.%' OR key LIKE 'icloud.%' OR key LIKE 'invite.%' OR key LIKE 'mcp_edited:%' OR key LIKE 'mcp_activity:%'
		OR key LIKE 'reminded:%'`,
}

// ForgetAll takes everything off this device, as the account is deleted: its pages and what is on them, settings and
// providers too. When this device first had the bin stays: it is of the device.
func (s *Store) ForgetAll() error {
	err := s.inTx(func(tx *sql.Tx) error {
		if err := forgetSearch(tx); err != nil {
			return err
		}
		for _, t := range tables {
			q := "DELETE FROM " + t.name
			if t.name == "key_values" {
				q += " WHERE key <> '" + binSinceKey + "'"
			}
			if _, err := tx.Exec(q); err != nil { // #nosec G202
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("*", "")
	s.removeCopies()
	return nil
}

func (s *Store) ForgetAccount() error { return s.forgetAccount(false) }

var ErrUnsynced = errors.New("changes made on this device have not gone up yet")

func (s *Store) ForgetSyncedAccount() error { return s.forgetAccount(true) }

func (s *Store) forgetAccount(synced bool) error {
	err := s.inTx(func(tx *sql.Tx) error {
		if synced {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrUnsynced
			}
		}
		if err := forgetSearch(tx); err != nil {
			return err
		}
		for _, t := range tables {
			q := "DELETE FROM " + t.name
			if where, ok := accountRows[t.name]; ok {
				q += " WHERE " + where
			}
			if _, err := tx.Exec(q); err != nil { // #nosec G202
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("*", "")
	s.removeCopies()
	return nil
}

func forgetSearch(tx *sql.Tx) error {
	if _, err := tx.Exec(`INSERT INTO search_index (search_index) VALUES ('delete-all')`); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM text_writes`)
	return err
}

func (s *Store) RemoveSharedPages(rootID string) ([]string, error) {
	own := s.Session().SpaceID
	var ids, copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`WITH RECURSIVE tree(id) AS (
				SELECT id FROM pages WHERE id = ? AND space_id != ?
				UNION SELECT p.id FROM pages p JOIN tree t ON p.parent_id = t.id WHERE p.space_id != ?)
			SELECT id FROM tree`, rootID, own, own)
		if ids, err = collect(rows, err, scanString); err != nil || len(ids) == 0 {
			return err
		}
		copies, err = s.removePages(tx, ids)
		return err
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return ids, nil
}

func (s *Store) RemovePages(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		copies, err = s.removePages(tx, ids)
		return err
	})
	if err != nil {
		return err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return nil
}

func (s *Store) removePages(tx *sql.Tx, ids []string) ([]string, error) {
	return s.wipePages(tx, ids, false)
}

// wipePages removes everything on the pages from this device, and their rows unless keepRows, and is the local copies
// of their files and recordings to remove once it is done.
func (s *Store) wipePages(tx *sql.Tx, ids []string, keepRows bool) ([]string, error) {
	in := marks(len(ids))
	args := anys(ids)
	rows, err := tx.Query(`SELECT local_path FROM files WHERE local_path IS NOT NULL AND page_id IN (`+in+`)
		UNION ALL SELECT local_path FROM recordings WHERE local_path IS NOT NULL AND page_id IN (`+in+`)`, slices.Concat(args, args)...) // #nosec G202
	copies, err := collect(rows, err, func(sc scanner) (string, error) {
		path, err := scanString(sc)
		return s.Path(path), err
	})
	if err != nil {
		return nil, err
	}
	files, err := filesIn(tx, s.Dir, `page_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	// What waits to go up of the pages' files goes, an upload too, and, as the pages go from this device rows and all,
	// a removal of where one is stored. Pages deleted for good keep their files' removals: those of files deleted for
	// good before them, whose rows no longer say where they are stored, and those the caller queues again.
	key := "files/"
	for _, f := range files {
		if _, err := tx.Exec(`DELETE FROM outbox WHERE (entity = 'files' AND entity_id = ?)
			OR (entity = 'storage' AND entity_id = ?) OR (? AND entity = 'storage_delete' AND entity_id = ?)`,
			f.ID, key+f.ID, !keepRows, key+f.ID); err != nil {
			return nil, err
		}
	}
	for _, t := range tables {
		if t.page == "" || keepRows && t.name == "pages" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM `+t.name+` WHERE `+t.page+` IN (`+in+`)`, args...); err != nil { // #nosec G202
			return nil, err
		}
	}
	// What this device keeps of a page by its id: its agent chats, what an agent last wrote on it, and the reminders of
	// its to-dos already given (reminded:{page}#{block}).
	for _, id := range ids {
		reminded := "reminded:" + id + "#"
		if _, err := tx.Exec(`DELETE FROM key_values WHERE key IN (?, ?, ?, ?) OR substr(key, 1, ?) = ?`,
			"chat."+id, "chat.past."+id, "chat.running."+id, "mcp_edited:"+id, len(reminded), reminded); err != nil {
			return nil, err
		}
	}
	return copies, nil
}
