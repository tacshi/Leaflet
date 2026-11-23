package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"leaflet/internal/doc"
)

type RowVersion struct {
	At     int64
	Device string
}

func (v RowVersion) After(o RowVersion) bool {
	return v.At > o.At || v.At == o.At && v.Device > o.Device
}

func (s *Store) RowVersion(table, id string) (RowVersion, error) {
	var v RowVersion
	err := s.db.QueryRow(`SELECT at, device FROM row_versions WHERE tbl = ? AND id = ?`, table, id).Scan(&v.At, &v.Device)
	if errors.Is(err, sql.ErrNoRows) {
		return RowVersion{}, nil
	}
	return v, err
}

func (s *Store) SetRowVersion(table, id string, v RowVersion) error {
	_, err := s.db.Exec(`INSERT INTO row_versions (tbl, id, at, device) VALUES (?, ?, ?, ?)
		ON CONFLICT(tbl, id) DO UPDATE SET at = excluded.at, device = excluded.device`, table, id, v.At, v.Device)
	return err
}

var synced = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true}

func (s *Store) RowUpdatedAt(table, id string) (time.Time, error) {
	if !synced[table] {
		return time.Time{}, fmt.Errorf("store: %s rows do not sync", table)
	}
	var at int64
	err := s.db.QueryRow(`SELECT updated_at FROM `+table+` WHERE id = ?`, id).Scan(&at) // #nosec G202
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(at), nil
}

// PeerContent is a device's page file as this one last merged it, or wrote it for itself. Merged is what the file said
// its device had merged of each other device's, the version of their file; nil where that is not known, as for this
// device's own and for a file merged before it was kept.
type PeerContent struct {
	Peer    string
	Version int64
	Content string
	Merged  map[string]int64
}

func (s *Store) PeerContent(peer, pageID string) (PeerContent, bool, error) {
	p := PeerContent{Peer: peer}
	var merged sql.NullString
	err := s.db.QueryRow(`SELECT version, content, merged FROM peer_contents WHERE peer_id = ? AND page_id = ?`, peer, pageID).
		Scan(&p.Version, &p.Content, &merged)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	p.Merged = decodeMerged(merged)
	return p, err == nil, err
}

func decodeMerged(raw sql.NullString) map[string]int64 {
	var merged map[string]int64
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &merged) != nil {
		return nil
	}
	return merged
}

func (s *Store) PeerVersion(peer, pageID string) (int64, bool, error) {
	var v int64
	err := s.db.QueryRow(`SELECT version FROM peer_contents WHERE peer_id = ? AND page_id = ?`, peer, pageID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return v, err == nil, err
}

func (s *Store) PeerContents(pageID string) ([]PeerContent, error) {
	rows, err := s.db.Query(`SELECT peer_id, version, content, merged FROM peer_contents WHERE page_id = ? ORDER BY peer_id`, pageID)
	return collect(rows, err, func(sc scanner) (PeerContent, error) {
		var p PeerContent
		var merged sql.NullString
		err := sc.Scan(&p.Peer, &p.Version, &p.Content, &merged)
		p.Merged = decodeMerged(merged)
		return p, err
	})
}

func (s *Store) PutPeerContent(peer, pageID string, version int64, content string) error {
	return putPeerContent(s.db, pageID, PeerContent{Peer: peer, Version: version, Content: content})
}

func putPeerContent(q querier, pageID string, p PeerContent) error {
	var merged any
	if p.Merged != nil {
		raw, err := json.Marshal(p.Merged)
		if err != nil {
			return err
		}
		merged = string(raw)
	}
	_, err := q.Exec(`INSERT INTO peer_contents (peer_id, page_id, version, content, merged) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(peer_id, page_id) DO UPDATE SET version = excluded.version, content = excluded.content,
		merged = excluded.merged`,
		p.Peer, pageID, p.Version, p.Content, merged)
	return err
}

func (s *Store) PeerMerged(pageID string, from PeerContent, label string, conflicts ...doc.Conflict) error {
	if err := s.inTx(func(tx *sql.Tx) error { return putPeerMerge(tx, pageID, from, label, conflicts) }); err != nil {
		return err
	}
	s.notifyPeerMerge(pageID, label, conflicts)
	return nil
}

func putPeerMerge(tx *sql.Tx, pageID string, from PeerContent, label string, conflicts []doc.Conflict) error {
	if err := putPeerContent(tx, pageID, from); err != nil {
		return err
	}
	if err := addConflicts(tx, pageID, conflicts); err != nil || label == "" {
		return err
	}
	return queue(tx, "page_content", pageID, map[string]any{"page_id": pageID}, label, pageID)
}

func (s *Store) notifyPeerMerge(pageID, label string, conflicts []doc.Conflict) {
	if label != "" {
		s.Notify("outbox", pageID)
	}
	if len(conflicts) > 0 {
		s.Notify("conflicts", pageID)
	}
}

func (s *Store) ForgetICloud() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{`DELETE FROM peer_contents`, `DELETE FROM row_versions`, `DELETE FROM key_values WHERE key LIKE 'icloud.%' AND key NOT LIKE 'icloud.link.%'`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
