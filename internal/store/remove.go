package store

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
)

// tables are the store's tables, each with the column that names the page
// a row is of, "" where rows are of no page: removing everything, an
// account or some pages goes by them, so a table schema gains is left out
// of none.
var tables = []struct{ name, page string }{
	{"spaces", ""}, {"folders", ""}, {"pages", "id"}, {"page_contents", "page_id"}, {"comments", "page_id"},
	{"recordings", "page_id"}, {"files", "page_id"}, {"ai_jobs", "page_id"}, {"model_providers", ""},
	{"page_history", "page_id"}, {"outbox", "page_id"}, {"conflicts", "page_id"}, {"page_members", "page_id"},
	{"key_values", ""}, {"peer_contents", "page_id"}, {"row_versions", ""},
}

// accountRows are, of the tables that hold the device's own rows too,
// the rows that go with an account: the device's own model connections
// stay, and its settings.
var accountRows = map[string]string{
	"model_providers": `is_local = 0`,
	"key_values": `key IN ('account', 'profile.name', 'last.page', 'device.id', 'sync.content_waiting', 'sync.own_members_at', 'sync.requeue_waiting', 'sync.moves_waiting')
		OR key LIKE 'space.%' OR key LIKE 'sync.cursor.%' OR key LIKE 'chat.%' OR key LIKE 'icloud.%' OR key LIKE 'invite.%'`,
}

// Wipe removes everything stored on this device.
// It is one transaction: a table left behind by a failure part way, as
// the outbox with the pages gone, would go up as the next account's.
func (s *Store) Wipe() error {
	err := s.inTx(func(tx *sql.Tx) error {
		for _, t := range tables {
			if _, err := tx.Exec("DELETE FROM " + t.name); err != nil { // #nosec G202 -- one of the tables listed here.
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("*", "")
	return s.removeCopies()
}

// ForgetAccount removes the account's pages, and everything synced or
// waiting to sync with them, from this device, its files and recordings'
// audio included. Settings and the device's
// own model connections stay. The next session starts with an empty space,
// and the device registers anew, as the server's row is the account's.
func (s *Store) ForgetAccount() error { return s.forgetAccount(false) }

// ErrUnsynced is ForgetSyncedAccount finding changes still to go up.
var ErrUnsynced = errors.New("changes made on this device have not gone up yet")

// ForgetSyncedAccount is ForgetAccount once every change here has gone up:
// it removes nothing, and returns ErrUnsynced, while one waits. It checks
// in the transaction that removes, so a change stored meanwhile, by leaflet
// mcp too, is never removed with the rest.
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
		for _, t := range tables {
			q := "DELETE FROM " + t.name
			if where, ok := accountRows[t.name]; ok {
				q += " WHERE " + where
			}
			if _, err := tx.Exec(q); err != nil { // #nosec G202 -- the tables listed here, and the literals of accountRows.
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Notify("*", "")
	return s.removeCopies()
}

// RemoveSharedPages removes from this device a page shared with the user
// whose share was withdrawn, with the pages under it and everything kept
// of them: content, history, comments, recordings and files with their
// copies here, members, conflicts, chats, and changes waiting, which the
// server would refuse now. A page of the user's own space stays, as does
// everything when the page is not here. It returns the pages removed.
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

// RemovePages removes pages from this device with everything kept of
// them (removePages), as if they had never been made here: none of it
// goes to the server.
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

// removePages removes pages from this device with everything kept of
// them: content, history, comments, recordings and files, members,
// conflicts, chats, and changes waiting to go up. It returns the paths of
// the files' and recordings' copies here, to remove once the transaction
// is done.
func (s *Store) removePages(tx *sql.Tx, ids []string) ([]string, error) {
	in := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.Query(`SELECT local_path FROM files WHERE local_path IS NOT NULL AND page_id IN (`+in+`)
		UNION ALL SELECT local_path FROM recordings WHERE local_path IS NOT NULL AND page_id IN (`+in+`)`, slices.Concat(args, args)...) // #nosec G202 -- placeholders only.
	copies, err := collect(rows, err, func(sc scanner) (string, error) {
		path, err := scanString(sc)
		return s.Path(path), err
	})
	if err != nil {
		return nil, err
	}
	// The files' entries are dropped by the files: ones queued before
	// they went with their page's have no page_id.
	files, err := filesIn(tx, s.Dir, `page_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := tx.Exec(`DELETE FROM outbox WHERE (entity = 'files' AND entity_id = ?)
			OR (entity IN ('storage', 'storage_delete') AND entity_id = ?)`, f.ID, "files/"+f.ID); err != nil {
			return nil, err
		}
	}
	for _, t := range tables {
		if t.page == "" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM `+t.name+` WHERE `+t.page+` IN (`+in+`)`, args...); err != nil { // #nosec G202 -- the tables listed in tables, and placeholders.
			return nil, err
		}
	}
	// The page's own rows may be queued without its page_id.
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity IN ('pages', 'page_content', 'page_history') AND entity_id IN (`+in+`)`, args...); err != nil { // #nosec G202 -- placeholders only.
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM key_values WHERE key IN (?, ?)`, "chat."+id, "chat.past."+id); err != nil {
			return nil, err
		}
	}
	return copies, nil
}
