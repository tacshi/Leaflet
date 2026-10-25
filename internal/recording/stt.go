package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"

	"leaflet/internal/agents"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
)

// IsLocalEndpoint reports whether an endpoint is on this machine or
// network, so audio can go to it straight from the device.
func IsLocalEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".local") ||
		strings.HasPrefix(h, "192.168.") || strings.HasPrefix(h, "10.")
}

// TranscribeEndpoint transcribes audio with an OpenAI-style
// /audio/transcriptions endpoint (OpenAI, DashScope, a self-hosted server).
func TranscribeEndpoint(ctx context.Context, baseURL, model string, wav []byte, apiKey, language string) ([]store.Segment, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
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
	part, err := w.CreateFormFile("file", "recording.wav")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(wav); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", agents.NormalizeBaseURL(baseURL)+"/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("Transcription failed: %s %s", res.Status, body) //nolint:staticcheck // shown to people as a sentence.
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
		if s.Speaker != nil {
			seg.Speaker = fmt.Sprint(s.Speaker)
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 && strings.TrimSpace(out.Text) != "" {
		segs = []store.Segment{{EndMs: int(out.Duration * 1000), Text: strings.TrimSpace(out.Text)}}
	}
	return segs, nil
}

// TestSonioxKey checks a Soniox API key against Soniox's API.
func TestSonioxKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("Paste a key to test") //nolint:staticcheck // shown to people as a sentence.
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
		return fmt.Errorf("Soniox rejected this key")
	case res.StatusCode >= 400:
		return fmt.Errorf("Soniox answered %s", res.Status)
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

// Soniox streams audio to Soniox's real-time API with a short-lived key
// the server mints, so the user's key never reaches this device.
type Soniox struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	final   []Token
	pending []Token
	err     error
	done    chan struct{}
	// OnText runs with the live text: final words and the current draft.
	OnText func(string)
}

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
	s := &Soniox{conn: conn, done: make(chan struct{})}
	go s.read()
	return s, nil
}

func (s *Soniox) read() {
	defer close(s.done)
	for {
		_, data, err := s.conn.Read(context.Background())
		if err != nil {
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
		fn := s.OnText
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

// Send streams PCM16 audio.
func (s *Soniox) Send(pcm []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.conn.Write(ctx, websocket.MessageBinary, pcm)
}

// Finish ends the stream and waits for the last final words.
func (s *Soniox) Finish() ([]store.Segment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.conn.Write(ctx, websocket.MessageBinary, []byte{})
	cancel()
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
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
		who := ""
		if t.Speaker != nil {
			who = fmt.Sprint(t.Speaker)
		}
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
