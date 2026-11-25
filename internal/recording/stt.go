package recording

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"

	"leaflet/internal/agents/endpoint"
	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

func IsLocalEndpoint(baseURL string) bool { return endpoint.IsLocal(baseURL) }

func TestEndpoint(ctx context.Context, baseURL, model, apiKey string) error {
	wav := WavFromPCM(make([]byte, 2*SampleRate))
	_, err := TranscribeEndpoint(ctx, baseURL, model, bytes.NewReader(wav), int64(len(wav)), apiKey, "auto")
	return err
}

func TranscribeEndpoint(ctx context.Context, baseURL, model string, wav io.Reader, size int64, apiKey, language string) ([]store.Segment, error) {
	return transcribeEndpoint(ctx, baseURL, model, wav, size, "recording.wav", transcribeWait(size), apiKey, language)
}

func transcribeEndpoint(ctx context.Context, baseURL, model string, wav io.Reader, size int64, name string, wait time.Duration, apiKey, language string) ([]store.Segment, error) {
	fields := [][2]string{{"model", model}, {"response_format", "verbose_json"}}
	if language != "auto" && language != "" && !strings.Contains(language, "-") {
		fields = append(fields, [2]string{"language", language})
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := streamForm(ctx, endpoint.NormalizeBaseURL(baseURL)+"/audio/transcriptions", fields, name, wav, size)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswer+1))
	if res.StatusCode >= 400 {
		body := strings.TrimSpace(string(data))
		if len(body) > 200 || strings.HasPrefix(body, "<") {
			body = ""
		}
		return nil, errors.New(locales.RecErrEndpoint(res.Status, body))
	}
	if int64(len(data)) > maxAnswer {
		return nil, errors.New(locales.RecErrAnswerTooLarge())
	}
	var out struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration"`
		Segments []struct {
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Text    string  `json:"text"`
			Speaker any     `json:"speaker"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	var segs []store.Segment
	for _, s := range out.Segments {
		seg := store.Segment{StartMs: int(s.Start * 1000), EndMs: int(s.End * 1000), Text: strings.TrimSpace(s.Text)}
		seg.Speaker = doc.JSONText(s.Speaker)
		segs = append(segs, seg)
	}
	if len(segs) == 0 && strings.TrimSpace(out.Text) != "" {
		segs = []store.Segment{{EndMs: int(out.Duration * 1000), Text: strings.TrimSpace(out.Text)}}
	}
	return segs, nil
}

var maxAnswer int64 = 64 << 20

// jobWait is how long a provider has to answer a request that starts a
// transcription of audio it already holds.
var jobWait = 2 * time.Minute

func transcribeWait(size int64) time.Duration {
	return 5*time.Minute + 2*time.Duration(size/(2*SampleRate))*time.Second
}

func fileWait(size int64) time.Duration {
	const leastBytesASecond = 4000
	return min(5*time.Minute+2*time.Duration(size/leastBytesASecond)*time.Second, 6*time.Hour)
}

var sonioxAPI = "https://api.soniox.com/v1"

var sonioxPoll = 2 * time.Second

const sonioxAsyncModel = "stt-async-v6"

func TranscribeSoniox(ctx context.Context, key, model string, audio io.Reader, size int64, name string, hints []string, speakers bool, left Leftovers) ([]store.Segment, error) {
	if !strings.HasPrefix(model, "stt-async") {
		model = sonioxAsyncModel
	}
	j := fileJob{
		provider: "soniox", api: sonioxAPI, key: key, do: sonioxDo, left: left, poll: sonioxPoll,
		failed: errors.New(locales.RecErrSonioxFailed("")),
		start:  "/transcriptions",
		job: func(file string) map[string]any {
			return map[string]any{"model": model, "file_id": file, "language_hints": hints, "enable_speaker_diarization": speakers}
		},
		jobPath: "/transcriptions/",
		check: func(ctx context.Context, call apiCall, id string) ([]store.Segment, bool, time.Duration, error) {
			var info struct {
				Status       string `json:"status"`
				ErrorMessage string `json:"error_message"`
			}
			if err := call(ctx, "GET", "/transcriptions/"+id, nil, &info); err != nil {
				return nil, false, 0, err
			}
			switch info.Status {
			case "completed":
				var transcript struct {
					Tokens []Token `json:"tokens"`
				}
				if err := call(ctx, "GET", "/transcriptions/"+id+"/transcript", nil, &transcript); err != nil {
					return nil, false, 0, err
				}
				return TokensToSegments(transcript.Tokens), true, 0, nil
			case "error":
				return nil, false, 0, errors.New(locales.RecErrSonioxFailed(info.ErrorMessage))
			}
			return nil, false, sonioxPoll, nil
		},
	}
	return j.transcribe(ctx, audio, size, name)
}

// streamForm is a POST to link of a multipart form, fields and then the
// audio as a file called name, which streams the audio rather than holding
// it in memory.
func streamForm(ctx context.Context, link string, fields [][2]string, name string, audio io.Reader, size int64) (*http.Request, error) {
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if _, err := w.CreateFormFile("file", name); err != nil {
		return nil, err
	}
	head := form.Len()
	if err := w.Close(); err != nil {
		return nil, err
	}
	tail := form.Bytes()[head:]
	req, err := http.NewRequestWithContext(ctx, "POST", link,
		io.MultiReader(bytes.NewReader(form.Bytes()[:head]), io.LimitReader(audio, size), bytes.NewReader(tail)))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(head) + size + int64(len(tail))
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req, nil
}

// An apiCall asks a provider's API at path, sending body as JSON, and
// reads its answer into out.
type apiCall func(ctx context.Context, method, path string, body []byte, out any) error

// A fileJob is a provider's API that transcribes audio uploaded to it as a
// job, which is asked after until it ends: Soniox's and MOSS's.
type fileJob struct {
	// provider is the name what it leaves there is noted under.
	provider, api, key string
	do                 func(req *http.Request, key string, out any) error
	left               Leftovers
	// poll is the pause before asking again after a request that failed
	// in passing.
	poll time.Duration
	// failed is the error for an answer that names no file or job.
	failed error
	// fields go with the audio's upload.
	fields [][2]string
	// start is the path a job is started at, with job's settings for the
	// file uploaded.
	start string
	job   func(file string) map[string]any
	// jobPath, followed by its ID, is the path a job is deleted at, "" for
	// a provider whose jobs are not deleted.
	jobPath string
	// check asks after the job: done, with its segments, once it has ended,
	// else how long to wait before asking again.
	check func(ctx context.Context, call apiCall, id string) (segs []store.Segment, done bool, pause time.Duration, err error)
}

func (j fileJob) call(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, j.api+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return j.do(req, j.key, out)
}

// transcribe uploads the audio, starts a job of it and asks after the job
// until it ends. What the transcription leaves with the provider is noted
// as soon as the provider names it, and deleted when the transcription
// ends, however it ends.
func (j fileJob) transcribe(ctx context.Context, audio io.Reader, size int64, name string) ([]store.Segment, error) {
	up, cancel := context.WithTimeout(ctx, transferWait(size))
	req, err := streamForm(up, j.api+"/files", j.fields, name, audio, size)
	var file struct {
		ID string `json:"id"`
	}
	if err == nil {
		err = j.do(req, j.key, &file)
	}
	cancel()
	if err != nil {
		return nil, err
	}
	if file.ID == "" {
		return nil, j.failed
	}
	j.left.keep(j.provider, "/files/"+file.ID)
	defer j.left.remove(j.provider, j.key, "/files/"+file.ID)

	create, _ := json.Marshal(j.job(file.ID))
	// MOSS names its job task_id, Soniox id.
	var started struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
	}
	start, cancel := context.WithTimeout(ctx, jobWait)
	err = j.call(start, "POST", j.start, create, &started)
	cancel()
	if err != nil {
		return nil, err
	}
	id := cmp.Or(started.TaskID, started.ID)
	if id == "" {
		return nil, j.failed
	}
	if j.jobPath != "" {
		j.left.keep(j.provider, j.jobPath+id)
		defer j.left.remove(j.provider, j.key, j.jobPath+id)
	}

	wait, cancel := context.WithTimeout(ctx, fileWait(size))
	defer cancel()
	for {
		var segs []store.Segment
		var done bool
		var pause time.Duration
		err := polled(wait, j.poll, func() (err error) {
			segs, done, pause, err = j.check(wait, j.call, id)
			return err
		})
		if err != nil || done {
			return segs, err
		}
		select {
		case <-wait.Done():
			return nil, wait.Err()
		case <-time.After(pause):
		}
	}
}

// A transientError is one a poll asks again after: the network dropped, or
// the service was busy or failing for a moment.
type transientError struct{ error }

func (e transientError) Unwrap() error { return e.error }

func busy(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

const pollRetries = 5

// polled asks get again, a pause apart, while it fails in passing, so that
// one dropped request does not end a transcription that takes hours.
func polled(ctx context.Context, pause time.Duration, get func() error) error {
	for tries := 0; ; tries++ {
		err := get()
		var passing transientError
		if err == nil || !errors.As(err, &passing) || tries >= pollRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pause):
		}
	}
}

func sonioxDo(req *http.Request, key string, out any) error {
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return transientError{err}
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == 401 || res.StatusCode == 403:
		return errors.New(locales.RecErrSonioxRejected())
	case busy(res.StatusCode):
		return transientError{errors.New(locales.RecErrSonioxStatus(res.Status))}
	case res.StatusCode >= 400:
		return errors.New(locales.RecErrSonioxStatus(res.Status))
	case out == nil:
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxAnswer)).Decode(out)
}

func TestSonioxKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New(locales.RecErrPasteKey())
	}
	req, err := http.NewRequestWithContext(ctx, "GET", sonioxAPI+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	switch {
	case res.StatusCode == 401 || res.StatusCode == 403:
		return errors.New(locales.RecErrSonioxRejected())
	case res.StatusCode >= 400:
		return errors.New(locales.RecErrSonioxStatus(res.Status))
	}
	return nil
}

func LocalSonioxKey() string {
	k, err := keychain.Get("stt.soniox.key")
	if err != nil {
		return ""
	}
	return string(k)
}

type KeptKey struct {
	Key     string
	Address string
}

func (k KeptKey) For(baseURL string) string {
	if k.Address == "" {
		if IsLocalEndpoint(strings.TrimSpace(baseURL)) {
			return k.Key
		}
		return ""
	}
	if endpoint.SameOrigin(k.Address, baseURL) {
		return k.Key
	}
	return ""
}

func LoadEndpointKey() KeptKey {
	raw, err := keychain.Get("stt.endpoint.key")
	if err != nil {
		return KeptKey{}
	}
	if address, key, ok := strings.Cut(string(raw), "\n"); ok {
		return KeptKey{Key: key, Address: address}
	}
	return KeptKey{Key: string(raw)}
}

func EndpointKey(baseURL string) string { return LoadEndpointKey().For(baseURL) }

func SetEndpointKey(baseURL, key string) error {
	if key == "" {
		return keychain.Delete("stt.endpoint.key")
	}
	return keychain.Set("stt.endpoint.key", []byte(endpoint.NormalizeBaseURL(baseURL)+"\n"+key))
}

func SetLocalSonioxKey(key string) error {
	if key == "" {
		return keychain.Delete("stt.soniox.key")
	}
	return keychain.Set("stt.soniox.key", []byte(key))
}

const SonioxEndpoint = "wss://stt-rt.soniox.com/transcribe-websocket"

type Token struct {
	Text    string `json:"text"`
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	IsFinal bool   `json:"is_final"`
	Speaker any    `json:"speaker"`
}

type Soniox struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	final   []Token
	pending []Token
	err     error
	lost    error
	done    chan struct{}
	sent    atomic.Int64
	ending  atomic.Bool
	closed  atomic.Bool
	wmu     sync.Mutex
	queue   chan []byte
	qmu     sync.Mutex
	qclosed bool
	written chan struct{}
	onText  func(string)
}

func (s *Soniox) SetOnText(fn func(string)) {
	s.mu.Lock()
	s.onText = fn
	text := s.textLocked()
	s.mu.Unlock()
	if fn != nil && text != "" {
		fn(text)
	}
}

var keepAliveAfter = 8 * time.Second

func DialSoniox(ctx context.Context, apiKey, model string, hints []string, speakers bool) (*Soniox, error) {
	conn, _, err := websocket.Dial(ctx, SonioxEndpoint, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	cfg, _ := json.Marshal(map[string]any{
		"api_key": apiKey, "model": model, "audio_format": "pcm_s16le", "sample_rate": SampleRate, "num_channels": 1,
		"language_hints": hints, "enable_speaker_diarization": speakers, "enable_endpoint_detection": true,
	})
	if err := conn.Write(ctx, websocket.MessageText, cfg); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "")
		return nil, err
	}
	return newSoniox(conn), nil
}

const maxQueued = 600

var errBehind = errors.New("the connection could not keep up")

var errNoEnd = errors.New("the last words never came")

var finishWait = 15 * time.Second

func newSoniox(conn *websocket.Conn) *Soniox {
	s := &Soniox{conn: conn, done: make(chan struct{}), queue: make(chan []byte, maxQueued), written: make(chan struct{})}
	s.sent.Store(time.Now().UnixMilli())
	guard.Go(s.read)
	guard.Go(s.write)
	after := keepAliveAfter
	guard.Go(func() { s.keepAlive(after) })
	return s
}

func (s *Soniox) write() {
	defer close(s.written)
	for pcm := range s.queue {
		s.wmu.Lock()
		if s.ending.Load() {
			s.wmu.Unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.conn.Write(ctx, websocket.MessageBinary, pcm)
		cancel()
		s.wmu.Unlock()
		if err != nil {
			s.lose(err)
			continue
		}
		s.sent.Store(time.Now().UnixMilli())
	}
}

func (s *Soniox) endQueue() {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if !s.qclosed {
		s.qclosed = true
		close(s.queue)
	}
}

func (s *Soniox) keepAlive(after time.Duration) {
	tick := time.NewTicker(min(time.Second, after/2))
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		s.wmu.Lock()
		if s.ending.Load() || time.Since(time.UnixMilli(s.sent.Load())) < after {
			s.wmu.Unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"keepalive"}`))
		cancel()
		s.wmu.Unlock()
		if err != nil {
			s.lose(err)
			return
		}
		s.sent.Store(time.Now().UnixMilli())
	}
}

