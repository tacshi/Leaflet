package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"leaflet/internal/doc"
)

type Segment struct {
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

type Recording struct {
	ID          string
	PageID      string
	BlockID     string
	AudioPath   string
	LocalPath   string
	DurationMs  int
	SttProvider string
	SttModel    string
	Status      string
	Transcript  []Segment
	Language    string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UploadedAt  time.Time
}

const recordingCols = `id, page_id, block_id, audio_path, local_path, duration_ms, stt_provider, stt_model, status, transcript, language, error, created_at, updated_at, uploaded_at`

func scanRecording(dir string, sc scanner) (Recording, error) {
	var r Recording
	var audio, local, lang, errText sql.NullString
	var transcript string
	var c, u int64
	var up sql.NullInt64
	err := sc.Scan(&r.ID, &r.PageID, &r.BlockID, &audio, &local, &r.DurationMs, &r.SttProvider, &r.SttModel, &r.Status, &transcript, &lang, &errText, &c, &u, &up)
	r.AudioPath, r.LocalPath, r.Language, r.Error = audio.String, absPath(dir, local.String), lang.String, errText.String
	r.CreatedAt, r.UpdatedAt, r.UploadedAt = time.UnixMilli(c), time.UnixMilli(u), fromMs(up)
	_ = json.Unmarshal([]byte(transcript), &r.Transcript)
	return r, err
}

func (s *Store) Recording(id string) (Recording, error) { return recordingIn(s.db, s.Dir, id) }

func recordingIn(q querier, dir, id string) (Recording, error) {
	return scanRecording(dir, q.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id))
}

func (s *Store) Recordings() ([]Recording, error) {
	rows, err := s.db.Query(`SELECT ` + recordingCols + ` FROM recordings ORDER BY created_at DESC`)
	return collect(rows, err, func(sc scanner) (Recording, error) { return scanRecording(s.Dir, sc) })
}

func (s *Store) PutRecording(r Recording) error { return putRecording(s.db, s.Dir, r) }

