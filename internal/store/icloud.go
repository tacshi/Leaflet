package store

import (
	"database/sql"
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

type PeerContent struct {
	Peer    string
	Version int64
	Content string
}

func (s *Store) PeerContent(peer, pageID string) (PeerContent, bool, error) {
	p := PeerContent{Peer: peer}
	err := s.db.QueryRow(`SELECT version, content FROM peer_contents WHERE peer_id = ? AND page_id = ?`, peer, pageID).Scan(&p.Version, &p.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
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
	rows, err := s.db.Query(`SELECT peer_id, version, content FROM peer_contents WHERE page_id = ? ORDER BY peer_id`, pageID)
	return collect(rows, err, func(sc scanner) (PeerContent, error) {
		var p PeerContent
		err := sc.Scan(&p.Peer, &p.Version, &p.Content)
		return p, err
	})
}

func (s *Store) PutPeerContent(peer, pageID string, version int64, content string) error {
	return putPeerContent(s.db, peer, pageID, version, content)
}

func putPeerContent(q querier, peer, pageID string, version int64, content string) error {
	_, err := q.Exec(`INSERT INTO peer_contents (peer_id, page_id, version, content) VALUES (?, ?, ?, ?)
		ON CONFLICT(peer_id, page_id) DO UPDATE SET version = excluded.version, content = excluded.content`,
		peer, pageID, version, content)
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
	if err := putPeerContent(tx, from.Peer, pageID, from.Version, from.Content); err != nil {
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
