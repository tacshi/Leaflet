package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const starterKey = "welcome.starter"

type starterSet struct {
	Pages []string `json:"pages"`
	At    int64    `json:"at"`
}

func (s *Store) MarkStarterPages(ids []string) error {
	v, err := json.Marshal(starterSet{Pages: ids, At: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return s.SetValue(starterKey, string(v))
}

func (s *Store) StarterPagesUnsettled() bool {
	_, ok := s.Value(starterKey)
	return ok
}

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

func starterTaken(tx *sql.Tx, set starterSet) (bool, error) {
	in := strings.TrimSuffix(strings.Repeat("?, ", len(set.Pages)), ", ")
	ids := make([]any, len(set.Pages))
	for i, id := range set.Pages {
		ids[i] = id
	}
	after := func(args ...any) []any { return append(slices.Clone(args), set.At) }
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
		{`SELECT 1 FROM pages WHERE parent_id IN (` + in + `) AND id NOT IN (` + in + `)`, slices.Concat(ids, ids)},
		{`SELECT 1 FROM pages WHERE default_template_id IN (` + in + `)
			UNION ALL SELECT 1 FROM folders WHERE default_template_id IN (` + in + `)`, slices.Concat(ids, ids)},
	}
	for _, c := range checks {
		var one int
		err := tx.QueryRow(c.query+` LIMIT 1`, c.args...).Scan(&one) // #nosec G202
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}
