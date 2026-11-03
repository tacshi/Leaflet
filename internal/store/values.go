package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Value reads a key-value setting, false when there is none or it cannot
// be read. A value a decision stands on is read with Lookup instead, so
// one that could not be read never counts as none.
func (s *Store) Value(key string) (string, bool) {
	v, ok, err := s.Lookup(key)
	return v, ok && err == nil
}

// Lookup reads a key-value setting: its value, whether there is one, and
// the error that kept it from being read.
func (s *Store) Lookup(key string) (string, bool, error) { return lookup(s.db, key) }

func lookup(q querier, key string) (string, bool, error) {
	var v string
	switch err := q.QueryRow(`SELECT value FROM key_values WHERE key = ?`, key).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return v, true, nil
}

// SetValue writes a key-value setting.
func (s *Store) SetValue(key, value string) error {
	err := setValue(s.db, key, value)
	if err == nil {
		s.Notify("key_values", "")
	}
	return err
}

func setValue(q querier, key, value string) error {
	_, err := q.Exec(`INSERT INTO key_values (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ValuesWithPrefix is the key-value settings whose keys start with
// prefix, by their keys less it.
func (s *Store) ValuesWithPrefix(prefix string) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM key_values WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	_ = each(rows, err, func(sc scanner) (bool, error) {
		var k, v string
		if sc.Scan(&k, &v) == nil {
			out[strings.TrimPrefix(k, prefix)] = v
		}
		return true, nil
	})
	return out
}

// DeleteValue removes a key-value setting.
func (s *Store) DeleteValue(key string) error {
	_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, key)
	return err
}

// valueOnce returns the setting key, which create makes the first time, in
// one transaction: two processes starting at once, as the app and leaflet
// mcp on a new database, make one between them, not one each. A setting
// that cannot be read is an error, never made anew over the one there.
func (s *Store) valueOnce(key string, create func(tx *sql.Tx) (string, error)) (string, error) {
	if v, ok, err := s.Lookup(key); err != nil || ok {
		return v, err
	}
	var v string
	made := false
	err := s.inTx(func(tx *sql.Tx) error {
		var ok bool
		var err error
		if v, ok, err = lookup(tx, key); err != nil || ok {
			return err
		}
		if v, err = create(tx); err != nil {
			return err
		}
		made = true
		return setValue(tx, key, v)
	})
	if err != nil {
		return "", err
	}
	if made {
		s.Notify("key_values", "")
	}
	return v, nil
}
