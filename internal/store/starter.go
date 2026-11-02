package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// A new device starts with pages of its own that show what Leaflet does
// (internal/core's welcome pages). They are the library's only when the
// device starts one: joining a library that other devices made, which has
// its own, they would make a second set there. The device keeps which
// they are until it joins one, and then settles them.

// starterKey keeps the starter pages of a device that has joined no
// library yet, as a starterSet.
const starterKey = "welcome.starter"

// starterSet is the starter pages, and when they were made, in
// milliseconds: the user changed one if it changed after.
type starterSet struct {
	Pages []string `json:"pages"`
	At    int64    `json:"at"`
}

// MarkStarterPages notes the starter pages this device just made.
func (s *Store) MarkStarterPages(ids []string) error {
	v, err := json.Marshal(starterSet{Pages: ids, At: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return s.SetValue(starterKey, string(v))
}

// SettleStarterPages decides what becomes of the starter pages as this
// device joins a library, once. Joining one other devices made (joined),
// it removes them, as that library has its own, unless the user has
// taken them up: changed one, added to one, or started from its
// template. Deleted ones go anyway. Starting a library, they stay, as
// its own.
func (s *Store) SettleStarterPages(joined bool) error {
	v, ok := s.Value(starterKey)
	if !ok {
		return nil
	}
	var set starterSet
	if json.Unmarshal([]byte(v), &set) != nil || !joined || len(set.Pages) == 0 {
		return s.DeleteValue(starterKey)
	}
	var copies []string
	removed := false
	err := s.inTx(func(tx *sql.Tx) error {
		taken, err := starterTaken(tx, set)
		if err != nil || taken {
			return err
		}
		if copies, err = s.removePages(tx, set.Pages); err != nil {
			return err
		}
		removed = true
		_, err = tx.Exec(`DELETE FROM key_values WHERE key IN (?, 'welcome.page')`, starterKey)
		return err
	})
	if err != nil {
		return err
	}
	if !removed {
		return s.DeleteValue(starterKey)
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return nil
}

// starterTaken reports whether the user has taken up the starter pages
// since they were made: changed one not deleted, its content, comments or
// files; recorded on one; made a page inside one; or set one as a page's
// or folder's template.
func starterTaken(tx *sql.Tx, set starterSet) (bool, error) {
	in := strings.TrimSuffix(strings.Repeat("?, ", len(set.Pages)), ", ")
	ids := make([]any, len(set.Pages))
	for i, id := range set.Pages {
		ids[i] = id
	}
	after := func(args ...any) []any { return append(append([]any{}, args...), set.At) }
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT 1 FROM pages WHERE id IN (` + in + `) AND deleted_at IS NULL AND updated_at > ?`, after(ids...)},
		{`SELECT 1 FROM page_contents c JOIN pages p ON p.id = c.page_id
			WHERE c.page_id IN (` + in + `) AND p.deleted_at IS NULL AND c.updated_at > ?`, after(ids...)},
		{`SELECT 1 FROM comments WHERE page_id IN (` + in + `) AND updated_at > ?`, after(ids...)},
		{`SELECT 1 FROM files WHERE page_id IN (` + in + `) AND updated_at > ?`, after(ids...)},
		{`SELECT 1 FROM recordings WHERE page_id IN (` + in + `)`, ids},
		{`SELECT 1 FROM pages WHERE parent_id IN (` + in + `) AND id NOT IN (` + in + `)`, append(ids, ids...)},
		{`SELECT 1 FROM pages WHERE default_template_id IN (` + in + `)
			UNION ALL SELECT 1 FROM folders WHERE default_template_id IN (` + in + `)`, append(ids, ids...)},
	}
	for _, c := range checks {
		var one int
		err := tx.QueryRow(c.query+` LIMIT 1`, c.args...).Scan(&one) // #nosec G202 -- placeholders only.
		if err == nil {
			return true, nil
		}
		if err != sql.ErrNoRows {
			return false, err
		}
	}
	return false, nil
}
