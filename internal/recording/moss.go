package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

// MOSS, MOSI's speech to text, transcribes saved audio with speakers, from
// this device with a key kept in its keychain: the server has none. It
// has no stream, so a recording is transcribed once it stops.

// Moss is the provider's name in the speech settings.
const Moss = "moss"

// mossAPI is MOSI's API; tests point it at their own server.
var mossAPI = "https://api.mosi.cn/v1"

// mossModel and mossVersion are the model saved audio is transcribed with.
const (
	mossModel   = "moss-transcribe-diarize"
	mossVersion = "v20260410-streamparam-20260703"
)

// mossMaxBytes is the largest file MOSS takes.
const mossMaxBytes = 512 << 20

// mossPoll is how long TranscribeMoss waits between asking whether it is
// done, when MOSS doesn't say. Tests shorten it.
var mossPoll = 2 * time.Second

// MossKey is the MOSS key kept in this device's keychain, or "".
func MossKey() string {
	k, err := keychain.Get("stt.moss.key")
	if err != nil {
		return ""
	}
	return string(k)
}

// SetMossKey keeps a MOSS key in the keychain, or removes it.
func SetMossKey(key string) error {
	if key = strings.TrimSpace(key); key == "" {
		if err := keychain.Delete("stt.moss.key"); !errors.Is(err, keychain.ErrNotFound) {
			return err
		}
		return nil
	}
	return keychain.Set("stt.moss.key", []byte(key))
}

// TestMossKey checks a MOSS key against MOSI's API: that MOSI takes it.
// Its list of models names them otherwise than the transcription does, so
// whether the key may transcribe is for MOSS to say when it does.
func TestMossKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New(locales.RecErrPasteKey())
	}
	req, err := http.NewRequestWithContext(ctx, "GET", mossAPI+"/models", nil)
	if err != nil {
		return err
	}
	return mossDo(req, strings.TrimSpace(key), nil)
}

// TranscribeMoss transcribes size bytes of audio from audio, named name,
// whose extension says what it is, with MOSS and the key on this device:
// it goes up as it is read, never all in memory, and is removed from MOSI
// once it is transcribed. Speakers are labelled when speakers is set.
func TranscribeMoss(ctx context.Context, key string, audio io.Reader, size int64, name string, speakers bool) ([]store.Segment, error) {
	if size > mossMaxBytes {
		return nil, errors.New(locales.RecErrMossTooLarge())
	}
	call := func(ctx context.Context, method, path string, body []byte, out any) error {
		req, err := http.NewRequestWithContext(ctx, method, mossAPI+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return mossDo(req, key, out)
	}

	// The form's purpose and the file's head, then the audio, then the
	// form's end, of a length known before the audio is read.
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	if err := w.WriteField("purpose", "audio"); err != nil {
		return nil, err
	}
	if _, err := w.CreateFormFile("file", name); err != nil {
		return nil, err
	}
	head := form.Len()
	if err := w.Close(); err != nil {
		return nil, err
	}
	tail := form.Bytes()[head:]
	up, cancel := context.WithTimeout(ctx, transferWait(size))
	req, err := http.NewRequestWithContext(up, "POST", mossAPI+"/files",
		io.MultiReader(bytes.NewReader(form.Bytes()[:head]), io.LimitReader(audio, size), bytes.NewReader(tail)))
	if err != nil {
		cancel()
		return nil, err
	}
	req.ContentLength = int64(head) + size + int64(len(tail))
	req.Header.Set("Content-Type", w.FormDataContentType())
	var file struct {
		ID string `json:"id"`
	}
	err = mossDo(req, key, &file)
	cancel()
	if err != nil {
		return nil, err
	}
	if file.ID == "" {
		return nil, errors.New(locales.RecErrMossFailed(""))
	}
	// MOSS keeps no transcript apart from its task: the file is all
	// there is to remove.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = call(ctx, "DELETE", "/files/"+file.ID, nil, nil)
	}()

	create, _ := json.Marshal(map[string]any{
		"model": mossModel, "version": mossVersion, "file_id": file.ID,
		"diarize": true, "response_format": "json", "async": true,
	})
	var task struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := call(ctx, "POST", "/audio/transcriptions", create, &task); err != nil {
		return nil, err
	}
	id := task.TaskID
	if id == "" {
		id = task.ID
	}
	if id == "" {
		return nil, errors.New(locales.RecErrMossFailed(""))
	}

	wait, cancel := context.WithTimeout(ctx, fileWait(size))
	defer cancel()
	for {
		var res mossTask
		if err := call(wait, "GET", "/audio/tasks/"+id, nil, &res); err != nil {
			return nil, err
		}
		switch strings.ToLower(res.Status) {
		case "success", "completed":
			return res.segments(speakers), nil
		case "failed", "error":
			return nil, errors.New(locales.RecErrMossFailed(res.Error.message()))
		}
		pause := mossPoll
		if res.RetryAfter > 0 {
			pause = time.Duration(min(res.RetryAfter, 30)) * time.Second
		}
		select {
		case <-wait.Done():
			return nil, wait.Err()
		case <-time.After(pause):
		}
	}
}

