package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// What iCloud sync keeps of the other devices: the version of each row it
// has, and, for each page, the content each device last published, the
// base its next version merges against (docs/protocol.md §8).

// RowVersion is a row's version: when it changed and on which device. The
// later time wins, and at the same time the greater device.
type RowVersion struct {
	At     int64
	Device string
}

// After reports whether v is newer than o.
func (v RowVersion) After(o RowVersion) bool {
	return v.At > o.At || v.At == o.At && v.Device > o.Device
}

// RowVersion returns the version of a row this device has, or the zero
// version.
func (s *Store) RowVersion(table, id string) RowVersion {
	var v RowVersion
	_ = s.db.QueryRow(`SELECT at, device FROM row_versions WHERE tbl = ? AND id = ?`, table, id).Scan(&v.At, &v.Device)
	return v
}

// SetRowVersion records the version of a row this device has.
func (s *Store) SetRowVersion(table, id string, v RowVersion) error {
	_, err := s.db.Exec(`INSERT INTO row_versions (tbl, id, at, device) VALUES (?, ?, ?, ?)
		ON CONFLICT(tbl, id) DO UPDATE SET at = excluded.at, device = excluded.device`, table, id, v.At, v.Device)
	return err
}

// synced are the tables iCloud sync carries rows of.
var synced = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true}

// RowUpdatedAt is when a row of a synced table last changed here, or the
// zero time.
func (s *Store) RowUpdatedAt(table, id string) (time.Time, error) {
	if !synced[table] {
		return time.Time{}, fmt.Errorf("store: %s rows do not sync", table)
	}
	var at int64
	err := s.db.QueryRow(`SELECT updated_at FROM `+table+` WHERE id = ?`, id).Scan(&at) // #nosec G202 -- table is one of synced.
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(at), nil
}

// PeerContent is a page's content as a device published it.
type PeerContent struct {
	Peer    string
	Version int64
	Content string
}

// PeerContent returns what a device last published of a page that this
// one has merged, or false.
func (s *Store) PeerContent(peer, pageID string) (PeerContent, bool) {
	p := PeerContent{Peer: peer}
	err := s.db.QueryRow(`SELECT version, content FROM peer_contents WHERE peer_id = ? AND page_id = ?`, peer, pageID).Scan(&p.Version, &p.Content)
	return p, err == nil
}

// PeerContents returns what every device last published of a page, as far
// as this one has merged it.
func (s *Store) PeerContents(pageID string) ([]PeerContent, error) {
	rows, err := s.db.Query(`SELECT peer_id, version, content FROM peer_contents WHERE page_id = ? ORDER BY peer_id`, pageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PeerContent
	for rows.Next() {
		var p PeerContent
		if err := rows.Scan(&p.Peer, &p.Version, &p.Content); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutPeerContent records what a device published of a page.
func (s *Store) PutPeerContent(peer, pageID string, version int64, content string) error {
	_, err := s.db.Exec(`INSERT INTO peer_contents (peer_id, page_id, version, content) VALUES (?, ?, ?, ?)
		ON CONFLICT(peer_id, page_id) DO UPDATE SET version = excluded.version, content = excluded.content`,
		peer, pageID, version, content)
	return err
}

// ForgetICloud removes what iCloud sync kept of the other devices, so the
// next sync starts afresh.
func (s *Store) ForgetICloud() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{`DELETE FROM peer_contents`, `DELETE FROM row_versions`, `DELETE FROM key_values WHERE key LIKE 'icloud.%'`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
