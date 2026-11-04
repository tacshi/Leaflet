package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"leaflet/internal/doc"
)

// Segment is one timestamped piece of a transcript.
type Segment struct {
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

// Recording is the audio and transcript of a recording block.
type Recording struct {
	ID          string
	PageID      string
	BlockID     string
	AudioPath   string
	LocalPath   string
	DurationMs  int
	SttProvider string
	SttModel    string
	Status      string // recording | transcribing | ready | failed
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

// Recording returns one recording.
func (s *Store) Recording(id string) (Recording, error) { return recordingIn(s.db, s.Dir, id) }

func recordingIn(q querier, dir, id string) (Recording, error) {
	return scanRecording(dir, q.QueryRow(`SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id))
}

// Recordings returns every recording, newest first.
func (s *Store) Recordings() ([]Recording, error) {
	rows, err := s.db.Query(`SELECT ` + recordingCols + ` FROM recordings ORDER BY created_at DESC`)
	return collect(rows, err, func(sc scanner) (Recording, error) { return scanRecording(s.Dir, sc) })
}

// PutRecording stores a recording row as it is, without queueing it to go
// up: for tests, which set rows up so.
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

// CreateRecording starts a recording.
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

// UpdateRecording changes a recording and queues it once it is no longer
// recording, in one transaction, over the recording as stored then.
func (s *Store) UpdateRecording(id string, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, "", false, fn)
}

// FinishRecording is UpdateRecording for a recording whose audio is whole,
// as one stopped or transcribed: with upload, its audio goes up too, unless
// it has, queued in the same transaction. Queued apart, a quit between the
// two would keep the audio here alone, as nothing queues it again for a
// recording finished.
func (s *Store) FinishRecording(id string, upload bool, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, "", upload, fn)
}

// IfStatusKey names, in a queued recording, the status the row elsewhere
// must still have for the change to apply there. A recording settled
// after a quit stopped its transcription (SettleRecording) goes so: the
// server may have finished the transcription meanwhile, or the device
// that started it, and their transcript would be lost under this one.
const IfStatusKey = "_if_status"

// SettleRecording is UpdateRecording for a recording a quit left at
// status, which nothing here finishes now: the change it makes for the
// page applies on the server and the other devices only while the
// recording is still at status there, and gives way to the row they have
// otherwise. With status "", it is FinishRecording. Its audio goes up with
// upload, as FinishRecording's does.
func (s *Store) SettleRecording(id, status string, upload bool, fn func(*Recording)) (Recording, error) {
	return s.updateRecording(id, status, upload, fn)
}

func (s *Store) updateRecording(id, ifStatus string, upload bool, fn func(*Recording)) (Recording, error) {
	var r Recording
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		if r, err = recordingIn(tx, s.Dir, id); err != nil {
			return err
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
		if ifStatus != "" {
			row[IfStatusKey] = ifStatus
		}
		if err := queue(tx, "recordings", r.ID, row, label, r.PageID); err != nil || !upload || r.AudioPath != "" || r.LocalPath == "" {
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

// QueueUpload queues a recording's audio for upload.
func (s *Store) QueueUpload(r Recording) error {
	if r.LocalPath == "" {
		return nil
	}
	return s.Queue("storage", "recordings/"+r.ID, audioUpload(s.Dir, r), "audio waiting to upload", r.PageID)
}

// audioUpload is the entry that uploads a recording's audio on this device.
func audioUpload(dir string, r Recording) map[string]any {
	return map[string]any{"bucket": "recordings", "local_path": relPath(dir, r.LocalPath), "content_type": "audio/wav", "recording_id": r.ID}
}

// DownloadedAudio keeps a recording's audio downloaded from where it went
// up, whole on the disk before its row names it, as DownloadedFile keeps a
// file's, for a copy of its page to copy
// (CopyNeeds). A recording removed before or while it came keeps none.
func (s *Store) DownloadedAudio(id string, data []byte) (string, error) {
	return s.DownloadedAudioFrom(id, bytes.NewReader(data))
}

// DownloadedAudioFrom is DownloadedAudio for audio read from r as it
// comes, written to the disk as it is read, never all in memory, as a
// recording of hours is hundreds of megabytes. One that fails part way
// keeps nothing.
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

// ClearLocalPath forgets a recording's audio on this device.
func (s *Store) ClearLocalPath(id string) error {
	if _, err := s.db.Exec(`UPDATE recordings SET local_path = NULL WHERE id = ?`, id); err != nil {
		return err
	}
	s.Notify("recordings", "")
	return nil
}