// mossTask is a transcription task, its transcript once it succeeded.
type mossTask struct {
	Status     string  `json:"status"`
	RetryAfter int     `json:"retry_after"`
	Text       string  `json:"text"`
	Duration   float64 `json:"duration"`
	Segments   []struct {
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Text    string  `json:"text"`
		Speaker any     `json:"speaker"`
	} `json:"segments"`
	Error mossError `json:"error"`
}

// mossControl matches the speaker and time tags MOSS may leave in a line.
var mossControl = regexp.MustCompile(`(?i)\[(?:S\d+|\d+(?:\.\d+)?)\]`)

// segments is the task's transcript: a line of only tags, which MOSS
// leaves where it heard no words, is none.
func (t mossTask) segments(speakers bool) []store.Segment {
	var segs []store.Segment
	for _, s := range t.Segments {
		text := strings.TrimSpace(s.Text)
		if strings.TrimSpace(mossControl.ReplaceAllString(text, "")) == "" {
			continue
		}
		seg := store.Segment{StartMs: msOf(s.Start), EndMs: max(msOf(s.End), msOf(s.Start)), Text: text}
		if speakers {
			seg.Speaker = strings.TrimSpace(doc.JSONText(s.Speaker))
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 && strings.TrimSpace(t.Text) != "" {
		segs = []store.Segment{{EndMs: msOf(t.Duration), Text: strings.TrimSpace(t.Text)}}
	}
	return segs
}

// msOf is seconds in milliseconds, none below zero.
func msOf(seconds float64) int {
	if seconds <= 0 {
		return 0
	}
	return int(seconds*1000 + 0.5)
}

// mossError is what MOSS says went wrong, in a task or an answer.
type mossError struct {
	Message  string `json:"message"`
	ErrorMsg string `json:"error_msg"`
	Internal string `json:"internal_error_msg"`
}

// UnmarshalJSON reads an error as an object, or as words alone.
func (e *mossError) UnmarshalJSON(data []byte) error {
	var words string
	if json.Unmarshal(data, &words) == nil {
		e.Message = words
		return nil
	}
	type plain mossError
	var p plain
	if json.Unmarshal(data, &p) == nil {
		*e = mossError(p)
	}
	return nil
}

func (e mossError) message() string {
	for _, m := range []string{e.ErrorMsg, e.Message, e.Internal} {
		if m = strings.TrimSpace(m); m != "" {
			return m
		}
	}
	return ""
}

// mossDo sends req to MOSI with key, and reads its answer into out, unless
// out is nil.
func mossDo(req *http.Request, key string, out any) error {
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		var body struct {
			Error   mossError `json:"error"`
			Message string    `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body)
		why := body.Error.message()
		if why == "" {
			why = strings.TrimSpace(body.Message)
		}
		switch res.StatusCode {
		case 401, 403:
			return errors.New(locales.RecErrMossRejected())
		case 402:
			return errors.New(locales.RecErrMossBalance())
		case 413:
			return errors.New(locales.RecErrMossTooLarge())
		}
		return errors.New(locales.RecErrMossStatus(res.Status, why))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}
