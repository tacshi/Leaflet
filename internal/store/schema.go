package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"sync"
)

var schema = `
CREATE TABLE IF NOT EXISTS spaces (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, owner_id TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS folders (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_folder_id TEXT, name TEXT NOT NULL,
  sort_key TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER,
  default_template_id TEXT, pulled_at INTEGER);
CREATE TABLE IF NOT EXISTS pages (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_id TEXT, folder_id TEXT,
  title TEXT NOT NULL DEFAULT '', icon TEXT, sort_key TEXT NOT NULL, created_by TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER,
  keep_offline INTEGER NOT NULL DEFAULT 1, is_template INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT '[]', default_template_id TEXT, pulled_at INTEGER);
CREATE INDEX IF NOT EXISTS pages_space ON pages (space_id, parent_id);
CREATE INDEX IF NOT EXISTS pages_parent ON pages (parent_id, sort_key);
CREATE TABLE IF NOT EXISTS page_contents (
  page_id TEXT PRIMARY KEY, content TEXT NOT NULL, plain_text TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL, base_content TEXT, remote_updated_at INTEGER,
  remote_revision INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS page_contents_remote ON page_contents (page_id, remote_updated_at, remote_revision);
CREATE TABLE IF NOT EXISTS comments (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, parent_comment_id TEXT,
  author_id TEXT, author_kind TEXT NOT NULL DEFAULT 'user', author_name TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL, quote TEXT, resolved INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER, pulled_at INTEGER);
CREATE INDEX IF NOT EXISTS comments_page ON comments (page_id, block_id);
CREATE TABLE IF NOT EXISTS recordings (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL, block_id TEXT NOT NULL, audio_path TEXT, local_path TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0, stt_provider TEXT NOT NULL DEFAULT '', stt_model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'recording', transcript TEXT NOT NULL DEFAULT '[]', language TEXT, error TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, uploaded_at INTEGER, pulled_at INTEGER);
CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY, space_id TEXT NOT NULL, folder_id TEXT, page_id TEXT, name TEXT NOT NULL,
  storage_path TEXT, local_path TEXT, mime_type TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER, pulled_at INTEGER);
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
CREATE INDEX IF NOT EXISTS page_history_page ON page_history (page_id, created_at);
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
  email TEXT NOT NULL DEFAULT '', accepted INTEGER NOT NULL DEFAULT 1, PRIMARY KEY (page_id, user_id));
CREATE TABLE IF NOT EXISTS key_values (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS peer_contents (
  peer_id TEXT NOT NULL, page_id TEXT NOT NULL, version INTEGER NOT NULL, content TEXT NOT NULL,
  PRIMARY KEY (peer_id, page_id));
CREATE INDEX IF NOT EXISTS peer_contents_page ON peer_contents (page_id, peer_id);
CREATE TABLE IF NOT EXISTS row_versions (
  tbl TEXT NOT NULL, id TEXT NOT NULL, at INTEGER NOT NULL, device TEXT NOT NULL,
  PRIMARY KEY (tbl, id));
` + textWritesTable + `
CREATE VIRTUAL TABLE IF NOT EXISTS search_index USING fts5(text,
  tokenize = 'trigram case_sensitive 1', detail = 'none', content = '', contentless_delete = 1);
` + textTriggers("page", "page_contents", "page_id", "content") +
	textTriggers("recording", "recordings", "id", "transcript") +
	textTriggers("comment", "comments", "id", "body")

const textWritesTable = `
CREATE TABLE IF NOT EXISTS text_writes (
  n INTEGER PRIMARY KEY, kind TEXT NOT NULL, id TEXT NOT NULL, write_id INTEGER NOT NULL, indexed INTEGER,
  UNIQUE (kind, id));`

func textTriggers(kind, table, key, col string) string {
	write := `INSERT INTO text_writes (kind, id, write_id) VALUES ('` + kind + `', new.` + key + `, random())
    ON CONFLICT (kind, id) DO UPDATE SET write_id = excluded.write_id;`
	return `
CREATE TRIGGER IF NOT EXISTS ` + table + `_text_insert AFTER INSERT ON ` + table + ` BEGIN
  ` + write + `
END;
CREATE TRIGGER IF NOT EXISTS ` + table + `_text_update AFTER UPDATE OF ` + col + ` ON ` + table + `
  WHEN new.` + col + ` IS NOT old.` + col + ` BEGIN
  ` + write + `
END;
CREATE TRIGGER IF NOT EXISTS ` + table + `_text_delete AFTER DELETE ON ` + table + ` BEGIN
  DELETE FROM search_index WHERE rowid IN
    (SELECT n FROM text_writes WHERE kind = '` + kind + `' AND id = old.` + key + ` AND indexed IS NOT NULL);
  DELETE FROM text_writes WHERE kind = '` + kind + `' AND id = old.` + key + `;
END;`
}

