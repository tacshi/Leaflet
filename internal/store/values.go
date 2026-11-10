package store

import (
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) Value(key string) (string, bool) {
	v, ok, err := s.Lookup(key)
	return v, ok && err == nil
}

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

func (s *Store) DeleteValue(key string) error {
	_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, key)
	return err
}

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
