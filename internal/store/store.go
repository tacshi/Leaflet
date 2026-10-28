// Package store is the app's database on this device: a mirror of the
// Supabase tables plus device-only state (outbox, conflicts, settings, and
// what iCloud sync has seen of the other devices).
// Every change is saved here first and synced later.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
  keep_offline INTEGER NOT NULL DEFAULT 1, is_template INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS pages_space ON pages (space_id, parent_id);
CREATE TABLE IF NOT EXISTS page_contents (
  page_id TEXT PRIMARY KEY, content TEXT NOT NULL, plain_text TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL, base_content TEXT, remote_updated_at INTEGER,
  remote_revision INTEGER NOT NULL DEFAULT 0);
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
  last_error TEXT, created_at INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS outbox_entity ON outbox (entity, entity_id);
CREATE TABLE IF NOT EXISTS conflicts (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, local_node TEXT NOT NULL,
  remote_node TEXT NOT NULL, kept TEXT NOT NULL, local_edited_at INTEGER NOT NULL, remote_edited_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL, resolved_at INTEGER);
CREATE TABLE IF NOT EXISTS page_members (
  page_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '', PRIMARY KEY (page_id, user_id));
CREATE TABLE IF NOT EXISTS key_values (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS peer_contents (
  peer_id TEXT NOT NULL, page_id TEXT NOT NULL, version INTEGER NOT NULL, content TEXT NOT NULL,
  PRIMARY KEY (peer_id, page_id));
CREATE TABLE IF NOT EXISTS row_versions (
  tbl TEXT NOT NULL, id TEXT NOT NULL, at INTEGER NOT NULL, device TEXT NOT NULL,
  PRIMARY KEY (tbl, id));
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
	// Transactions take the write lock as they begin (_txlock), so what one
	// reads stays as it read it until it commits, and one that cannot have
	// the lock waits for it, up to the busy timeout, before doing anything.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(0)&_txlock=immediate"
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
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, Dir: dir}, nil
}

// migrations bring a database an earlier version made up to this one's:
// migrations[i] takes it from schema version i to i+1, kept in PRAGMA
// user_version. A change to a table that exists adds one here as well as
// changing schema; a new table needs only schema.
var migrations = []func(context.Context, *sql.Conn) error{
	// 1: columns added before the first release, which kept no version:
	// databases from the releases have them, earlier ones lack them.
	func(ctx context.Context, c *sql.Conn) error {
		return addColumns(ctx, c, [][3]string{
			{"pages", "is_template", "INTEGER NOT NULL DEFAULT 0"},
			{"page_contents", "remote_revision", "INTEGER NOT NULL DEFAULT 0"},
			{"outbox", "revision", "INTEGER NOT NULL DEFAULT 0"},
		})
	},
}

// migrate creates the tables, or brings those an earlier version made up
// to date, in one transaction that another process opening the database
// waits for.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := upgrade(ctx, c); err != nil {
		_, _ = c.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	_, err = c.ExecContext(ctx, `COMMIT`)
	return err
}

func upgrade(ctx context.Context, c *sql.Conn) error {
	var version, tables int
	if err := c.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("the database is from a newer Leaflet (schema %d; this one knows %d)", version, len(migrations))
	}
	// A new database gets the schema as it is; only one an earlier
	// version made runs the migrations.
	if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'pages'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		for i := version; i < len(migrations); i++ {
			if err := migrations[i](ctx, c); err != nil {
				return fmt.Errorf("migrate to schema %d: %w", i+1, err)
			}
		}
	}
	if _, err := c.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	_, err := c.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)))
	return err
}

// addColumns adds the columns, as {table, column, definition}, that their
// tables lack.
func addColumns(ctx context.Context, c *sql.Conn, cols [][3]string) error {
	for _, col := range cols {
		var n int
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := c.ExecContext(ctx, "ALTER TABLE "+col[0]+" ADD COLUMN "+col[1]+" "+col[2]); err != nil {
			return err
		}
	}
	return nil
}

// querier is the database, or a transaction on it.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
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

// ValuesWithPrefix is the key-value settings whose keys start with
// prefix, by their keys less it.
func (s *Store) ValuesWithPrefix(prefix string) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM key_values WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// DeleteValue removes a key-value setting.
func (s *Store) DeleteValue(key string) error {
	_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, key)
	return err
}

// Wipe removes everything stored on this device.
func (s *Store) Wipe() error {
	for _, t := range []string{"spaces", "folders", "pages", "page_contents", "comments", "recordings",
		"files", "ai_jobs", "model_providers", "page_history", "outbox", "conflicts", "page_members", "key_values",
		"peer_contents", "row_versions"} {
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
