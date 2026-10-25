// Package store is the app's database on this device: a mirror of the
// Supabase tables plus device-only state (outbox, conflicts, settings).
// Every change is saved here first and synced later.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS spaces (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, owner_id TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS folders (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_folder_id TEXT, name TEXT NOT NULL,
  sort_key TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER);
CREATE TABLE IF NOT EXISTS pages (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_id TEXT, folder_id TEXT,
  title TEXT NOT NULL DEFAULT '', icon TEXT, sort_key TEXT NOT NULL, created_by TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER,
  keep_offline INTEGER NOT NULL DEFAULT 1);
CREATE INDEX IF NOT EXISTS pages_space ON pages (space_id, parent_id);
CREATE TABLE IF NOT EXISTS page_contents (
  page_id TEXT PRIMARY KEY, content TEXT NOT NULL, plain_text TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL, base_content TEXT, remote_updated_at INTEGER);
CREATE TABLE IF NOT EXISTS comments (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, parent_comment_id TEXT,
  author_id TEXT, author_kind TEXT NOT NULL DEFAULT 'user', author_name TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL, quote TEXT, resolved INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER);
CREATE INDEX IF NOT EXISTS comments_page ON comments (page_id, block_id);
CREATE TABLE IF NOT EXISTS recordings (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, audio_path TEXT, local_path TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0, stt_provider TEXT NOT NULL DEFAULT '', stt_model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'recording', transcript TEXT NOT NULL DEFAULT '[]', language TEXT, error TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, uploaded_at INTEGER);
CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, folder_id TEXT, page_id TEXT, name TEXT NOT NULL,
  storage_path TEXT, local_path TEXT, mime_type TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER);
CREATE TABLE IF NOT EXISTS ai_jobs (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, surface TEXT NOT NULL, focus_block_ids TEXT NOT NULL DEFAULT '[]',
  comment_id TEXT, conversation_id TEXT, provider_id TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL,
  request TEXT NOT NULL, result TEXT, error TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS model_providers (
  id TEXT PRIMARY KEY, preset TEXT NOT NULL, name TEXT NOT NULL, base_url TEXT NOT NULL, auth_kind TEXT NOT NULL,
  default_model TEXT NOT NULL DEFAULT '', is_local INTEGER NOT NULL DEFAULT 0, supports_tools INTEGER,
  models TEXT NOT NULL DEFAULT '[]', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS page_history (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, content TEXT NOT NULL, reason TEXT NOT NULL, ai_job_id TEXT,
  label TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT, entity TEXT NOT NULL, entity_id TEXT NOT NULL, op TEXT NOT NULL,
  payload TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', page_id TEXT, attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT, created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS outbox_entity ON outbox (entity, entity_id);
CREATE TABLE IF NOT EXISTS conflicts (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, local_node TEXT NOT NULL,
  remote_node TEXT NOT NULL, kept TEXT NOT NULL, local_edited_at INTEGER NOT NULL, remote_edited_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL, resolved_at INTEGER);
CREATE TABLE IF NOT EXISTS page_members (
  page_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '', PRIMARY KEY (page_id, user_id));
CREATE TABLE IF NOT EXISTS key_values (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

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

// OpenPath opens the database at a path.
func OpenPath(path, dir string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)"
	if path == ":memory:" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: writes from the UI and sync take turns, and an
	// in-memory database stays one database.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, Dir: dir}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the database, for sync.
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

// Value reads a key-value setting.
func (s *Store) Value(key string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM key_values WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// SetValue writes a key-value setting.
func (s *Store) SetValue(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO key_values (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err == nil {
		s.Notify("key_values", "")
	}
	return err
}

// DeleteValue removes a key-value setting.
func (s *Store) DeleteValue(key string) error {
	_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, key)
	return err
}

// Wipe removes everything stored on this device.
func (s *Store) Wipe() error {
	for _, t := range []string{"spaces", "folders", "pages", "page_contents", "comments", "recordings",
		"files", "ai_jobs", "model_providers", "page_history", "outbox", "conflicts", "page_members", "key_values"} {
		if _, err := s.db.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	s.Notify("*", "")
	return nil
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
