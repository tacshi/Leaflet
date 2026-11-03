// Package store is the app's database on this device: a mirror of the
// Supabase tables plus device-only state (outbox, conflicts, settings, and
// what iCloud sync has seen of the other devices).
// Every change is saved here first and synced later.
package store

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Change says what changed: a table, and the page when one is concerned.
type Change struct {
	Table  string
	PageID string
}

// Store is the database and the session it writes for.
type Store struct {
	db  *sql.DB
	Dir string

	mu        sync.Mutex
	session   Session
	listeners []func(Change)

	// todos are each page's to-dos as its content last read gave them,
	// with what that content was (todoKey): Todos decodes only the pages
	// whose content changed since. Guarded by todosMu.
	todosMu sync.Mutex
	todos   map[string]pageTodos
}

// Open opens (creating) the database in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "leaflet.db")
	return OpenPath(path, dir)
}

// OpenMemory opens an empty database in memory, for tests.
func OpenMemory(dir string) (*Store, error) {
	return OpenPath(":memory:", dir)
}

// OpenPath opens the database at a path, for the data folder dir. A dir
// given relative to the working folder is kept whole: the paths of the
// files in it are joined to it (absPath), and would otherwise be joined
// twice, or to another working folder.
func OpenPath(path, dir string) (*Store, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		dir = abs
	}
	// Transactions take the write lock as they begin (_txlock), so what one
	// reads stays as it read it until it commits, and one that cannot have
	// the lock waits for it, up to the busy timeout, before doing anything.
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
	// One connection: writes from the UI and sync take turns, and an
	// in-memory database stays one database.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, Dir: dir}, nil
}

// fileURI is the SQLite URI of the database at path, with query: the
// path escaped, so a "#", "?" or "%" in a folder's name opens that
// folder's database and not another.
func fileURI(path, query string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: query}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path // C:/… on Windows
	}
	return u.String(), nil
}

// querier is the database, or a transaction on it.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// scanner is a row to read: one QueryRow returned, or the rows Query did.
type scanner interface{ Scan(dest ...any) error }

// each calls fn with each of the rows a query returned, until it reports
// it needs no more, and closes them.
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

// collect reads, with scan, each of the rows a query returned, and closes
// them.
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

// scanString reads a row of one text column.
func scanString(sc scanner) (string, error) {
	var v string
	err := sc.Scan(&v)
	return v, err
}

// inTx runs fn in one transaction, which holds the write lock from its
// start: a change and its entry in the outbox are stored together or not
// at all, and no pull stores between them. fn must use only tx, as the
// store's one connection is the transaction's until it ends.
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

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the database, for tests to set up rows as no method of the
// store would.
func (s *Store) DB() *sql.DB { return s.db }

// Subscribe calls fn after each change, on the goroutine that made it.
func (s *Store) Subscribe(fn func(Change)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// Notify tells the listeners about a change.
func (s *Store) Notify(table, pageID string) {
	s.mu.Lock()
	ls := append([]func(Change){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range ls {
		fn(Change{Table: table, PageID: pageID})
	}
}

// WatchOthers calls fn, every so often, after another process (such as
// `leaflet mcp`) has written to the database. It returns when stop
// closes.
func (s *Store) WatchOthers(every time.Duration, stop <-chan struct{}, fn func()) {
	// data_version moves on when another connection commits; the
	// store's one connection keeps it, so the app's own writes leave it.
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

// Helpers for nullable columns: "" and zero times are NULL.

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

// IsNotFound reports whether err means the row does not exist.
func IsNotFound(err error) bool {
	return errors.Is(err, errNotFound) || errors.Is(err, sql.ErrNoRows)
}
