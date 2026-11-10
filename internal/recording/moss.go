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

const Moss = "moss"

var mossAPI = "https://api.mosi.cn/v1"

const (
	mossModel   = "moss-transcribe-diarize"
	mossVersion = "v20260410-streamparam-20260703"
)

const mossMaxBytes = 512 << 20

var mossPoll = 2 * time.Second

func MossKey() string {
	k, err := keychain.Get("stt.moss.key")
	if err != nil {
		return ""
	}
	return string(k)
}

func SetMossKey(key string) error {
	if key = strings.TrimSpace(key); key == "" {
		if err := keychain.Delete("stt.moss.key"); !errors.Is(err, keychain.ErrNotFound) {
			return err
		}
		return nil
	}
	return keychain.Set("stt.moss.key", []byte(key))
}

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
	start, cancel := context.WithTimeout(ctx, jobWait)
	err = call(start, "POST", "/audio/transcriptions", create, &task)
	cancel()
	if err != nil {
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

var mossControl = regexp.MustCompile(`(?i)\[(?:S\d+|\d+(?:\.\d+)?)\]`)

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

func msOf(seconds float64) int {
	if seconds <= 0 {
		return 0
	}
	return int(seconds*1000 + 0.5)
}

type mossError struct {
	Message  string `json:"message"`
	ErrorMsg string `json:"error_msg"`
	Internal string `json:"internal_error_msg"`
}

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
	return json.NewDecoder(io.LimitReader(res.Body, maxAnswer)).Decode(out)
}
