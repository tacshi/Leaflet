package recording

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
		return keychain.Delete("stt.moss.key")
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

func TranscribeMoss(ctx context.Context, key string, audio io.Reader, size int64, name string, speakers bool, left Leftovers) ([]store.Segment, error) {
	if size > mossMaxBytes {
		return nil, errors.New(locales.RecErrMossTooLarge())
	}
	j := fileJob{
		provider: Moss, api: mossAPI, key: key, do: mossDo, left: left, poll: mossPoll,
		failed: errors.New(locales.RecErrMossFailed("")),
		fields: [][2]string{{"purpose", "audio"}},
		start:  "/audio/transcriptions",
		job: func(file string) map[string]any {
			return map[string]any{"model": mossModel, "version": mossVersion, "file_id": file, "diarize": true, "response_format": "json", "async": true}
		},
		check: func(ctx context.Context, call apiCall, id string) ([]store.Segment, bool, time.Duration, error) {
			var res mossTask
			if err := call(ctx, "GET", "/audio/tasks/"+id, nil, &res); err != nil {
				return nil, false, 0, err
			}
			switch strings.ToLower(res.Status) {
			case "success", "completed":
				return res.segments(speakers), true, 0, nil
			case "failed", "error":
				return nil, false, 0, errors.New(locales.RecErrMossFailed(res.Error.message()))
			}
			pause := mossPoll
			if res.RetryAfter > 0 {
				pause = time.Duration(min(res.RetryAfter, 30)) * time.Second
			}
			return nil, false, pause, nil
		},
	}
	return j.transcribe(ctx, audio, size, name)
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
		return transientError{err}
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
		if err := errors.New(locales.RecErrMossStatus(res.Status, why)); busy(res.StatusCode) {
			return transientError{err}
		} else {
			return err
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxAnswer)).Decode(out)
}