func putRecording(q querier, dir string, r Recording) error {
	transcript, _ := json.Marshal(r.Transcript)
	if r.Transcript == nil {
		transcript = []byte("[]")
	}
	_, err := q.Exec(`INSERT INTO recordings (`+recordingCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET block_id = excluded.block_id, audio_path = excluded.audio_path,
		local_path = COALESCE(excluded.local_path, recordings.local_path), duration_ms = excluded.duration_ms,
		stt_provider = excluded.stt_provider, stt_model = excluded.stt_model, status = excluded.status,
		transcript = excluded.transcript, language = excluded.language, error = excluded.error,
		updated_at = excluded.updated_at, uploaded_at = excluded.uploaded_at`,
		r.ID, r.PageID, r.BlockID, ns(r.AudioPath), ns(relPath(dir, r.LocalPath)), r.DurationMs, r.SttProvider, r.SttModel, r.Status,
		string(transcript), ns(r.Language), ns(r.Error), ms(r.CreatedAt), ms(r.UpdatedAt), nt(r.UploadedAt))
	return err
}

func (s *Store) CreateRecording(pageID, blockID, provider, model, localPath, language string) (Recording, error) {
	now := time.Now()
	r := Recording{ID: doc.NewRowID(), PageID: pageID, BlockID: blockID, LocalPath: localPath, SttProvider: provider,
		SttModel: model, Status: "recording", Language: language, CreatedAt: now, UpdatedAt: now}
	if err := putRecording(s.db, s.Dir, r); err != nil {
		return Recording{}, err
	}
	s.Notify("recordings", pageID)
	return r, nil
}

func (s *Store) UpdateRecording(id string, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, "", false, fn)
}

func (s *Store) FinishRecording(id string, upload bool, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, "", upload, fn)
}

const IfStatusKey = "_if_status"

func (s *Store) SettleRecording(id, status string, upload bool, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, status, upload, fn)
}

// updateRecording changes the recording's row by fn, and queues the columns it changed, so that a change made here
// takes nothing back of what another device changed since: dismissing an error here leaves the transcript another
// device made. A row that was still recording, which no device has yet, goes whole.
func (s *Store) updateRecording(id, ifStatus string, upload bool, fn func(*Recording)) (Recording, error) {
	var r Recording
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		if r, err = recordingIn(tx, s.Dir, id); err != nil {
			return err
		}
		var old map[string]any
		if r.Status != "recording" {
			was := r
			was.Transcript = slices.Clone(r.Transcript)
			old = RecordingToRemote(was)
		}
		fn(&r)
		r.UpdatedAt = time.Now()
		if err := putRecording(tx, s.Dir, r); err != nil {
			return err
		}
		if r.Status == "recording" {
			return nil
		}
		label := "transcript"
		if r.AudioPath == "" && r.LocalPath != "" {
			label = "audio waiting to upload"
		}
		row := RecordingToRemote(r)
		if ifStatus == "" {
			if ifStatus, err = settleWaiting(tx, r); err != nil {
				return err
			}
		}
		if ifStatus != "" {
			row[IfStatusKey] = ifStatus
		}
		if err := queueChange(tx, "recordings", r.ID, old, row, label, r.PageID); err != nil || !upload || r.AudioPath != "" || r.LocalPath == "" {
			return err
		}
		return queue(tx, "storage", "recordings/"+r.ID, audioUpload(s.Dir, r), "audio waiting to upload", r.PageID)
	})
	if err != nil {
		return Recording{}, err
	}
	if r.Status != "recording" {
		s.Notify("outbox", r.PageID)
	}
	s.Notify("recordings", r.PageID)
	return r, nil
}

// settleWaiting is the status a settle of r that still waits to go up holds it to, while r keeps
// the status it settled to. A change made after the settle, as dismissing its error, then goes
// only as the settle does: where another device settled the recording first, its row stays, and
// this change, made to the settle that lost, goes with it. A change of status, as transcribing the
// recording again, is a new one of this device's and goes as it is.
func settleWaiting(q querier, r Recording) (string, error) {
	var payload string
	err := q.QueryRow(`SELECT payload FROM outbox WHERE entity = 'recordings' AND entity_id = ? AND op = 'upsert'`, r.ID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var queued map[string]any
	if err := json.Unmarshal([]byte(payload), &queued); err != nil {
		return "", err
	}
	if status, _ := queued[IfStatusKey].(string); status != "" && queued["status"] == r.Status {
		return status, nil
	}
	return "", nil
}

func (s *Store) QueueUpload(r Recording) error {
	if r.LocalPath == "" {
		return nil
	}
	return s.Queue("storage", "recordings/"+r.ID, audioUpload(s.Dir, r), "audio waiting to upload", r.PageID)
}

func audioUpload(dir string, r Recording) map[string]any {
	return map[string]any{"bucket": "recordings", "local_path": relPath(dir, r.LocalPath), "content_type": "audio/wav", "recording_id": r.ID}
}

func (s *Store) DownloadedAudio(id string, data []byte) (string, error) {
	return s.DownloadedAudioFrom(id, bytes.NewReader(data))
}

func (s *Store) DownloadedAudioFrom(id string, r io.Reader) (string, error) {
	if _, err := s.Recording(id); err != nil {
		return "", err
	}
	local := filepath.Join(s.Dir, "recordings", id+".wav")
	if err := writeFrom(local, r); err != nil {
		return "", err
	}
	res, err := s.db.Exec(`UPDATE recordings SET local_path = ? WHERE id = ?`, relPath(s.Dir, local), id)
	if err == nil {
		var n int64
		if n, err = res.RowsAffected(); err == nil && n == 0 {
			err = errNotFound
		}
	}
	if err != nil {
		_ = os.Remove(local)
		return "", err
	}
	s.Notify("recordings", "")
	return local, nil
}

func (s *Store) ClearLocalPath(id string) error {
	if _, err := s.db.Exec(`UPDATE recordings SET local_path = NULL WHERE id = ?`, id); err != nil {
		return err
	}
	s.Notify("recordings", "")
	return nil
}