var migrations = []func(context.Context, *sql.Conn) error{
	func(ctx context.Context, c *sql.Conn) error {
		return addColumns(ctx, c, [][3]string{
			{"pages", "is_template", "INTEGER NOT NULL DEFAULT 0"},
			{"page_contents", "remote_revision", "INTEGER NOT NULL DEFAULT 0"},
			{"outbox", "revision", "INTEGER NOT NULL DEFAULT 0"},
		})
	},
	func(ctx context.Context, c *sql.Conn) error {
		return addIndexes(ctx, c, [][3]string{
			{"pages_parent", "pages", "parent_id, sort_key"},
			{"page_history_page", "page_history", "page_id, created_at"},
			{"peer_contents_page", "peer_contents", "page_id, peer_id"},
		})
	},
	func(ctx context.Context, c *sql.Conn) error {
		return addColumns(ctx, c, [][3]string{
			{"page_members", "accepted", "INTEGER NOT NULL DEFAULT 1"},
		})
	},
	func(ctx context.Context, c *sql.Conn) error {
		return addColumns(ctx, c, [][3]string{
			{"pages", "tags", "TEXT NOT NULL DEFAULT '[]'"},
		})
	},
	func(ctx context.Context, c *sql.Conn) error {
		return addColumns(ctx, c, [][3]string{
			{"folders", "default_template_id", "TEXT"},
			{"pages", "default_template_id", "TEXT"},
		})
	},
	func(ctx context.Context, c *sql.Conn) error {
		return execIfTable(ctx, c, "conflicts", `DELETE FROM conflicts WHERE resolved_at IS NOT NULL`)
	},
	func(ctx context.Context, c *sql.Conn) error {
		return execIfTable(ctx, c, "comments", `UPDATE comments SET body = '', quote = NULL
			WHERE deleted_at IS NOT NULL AND (body <> '' OR quote IS NOT NULL)`)
	},
	func(ctx context.Context, c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, textWritesTable); err != nil {
			return err
		}
		if err := addIndexes(ctx, c, [][3]string{
			{"page_contents_remote", "page_contents", "page_id, remote_updated_at, remote_revision"},
		}); err != nil {
			return err
		}
		for _, t := range [][3]string{{"page", "page_contents", "page_id"}, {"recording", "recordings", "id"}, {"comment", "comments", "id"}} {
			if err := execIfTable(ctx, c, t[1], `INSERT OR IGNORE INTO text_writes (kind, id, write_id) SELECT '`+t[0]+`', `+t[2]+`, random() FROM `+t[1]); err != nil { // #nosec G202
				return err
			}
		}
		return nil
	},
	func(ctx context.Context, c *sql.Conn) error {
		var cols [][3]string
		for _, t := range pulledTables {
			cols = append(cols, [3]string{t, "pulled_at", "INTEGER"})
		}
		return addColumns(ctx, c, cols)
	},
	func(ctx context.Context, c *sql.Conn) error {
		if err := execIfTable(ctx, c, "search_index", `DELETE FROM search_index WHERE rowid IN
			(SELECT n FROM text_writes WHERE kind = 'recording' AND indexed IS NOT NULL)`); err != nil {
			return err
		}
		return execIfTable(ctx, c, "text_writes", `UPDATE text_writes SET indexed = NULL WHERE kind = 'recording'`)
	},
}

func execIfTable(ctx context.Context, c *sql.Conn, table, query string) error {
	var n int
	if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := c.ExecContext(ctx, query)
	return err
}

var schemaObjects = sync.OnceValue(func() []string {
	made := regexp.MustCompile(`CREATE (?:VIRTUAL )?(?:TABLE|INDEX|TRIGGER) IF NOT EXISTS (\w+)`)
	var names []string
	for _, m := range made.FindAllStringSubmatch(schema, -1) {
		names = append(names, m[1])
	}
	return names
})

func upToDate(ctx context.Context, c *sql.Conn) (bool, error) {
	var version int
	if err := c.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		return false, err
	}
	rows, err := c.QueryContext(ctx, `SELECT name FROM sqlite_master`)
	names, err := collect(rows, err, scanString)
	if err != nil {
		return false, err
	}
	return !slices.ContainsFunc(schemaObjects(), func(name string) bool { return !slices.Contains(names, name) }), nil
}

func migrate(db *sql.DB) error {
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if current, err := upToDate(ctx, c); err != nil || current {
		return err
	}
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

func addColumns(ctx context.Context, c *sql.Conn, cols [][3]string) error {
	for _, col := range cols {
		var tables, n int
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, col[0]).Scan(&tables); err != nil {
			return err
		}
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]).Scan(&n); err != nil {
			return err
		}
		if tables == 0 || n > 0 {
			continue
		}
		if _, err := c.ExecContext(ctx, "ALTER TABLE "+col[0]+" ADD COLUMN "+col[1]+" "+col[2]); err != nil { // #nosec G202
			return err
		}
	}
	return nil
}

func addIndexes(ctx context.Context, c *sql.Conn, indexes [][3]string) error {
	for _, ix := range indexes {
		var n int
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, ix[1]).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if _, err := c.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+ix[0]+" ON "+ix[1]+" ("+ix[2]+")"); err != nil { // #nosec G202
			return err
		}
	}
	return nil
}
