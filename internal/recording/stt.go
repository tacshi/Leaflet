package recording

import (
	"bytes"
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

// IsLocalEndpoint reports whether an endpoint is on this machine or
// network, so audio can go to it straight from the device.
func IsLocalEndpoint(baseURL string) bool { return endpoint.IsLocal(baseURL) }

// TestEndpoint tries an OpenAI-compatible endpoint with a second of
// silence, as Settings does on the desktop and the phone.
func TestEndpoint(ctx context.Context, baseURL, model, apiKey string) error {
	wav := WavFromPCM(make([]byte, 2*SampleRate))
	_, err := TranscribeEndpoint(ctx, baseURL, model, bytes.NewReader(wav), int64(len(wav)), apiKey, "auto")
	return err
}

// TranscribeEndpoint transcribes size bytes of WAV audio from wav with an
// OpenAI-style /audio/transcriptions endpoint (OpenAI, DashScope, a
// self-hosted server). The audio goes into the request as it is read: a
// long recording is never all in memory, as it would be twice over in a
// form built first.
func TranscribeEndpoint(ctx context.Context, baseURL, model string, wav io.Reader, size int64, apiKey, language string) ([]store.Segment, error) {
	return transcribeEndpoint(ctx, baseURL, model, wav, size, "recording.wav", transcribeWait(size), apiKey, language)
}

// transcribeEndpoint is TranscribeEndpoint of audio named name, whose
// extension says what it is to the server, waiting for wait at the most.
func transcribeEndpoint(ctx context.Context, baseURL, model string, wav io.Reader, size int64, name string, wait time.Duration, apiKey, language string) ([]store.Segment, error) {
	// The form's fields and the file's header, then the audio, then the
	// form's end: its length is known before the audio is read, for the
	// servers that take no body of unknown length.
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	if err := w.WriteField("model", model); err != nil {
		return nil, err
	}
	if err := w.WriteField("response_format", "verbose_json"); err != nil {
		return nil, err
	}
	if language != "auto" && language != "" && !strings.Contains(language, "-") {
		if err := w.WriteField("language", language); err != nil {
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
	body := io.MultiReader(bytes.NewReader(form.Bytes()[:head]), io.LimitReader(wav, size), bytes.NewReader(tail))
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint.NormalizeBaseURL(baseURL)+"/audio/transcriptions", body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(head) + size + int64(len(tail))
	req.Header.Set("Content-Type", w.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		body := strings.TrimSpace(string(data))
		if len(body) > 200 || strings.HasPrefix(body, "<") {
			body = ""
		}
		return nil, errors.New(locales.RecErrEndpoint(res.Status, body))
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

// transcribeWait is how long an endpoint gets to transcribe size bytes of
// the app's WAV (2 bytes a sample): five minutes, and twice the audio's
// length more, as a server on a CPU can take longer than the audio lasts.
func transcribeWait(size int64) time.Duration {
	return 5*time.Minute + 2*time.Duration(size/(2*SampleRate))*time.Second
}

// fileWait is how long an endpoint gets to transcribe an audio file of
// size bytes: as transcribeWait, but of audio that may be compressed, whose
// length its size doesn't say. It takes the least bit rate speech has,
// which makes a wait long for most, up to a most any file has.
func fileWait(size int64) time.Duration {
	const leastBytesASecond = 4000
	return min(5*time.Minute+2*time.Duration(size/leastBytesASecond)*time.Second, 6*time.Hour)
}

// TestSonioxKey checks a Soniox API key against Soniox's API.
func TestSonioxKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New(locales.RecErrPasteKey())
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.soniox.com/v1/models", nil)
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

// LocalSonioxKey is the Soniox key kept in this device's keychain, for
// a build without a Leaflet server; "" when there is none.
func LocalSonioxKey() string {
	k, err := keychain.Get("stt.soniox.key")
	if err != nil {
		return ""
	}
	return string(k)
}

// EndpointKey is the key kept in this device's keychain for a speech
// endpoint on this machine or network; "" when there is none.
func EndpointKey() string {
	k, err := keychain.Get("stt.endpoint.key")
	if err != nil {
		return ""
	}
	return string(k)
}

// SetEndpointKey keeps the endpoint's key in the keychain, or removes it.
func SetEndpointKey(key string) error {
	if key == "" {
		if err := keychain.Delete("stt.endpoint.key"); !errors.Is(err, keychain.ErrNotFound) {
			return err
		}
		return nil
	}
	return keychain.Set("stt.endpoint.key", []byte(key))
}

// SetLocalSonioxKey keeps a Soniox key in the keychain, or removes it.
func SetLocalSonioxKey(key string) error {
	if key == "" {
		return keychain.Delete("stt.soniox.key")
	}
	return keychain.Set("stt.soniox.key", []byte(key))
}

// SonioxEndpoint is Soniox's real-time API.
const SonioxEndpoint = "wss://stt-rt.soniox.com/transcribe-websocket"

// Token is a word or piece of one from Soniox.
type Token struct {
	Text    string `json:"text"`
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	IsFinal bool   `json:"is_final"`
	Speaker any    `json:"speaker"`
}

// Soniox streams audio to Soniox's real-time API, with the key kept on
// this device or else a short-lived one the server mints from the user's
// key, which then never reaches the device.
type Soniox struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	final   []Token
	pending []Token
	err     error
	// lost is why the stream ended before Finish or Close asked it to.
	lost error
	done chan struct{}
	// sent is when audio or a keepalive last went, in Unix milliseconds;
	// ending is set once Finish or Close ends the stream.
	sent   atomic.Int64
	ending atomic.Bool
	// wmu orders the writes, so no audio follows the end of it that
	// Finish sends.
	wmu sync.Mutex
	// queue is the audio write sends, in order, so a slow connection does
	// not hold up the recording; qmu guards closing it, once Finish or
	// Close ends it, and written closes when write is done.
	queue   chan []byte
	qmu     sync.Mutex
	qclosed bool
	written chan struct{}
	// onText runs with the live text, final words and the current draft;
	// SetOnText sets it, guarded by mu, as read runs from the start.
	onText func(string)
}

// SetOnText has fn run with the live text, final words and the current
// draft, as it comes, starting with what came before it was set.
func (s *Soniox) SetOnText(fn func(string)) {
	s.mu.Lock()
	s.onText = fn
	text := s.textLocked()
	s.mu.Unlock()
	if fn != nil && text != "" {
		fn(text)
	}
}

// keepAliveAfter is how long the stream goes without audio, as while a
// recording is paused, before a keepalive goes: Soniox ends a stream that
// hears nothing for 20 s. Tests shorten it.
var keepAliveAfter = 8 * time.Second

// DialSoniox connects and sends the stream's configuration.
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

// maxQueued is the most audio waiting to go to a slow stream: 30 s of the
// capture page's 50 ms chunks.
const maxQueued = 600

// errBehind is a stream that could not keep up with the audio.
var errBehind = errors.New("the connection could not keep up")

// errNoEnd is a stream whose last words never came once it was finished.
var errNoEnd = errors.New("the last words never came")

// finishWait is how long Finish waits for the audio queued to go, and then
// for the last words. Tests shorten it.
var finishWait = 15 * time.Second

// newSoniox reads, writes and keeps alive a stream that has its
// configuration.
func newSoniox(conn *websocket.Conn) *Soniox {
	s := &Soniox{conn: conn, done: make(chan struct{}), queue: make(chan []byte, maxQueued), written: make(chan struct{})}
	s.sent.Store(time.Now().UnixMilli())
	guard.Go(s.read)
	guard.Go(s.write)
	// Read here, so a test that changes it races no stream of another.
	after := keepAliveAfter
	guard.Go(func() { s.keepAlive(after) })
	return s
}

// write sends the queued audio until the queue closes; once the stream is
// ending it sends no more.
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

// endQueue closes the queue, once.
func (s *Soniox) endQueue() {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if !s.qclosed {
		s.qclosed = true
		close(s.queue)
	}
}

// keepAlive keeps a stream that gets no audio open, until it ends.
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

// lose records why the stream ended early, unless it was asked to end.
func (s *Soniox) lose(err error) {
	if s.ending.Load() {
		return
	}
	s.mu.Lock()
	if s.lost == nil {
		s.lost = err
	}
	s.mu.Unlock()
}

// Lost is why the stream ended before it was finished, nil if it did not:
// what was said after that is in the audio, not the transcript.
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

// Send queues PCM16 audio to stream, without waiting for it to go; once
// the stream is ending it sends nothing.
func (s *Soniox) Send(pcm []byte) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.qclosed || s.ending.Load() {
		return
	}
	select {
	case s.queue <- pcm:
	default:
		s.lose(errBehind)
	}
}

// Close drops the stream, for a recording that needs it no more, at once:
// it runs on the main thread, and the close handshake would wait seconds
// on a slow network. The stream's reading, writing and keepalives end as
// its connection does.
func (s *Soniox) Close() {
	s.ending.Store(true)
	s.endQueue()
	_ = s.conn.CloseNow()
}

// Finish ends the stream and waits for the last final words.
func (s *Soniox) Finish() ([]store.Segment, error) {
	// The audio queued goes first, as long as the connection takes it.
	// What it could not take is in the recording's audio, not the
	// transcript: the stream counts as lost, and the recording, its words
	// kept, is to transcribe again.
	s.endQueue()
	select {
	case <-s.written:
	case <-time.After(finishWait):
		s.lose(errBehind)
	}
	s.wmu.Lock()
	s.ending.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// An empty text frame ends the stream. An empty binary one is only an
	// empty piece of audio, after which Soniox goes on waiting for more.
	_ = s.conn.Write(ctx, websocket.MessageText, []byte{})
	cancel()
	s.wmu.Unlock()
	select {
	case <-s.done:
	case <-time.After(finishWait):
		// Ending, it takes no loss by lose: this one is its own.
		s.mu.Lock()
		if s.lost == nil {
			s.lost = errNoEnd
		}
		s.mu.Unlock()
	}
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	// Words still waiting to be final when the stream ended, as when it
	// timed out, showed live: they stay rather than vanish.
	segs := TokensToSegments(append(s.final, s.pending...))
	if s.err != nil && len(segs) == 0 {
		return nil, s.err
	}
	return segs, nil
}

// isPunct reports whether text has only punctuation and spaces.
func isPunct(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }) == ""
}

// Tidy joins lines of only punctuation to the line before, as transcripts
// made before TokensToSegments did so have them, and a line that carries
// on a sentence, starting in lower case after one that did not end it, as
// a pause mid-sentence splits it.
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

// carriesOn tells whether next continues the sentence prev leaves open:
// prev ends without a sentence's end and next starts in lower case.
func carriesOn(prev, next string) bool {
	prev, next = strings.TrimSpace(prev), strings.TrimSpace(next)
	last, _ := utf8.DecodeLastRuneInString(prev)
	first, _ := utf8.DecodeRuneInString(next)
	return prev != "" && !strings.ContainsRune(".!?…。！？\"”'’)", last) && unicode.IsLower(first)
}

// TokensToSegments groups Soniox tokens into transcript segments: a new
// one on a new speaker, after a pause of more than a second, or at a
// sentence end after four seconds.
func TokensToSegments(tokens []Token) []store.Segment {
	var segs []store.Segment
	var text strings.Builder
	start, end := -1, 0
	speaker := ""
	flush := func() {
		if t := strings.TrimSpace(text.String()); t != "" {
			segs = append(segs, store.Segment{StartMs: max(start, 0), EndMs: end, Text: t, Speaker: speaker})
		}
		text.Reset()
		start = -1
	}
	for _, t := range tokens {
		s, e := t.StartMs, t.EndMs
		if e < s {
			e = s
		}
		// As the server writes it, a number too.
		who := doc.JSONText(t.Speaker)
		// Punctuation that comes late ends the words before it rather
		// than starting a line of its own.
		if isPunct(t.Text) {
			if text.Len() > 0 {
				text.WriteString(t.Text)
			} else if len(segs) > 0 {
				segs[len(segs)-1].Text += strings.TrimSpace(t.Text)
			}
			continue
		}
		if text.Len() > 0 && ((who != "" && who != speaker) || s-end > 1000) {
			flush()
		}
		if start < 0 {
			start = s
		}
		if who != "" {
			speaker = who
		}
		text.WriteString(t.Text)
		end = e
		word := strings.TrimSpace(t.Text)
		for _, p := range []string{".", "?", "!", "。", "？", "！"} {
			if strings.HasSuffix(word, p) && end-start > 4000 {
				flush()
				break
			}
		}
	}
	flush()
	return segs
}