func (s *Soniox) lose(err error) {
	if s.closed.Load() {
		return
	}
	s.mu.Lock()
	if s.lost == nil {
		s.lost = err
	}
	s.mu.Unlock()
}

func (s *Soniox) Lost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost
}

func (s *Soniox) read() {
	defer close(s.done)
	for {
		_, data, err := s.conn.Read(context.Background())
		if err != nil {
			s.lose(err)
			return
		}
		var msg struct {
			Tokens       []Token `json:"tokens"`
			Finished     bool    `json:"finished"`
			ErrorMessage string  `json:"error_message"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		s.mu.Lock()
		if msg.ErrorMessage != "" {
			s.err = fmt.Errorf("%s", msg.ErrorMessage)
			s.mu.Unlock()
			s.lose(s.err)
			return
		}
		s.pending = nil
		for _, t := range msg.Tokens {
			if t.IsFinal {
				if t.Text != "<end>" {
					s.final = append(s.final, t)
				}
			} else {
				s.pending = append(s.pending, t)
			}
		}
		text := s.textLocked()
		fn := s.onText
		s.mu.Unlock()
		if fn != nil {
			fn(text)
		}
		if msg.Finished {
			return
		}
	}
}

func (s *Soniox) textLocked() string {
	var b strings.Builder
	for _, t := range s.final {
		b.WriteString(t.Text)
	}
	for _, t := range s.pending {
		b.WriteString(t.Text)
	}
	return b.String()
}

func (s *Soniox) Send(pcm []byte) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	// A stream that lost audio takes no more, so what it heard is the start
	// of the recording, and the rest is transcribed from the file after it.
	if s.qclosed || s.ending.Load() || s.Lost() != nil {
		return
	}
	select {
	case s.queue <- pcm:
	default:
		s.lose(errBehind)
	}
}

func (s *Soniox) Close() {
	s.closed.Store(true)
	s.ending.Store(true)
	s.endQueue()
	_ = s.conn.CloseNow()
}

func (s *Soniox) Finish() ([]store.Segment, error) {
	s.endQueue()
	select {
	case <-s.written:
	case <-time.After(finishWait):
		s.lose(errBehind)
	}
	s.wmu.Lock()
	s.ending.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.conn.Write(ctx, websocket.MessageText, []byte{})
	cancel()
	s.wmu.Unlock()
	select {
	case <-s.done:
	case <-time.After(finishWait):
		s.mu.Lock()
		if s.lost == nil {
			s.lost = errNoEnd
		}
		s.mu.Unlock()
	}
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	segs := TokensToSegments(append(s.final, s.pending...))
	if s.err != nil && len(segs) == 0 {
		return nil, s.err
	}
	return segs, nil
}

func isPunct(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }) == ""
}

func Tidy(segs []store.Segment) []store.Segment {
	var out []store.Segment
	for _, s := range segs {
		if len(out) > 0 {
			prev := &out[len(out)-1]
			if isPunct(s.Text) {
				prev.Text += strings.TrimSpace(s.Text)
				prev.EndMs = max(prev.EndMs, s.EndMs)
				continue
			}
			if s.Speaker == prev.Speaker && carriesOn(prev.Text, s.Text) && len(prev.Text)+len(s.Text) < 600 {
				prev.Text += " " + strings.TrimSpace(s.Text)
				prev.EndMs = max(prev.EndMs, s.EndMs)
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

func carriesOn(prev, next string) bool {
	prev, next = strings.TrimSpace(prev), strings.TrimSpace(next)
	last, _ := utf8.DecodeLastRuneInString(prev)
	first, _ := utf8.DecodeRuneInString(next)
	return prev != "" && !strings.ContainsRune(".!?…。！？\"”'’)", last) && unicode.IsLower(first)
}

var sentenceEnds = []string{".", "?", "!", "。", "？", "！"}

func sentenceEnd(text string) string {
	text = strings.TrimSpace(text)
	for _, p := range sentenceEnds {
		if strings.HasSuffix(text, p) {
			return p
		}
	}
	return ""
}

func startsWord(text string) bool {
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsSpace(r) || unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

func TokensToSegments(tokens []Token) []store.Segment {
	var segs []store.Segment
	var text strings.Builder
	start, end := -1, 0
	speaker := ""
	ended := ""
	flush := func() {
		if t := strings.TrimSpace(text.String()); t != "" {
			segs = append(segs, store.Segment{StartMs: max(start, 0), EndMs: end, Text: t, Speaker: speaker})
		}
		text.Reset()
		start = -1
		ended = ""
	}
	for _, t := range tokens {
		s, e := t.StartMs, t.EndMs
		if e < s {
			e = s
		}
		who := doc.JSONText(t.Speaker)
		if isPunct(t.Text) {
			if text.Len() > 0 {
				text.WriteString(t.Text)
				if p := sentenceEnd(t.Text); p != "" && end-start > 4000 {
					ended = p
				}
			} else if len(segs) > 0 {
				segs[len(segs)-1].Text += strings.TrimSpace(t.Text)
			}
			continue
		}
		if text.Len() > 0 && ((who != "" && who != speaker) || s-end > 1000 || (ended != "" && (ended != "." || startsWord(t.Text)))) {
			flush()
		}
		ended = ""
		if start < 0 {
			start = s
		}
		if who != "" {
			speaker = who
		}
		text.WriteString(t.Text)
		end = e
		if end-start > 4000 {
			ended = sentenceEnd(t.Text)
		}
	}
	flush()
	return segs
}
