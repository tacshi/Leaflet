package store

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Change struct {
	Table  string
	PageID string
}

type Store struct {
	db  *sql.DB
	Dir string

	mu        sync.Mutex
	session   Session
	listeners []func(Change)

	todosMu sync.Mutex
	todos   map[string]pageTodos

	indexing atomic.Bool
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return OpenPath(Path(dir), dir)
}

func Path(dir string) string { return filepath.Join(dir, "leaflet.db") }

func OpenMemory(dir string) (*Store, error) {
	return OpenPath(":memory:", dir)
}

func OpenPath(path, dir string) (*Store, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		dir = abs
	}
	dsn := ":memory:"
	if path != ":memory:" {
		var err error
		if dsn, err = fileURI(path, "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)&_txlock=immediate"); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, Dir: dir}, nil
}

func fileURI(path, query string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: query}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

func each(rows *sql.Rows, err error, fn func(scanner) (more bool, err error)) error {
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if more, err := fn(rows); err != nil || !more {
			return err
		}
	}
	return rows.Err()
}

func collect[T any](rows *sql.Rows, err error, scan func(scanner) (T, error)) ([]T, error) {
	var out []T
	err = each(rows, err, func(sc scanner) (bool, error) {
		v, err := scan(sc)
		out = append(out, v)
		return true, err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanString(sc scanner) (string, error) {
	var v string
	err := sc.Scan(&v)
	return v, err
}

func (s *Store) inTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Subscribe(fn func(Change)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

func (s *Store) Notify(table, pageID string) {
	s.mu.Lock()
	ls := append([]func(Change){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range ls {
		fn(Change{Table: table, PageID: pageID})
	}
}

func (s *Store) WatchOthers(every time.Duration, stop <-chan struct{}, fn func()) {
	version := func() int64 {
		var v int64
		if err := s.db.QueryRow(`PRAGMA data_version`).Scan(&v); err != nil {
			return -1
		}
		return v
	}
	last := version()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if v := version(); v != last && v >= 0 {
				if last >= 0 {
					fn()
				}
				last = v
			}
		}
	}
}

func ns(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nt(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMs(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}

var errNotFound = errors.New("not found")

func IsNotFound(err error) bool {
	return errors.Is(err, errNotFound) || errors.Is(err, sql.ErrNoRows)
}
