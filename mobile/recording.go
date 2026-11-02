package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Microphone is the device's microphone, which recordings hear through,
// implemented in Swift with the app's audio engine. The core calls it
// from its own goroutines. Each call names the recording it is for by the
// session Start was given: one recording's Stop or Close, late, leaves
// the next one's alone, and each one's audio goes to its own sink.
type Microphone interface {
	// Start turns the microphone on for recording session, to hand sink
	// 16-bit mono PCM at rate samples a second. It returns before the
	// microphone is on, as asking for it may wait for the user: sink's
	// Started or Failed says when it is, or why it is not.
	Start(session, rate int, sink *AudioSink) error
	// Pause stops handing on audio until Resume; neither waits. A Resume
	// that cannot turn the microphone back on says why with sink's
	// Stalled.
	Pause(session int)
	Resume(session int)
	// Stop hands the sink the audio the microphone still holds, then
	// turns it off.
	Stop(session int)
	// Close lets the microphone go at once, after Stop or for a recording
	// that could not start.
	Close(session int)
}

// AudioSink takes what the microphone hears. The app calls it from any
// thread, but one call at a time.
type AudioSink struct{ s recording.Sink }

// Chunk takes the audio heard next, in order.
func (a *AudioSink) Chunk(pcm []byte) {
	defer catch(nil)
	// The app's bytes are its own once the call returns.
	a.s.Chunk(append([]byte(nil), pcm...))
}

// Started says the microphone is on.
func (a *AudioSink) Started() {
	defer catch(nil)
	a.s.Started()
}

// Failed says why the microphone could not start.
func (a *AudioSink) Failed(message string) {
	defer catch(nil)
	a.s.Failed(message)
}

// Stalled says why the microphone, once on, stopped and could not go on,
// as when a call ended without giving it back: the recording pauses.
func (a *AudioSink) Stalled(message string) {
	defer catch(nil)
	a.s.Stalled(message)
}

// capture is the app's microphone as the recorder's Capture.
type capture struct{ m Microphone }

// micSessions numbers the recordings the microphone starts.
var micSessions atomic.Int64

func (c capture) Start(rate int, sink recording.Sink) (recording.Mic, error) {
	session := int(micSessions.Add(1))
	if err := c.m.Start(session, rate, &AudioSink{s: sink}); err != nil {
		return nil, err
	}
	return mic{m: c.m, session: session}, nil
}

// mic is the app's microphone as one recording's Mic.
type mic struct {
	m       Microphone
	session int
}

func (m mic) Pause()  { m.m.Pause(m.session) }
func (m mic) Resume() { m.m.Resume(m.session) }
func (m mic) Stop()   { m.m.Stop(m.session) }
func (m mic) Close()  { m.m.Close(m.session) }

// Transcriber is the device's own speech engine, implemented in Swift
// with iOS's speech recognition: what OnDevice transcribes with. Streams
// are named by the core, one per recording. The core calls it from its
// own goroutines.
type Transcriber interface {
	// Start starts transcribing stream id, in language ("auto" for the
	// device's), from audio Send gives it. It returns at once; sink's Text
	// takes the words so far as they come.
	Start(id, language string, sink *TranscriptSink) error
	// Send takes the audio of stream id recorded next, 16-bit mono PCM at
	// recording.SampleRate, in order. It does not wait.
	Send(id string, pcm []byte)
	// Finish waits for the words of the audio sent to stream id, ends it,
	// and returns its transcript, a JSON array of store.Segment.
	Finish(id string) (string, error)
	// Close ends stream id at once.
	Close(id string)
	// File transcribes the WAV file at path, as Finish returns.
	File(path, language string) (string, error)
}

// TranscriptSink takes the words of a stream so far, as they come.
type TranscriptSink struct{ text func(string) }

// Text takes the words so far.
func (s *TranscriptSink) Text(text string) {
	defer catch(nil)
	s.text(text)
}

// engine is the app's Transcriber as the recorder's Engine.
type engine struct{ t Transcriber }

func (e engine) Stream(language string, text func(string)) (recording.Stream, error) {
	id := doc.NewRowID()
	if err := e.t.Start(id, language, &TranscriptSink{text: text}); err != nil {
		return nil, err
	}
	return &stream{t: e.t, id: id}, nil
}

func (e engine) File(path, language string) ([]store.Segment, error) {
	raw, err := e.t.File(path, language)
	if err != nil {
		return nil, err
	}
	return segments(raw)
}

func segments(raw string) ([]store.Segment, error) {
	var segs []store.Segment
	if err := json.Unmarshal([]byte(raw), &segs); err != nil {
		return nil, err
	}
	return recording.Tidy(segs), nil
}

// stream is one recording's transcription by the app's Transcriber.
type stream struct {
	t  Transcriber
	id string
}

func (s *stream) Send(pcm []byte) { s.t.Send(s.id, pcm) }

func (s *stream) Finish() ([]store.Segment, error) {
	raw, err := s.t.Finish(s.id)
	if err != nil {
		return nil, err
	}
	return segments(raw)
}

func (s *stream) Close() { s.t.Close(s.id) }

func (s *stream) Lost() error { return nil }

// UseTranscriber gives the recorder the device's own speech engine, which
// the app has where iOS can transcribe. A device that has one transcribes
// with it unless another engine is set up: speech settings left at an
// endpoint with no address turn to it.
func (c *Core) UseTranscriber(t Transcriber) {
	defer catch(nil)
	c.q.do(func() {
		// Through its lock: transcriptions running read it.
		c.core.Rec.SetEngine(engine{t})
		s := c.core.Store.Settings()
		if s.Stt.Provider != "openai-compatible" || strings.TrimSpace(s.Stt.BaseURL) != "" {
			return
		}
		s.Stt.Provider = recording.OnDevice
		if err := c.core.Store.SaveSettings(s); err != nil {
			log.Printf("settings: %v", err)
		}
	})
}

// UseMicrophone gives the recorder the app's microphone; until it has
// one, recording says the window isn't ready. RecordingChanged tells the
// app each change of the recording in progress after.
func (c *Core) UseMicrophone(m Microphone) {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.Attach(capture{m}, func(fn func()) {
			c.q.post(func() {
				fn()
				c.tellRecording()
			})
		})
	})
}

// recordingState is the recording in progress and the transcriptions
// running, as RecordingChanged gives them.
type recordingState struct {
	Active   bool   `json:"active"`
	Starting bool   `json:"starting"`
	Paused   bool   `json:"paused"`
	PageID   string `json:"page_id"`
	BlockID  string `json:"block_id"`
	ID       string `json:"id"`
	// Levels are the latest loudness levels, one each 50 ms, newest last.
	Levels   []float64 `json:"levels"`
	LiveText string    `json:"live_text"`
	// Engine says where it is transcribed, or that it keeps only audio.
	Engine string `json:"engine"`
	// Error says what went wrong, while recording or as it stopped.
	Error string `json:"error"`
	// ElapsedMs is how long it has been recording, without pauses.
	ElapsedMs int64 `json:"elapsed_ms"`
	// Transcribing are the recordings being transcribed; Finishing those
	// among them whose words came live, now getting their timestamps.
	Transcribing []string `json:"transcribing"`
	Finishing    []string `json:"finishing"`
	// Testing is a test of the microphone running, from Settings, and
	// Heard what the last one heard, or why it heard nothing.
	Testing bool   `json:"testing"`
	Heard   string `json:"heard"`
}

// levelsSent is how many of the latest levels go to the app: what the
// widest page shows.
const levelsSent = 240

// recState returns the recording's state; on the queue.
func (c *Core) recState() recordingState {
	r := c.core.Rec
	st := r.State
	out := recordingState{
		Active: st.Active, Starting: st.Starting, Paused: st.Paused, PageID: st.PageID, BlockID: st.BlockID,
		ID: st.ID, Levels: st.Levels, LiveText: st.LiveText, Engine: st.Engine, Error: st.Error,
		ElapsedMs: r.Elapsed().Milliseconds(), Transcribing: []string{}, Finishing: []string{},
		Testing: c.micTesting, Heard: c.micHeard,
	}
	if n := len(out.Levels); n > levelsSent {
		out.Levels = out.Levels[n-levelsSent:]
	}
	if out.Levels == nil {
		out.Levels = []float64{}
	}
	for id := range r.Transcribing {
		out.Transcribing = append(out.Transcribing, id)
	}
	for id := range r.Finishing {
		out.Finishing = append(out.Finishing, id)
	}
	return out
}

// tellRecording tells the app the recording's state; on the queue.
func (c *Core) tellRecording() {
	raw, _ := encode(c.recState())
	c.ev.recordingChanged(raw)
}

// RecordingState returns the recording in progress, as RecordingChanged
// gives it.
func (c *Core) RecordingState() (_ string, err error) {
	defer catch(&err)
	var st recordingState
	c.q.do(func() { st = c.recState() })
	return encode(st)
}

// online reports whether the core may reach the network; on the queue.
func (c *Core) online(s store.Settings) bool { return c.plat.Online() && !s.WorkOffline }

// StartRecording records into an open page, with a transcript when the
// speech settings can make one. It returns the recording's block, as
// JSON, for the app's editor to put in the page at the caret.
func (c *Core) StartRecording(pageID string) (_ string, err error) {
	defer catch(&err)
	var node *doc.Node
	c.q.do(func() {
		if c.pages[pageID] == nil {
			err = errNotOpen
			return
		}
		s := c.core.Store.Settings()
		err = c.core.Rec.Start(pageID, s.Stt, c.online(s), func(n *doc.Node) { node = n })
		c.tellRecording()
	})
	if err != nil {
		return "", err
	}
	return encode(node)
}

// StopRecording ends the recording, saves its audio and transcribes it.
func (c *Core) StopRecording() {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.Stop()
		c.tellRecording()
	})
}

// TogglePauseRecording pauses the recording, or goes on with it.
func (c *Core) TogglePauseRecording() {
	defer catch(nil)
	c.q.do(func() {
		c.core.Rec.TogglePause()
		c.tellRecording()
	})
}

// recordingRow is a recording block's recording, as Recording returns it.
type recordingRow struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	DurationMs  int             `json:"duration_ms"`
	SttProvider string          `json:"stt_provider"`
	Error       string          `json:"error"`
	Transcript  []store.Segment `json:"transcript"`
	// Plain is the transcript to copy: a line each, after its speaker.
	Plain string `json:"plain"`
}

// Recording returns a recording block's recording as recordingRow, its
// transcript tidied as the block shows it.
func (c *Core) Recording(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	row := recordingRow{
		ID: rec.ID, Status: rec.Status, DurationMs: rec.DurationMs, SttProvider: rec.SttProvider,
		Error: rec.Error, Transcript: recording.Tidy(rec.Transcript),
	}
	if row.Transcript == nil {
		row.Transcript = []store.Segment{}
	}
	var lines []string
	for _, s := range row.Transcript {
		if s.Speaker != "" {
			lines = append(lines, locales.RecSpeakerLine(s.Speaker, s.Text))
		} else {
			lines = append(lines, s.Text)
		}
	}
	row.Plain = strings.Join(lines, "\n")
	return encode(row)
}

// RecordingAudio returns the path of a recording's WAV audio on this
// device, downloading it first from where it went up when it is not.
func (c *Core) RecordingAudio(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	if rec.LocalPath != "" {
		if _, err := os.Stat(rec.LocalPath); err == nil {
			return rec.LocalPath, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	data, err := c.core.Rec.Audio(ctx, rec)
	if err != nil {
		return "", err
	}
	var path string
	c.q.do(func() { path, err = c.core.Store.DownloadedAudio(id, data) })
	return path, err
}

// Retranscribe transcribes a recording again, or for the first time,
// with the speech settings as they are.
func (c *Core) Retranscribe(id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		s := c.core.Store.Settings()
		err = c.core.Rec.Retranscribe(id, s.Stt, c.online(s))
		c.tellRecording()
	})
	return err
}

// InsertTranscript puts a recording's transcript in its page, open,
// after its block: a paragraph a line, each after its time in bold.
func (c *Core) InsertTranscript(id string) (err error) {
	defer catch(&err)
	c.q.do(func() {
		var rec store.Recording
		if rec, err = c.core.Store.Recording(id); err != nil {
			return
		}
		p := c.pages[rec.PageID]
		if p == nil {
			err = errNotOpen
			return
		}
		if p.live.ReadOnly() {
			return
		}
		var nodes []*doc.Node
		for _, s := range recording.Tidy(rec.Transcript) {
			n := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
			n.SetDelta(doc.Delta{
				{Text: fmtDuration(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}},
				{Text: s.Text},
			})
			nodes = append(nodes, n)
		}
		content := p.cur.Copy()
		if err = content.Insert(doc.Position{After: rec.BlockID}, nodes...); err != nil {
			return
		}
		p.bump(content)
		c.ev.pageUpdated(p.id, content.Encode(), p.rev, "agent", true)
		p.live.Edited()
	})
	return err
}

func fmtDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Speech to text -----------------------------------------------------------

// speechLanguage is a language transcription can be told to expect.
type speechLanguage struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// speechSettings are Settings › Speech to text, as SpeechSettings returns
// them.
type speechSettings struct {
	Stt store.SttSettings `json:"stt"`
	// EndpointKey is the key of an endpoint on this device's network,
	// kept in its keychain.
	EndpointKey string `json:"endpoint_key"`
	// SonioxSaved says this device's keychain has a Soniox key.
	SonioxSaved bool `json:"soniox_saved"`
	SignedIn    bool `json:"signed_in"`
	// LocalEndpoint says the endpoint is on this device's network: its
	// key is this device's; a cloud one's comes from an agent provider.
	LocalEndpoint bool `json:"local_endpoint"`
	// OnDevice says the device has an engine of its own.
	OnDevice bool `json:"on_device"`
	// Status says where transcription runs, or that it is not set up.
	Status    string           `json:"status"`
	Languages []speechLanguage `json:"languages"`
}

// SpeechSettings returns the speech settings as speechSettings.
func (c *Core) SpeechSettings() (_ string, err error) {
	defer catch(&err)
	var out speechSettings
	c.q.do(func() {
		out.Stt = c.core.Store.Settings().Stt
		out.SignedIn = c.core.Remote.SignedIn()
		out.OnDevice = c.core.Rec.Engine != nil
	})
	if k, err := keychain.Get("stt.endpoint.key"); err == nil {
		out.EndpointKey = string(k)
	}
	out.SonioxSaved = recording.LocalSonioxKey() != ""
	out.LocalEndpoint = recording.IsLocalEndpoint(out.Stt.BaseURL)
	stt := out.Stt
	switch {
	case stt.Provider == recording.OnDevice && !out.OnDevice,
		stt.Provider == "soniox" && !out.SonioxSaved && !out.SignedIn,
		stt.Provider == "openai-compatible" && strings.TrimSpace(stt.BaseURL) == "":
		out.Status = locales.RecSttNotSetUp()
	default:
		out.Status = recording.EngineLabel(stt)
	}
	for _, code := range recording.LanguageCodes() {
		out.Languages = append(out.Languages, speechLanguage{Code: code, Name: recording.LanguageName(code)})
	}
	return encode(out)
}

// SaveSpeechSettings keeps the speech settings, a store.SttSettings as
// JSON, and mirrors them to the server, signed in. The JSON is the whole
// of the settings: a field it leaves out is cleared, as the app leaves
// out a provider ID it chose none for, and language hints it emptied.
func (c *Core) SaveSpeechSettings(stt string) (err error) {
	defer catch(&err)
	var next store.SttSettings
	if err := json.Unmarshal([]byte(stt), &next); err != nil {
		return err
	}
	c.q.do(func() {
		s := c.core.Store.Settings()
		s.Stt = next
		err = c.core.Store.SaveSettings(s)
	})
	if err == nil {
		c.mirrorStt(next)
	}
	return err
}

// sttLatest numbers the changes of the speech settings, and sttSent
// orders their mirrors: each change sends them, one at a time, and only
// the last goes, so the server never ends with older ones. A change only
// counts, never waiting for a send on the network.
var (
	sttLatest atomic.Int64
	sttSent   sync.Mutex
)

// mirrorStt sends the speech settings to the server, signed in, as the
// desktop does: it transcribes with them.
func (c *Core) mirrorStt(stt store.SttSettings) {
	be := c.core.Remote
	// The device's own engine is this device's alone: the server's
	// settings stay those the other devices chose.
	if !be.SignedIn() || stt.Provider == recording.OnDevice {
		return
	}
	n := sttLatest.Add(1)
	guard.Go(func() {
		// Only the last change waits for its turn.
		time.Sleep(500 * time.Millisecond)
		if sttLatest.Load() != n {
			return
		}
		sttSent.Lock()
		defer sttSent.Unlock()
		if sttLatest.Load() != n {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
		defer cancel()
		var provider any
		if stt.ProviderID != "" {
			provider = stt.ProviderID
		}
		row := map[string]any{"user_id": be.UserID(), "provider": stt.Provider, "language": stt.Language,
			"live_transcript": stt.LiveTranscript, "speaker_labels": stt.SpeakerLabels,
			"base_url": stt.BaseURL, "model": stt.Model, "provider_id": provider}
		if _, err := be.Upsert(ctx, "stt_settings", row, "user_id"); err != nil {
			log.Printf("stt settings: %v", err)
		}
	})
}

// SetEndpointKey keeps the key of an endpoint on this device's network
// in its keychain; "" removes it.
func (c *Core) SetEndpointKey(key string) (err error) {
	defer catch(&err)
	if key == "" {
		return keychain.Delete("stt.endpoint.key")
	}
	return keychain.Set("stt.endpoint.key", []byte(key))
}

// SaveSonioxKey keeps a Soniox key on the server, signed in, or else in
// this device's keychain. It returns where it went, in words for the user.
func (c *Core) SaveSonioxKey(key string) (_ string, err error) {
	defer catch(&err)
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	be := c.core.Remote
	if !be.SignedIn() {
		if err := recording.SetLocalSonioxKey(key); err != nil {
			return "", fmt.Errorf("%s", locales.RecSttKeyNotSaved(err.Error()))
		}
		return locales.RecSttSavedOnDevice(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	if _, err := be.Invoke(ctx, "stt", map[string]any{"action": "save_key", "provider": "soniox", "api_key": key}); err != nil {
		return "", fmt.Errorf("%s", locales.RecSttKeyNotSaved(err.Error()))
	}
	return locales.RecSttSavedOnServer(), nil
}

// TestEndpoint tries an OpenAI-compatible endpoint with a second of
// silence. It returns words to show for it working.
func (c *Core) TestEndpoint(url, model, key string) (_ string, err error) {
	defer catch(&err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := recording.TestEndpoint(ctx, url, model, key); err != nil {
		return "", err
	}
	return locales.RecSttItWorks(), nil
}

// TestSoniox tries a Soniox key, or this device's saved one when key is
// "". It returns words to show for it working.
func (c *Core) TestSoniox(key string) (_ string, err error) {
	defer catch(&err)
	if key = strings.TrimSpace(key); key == "" {
		key = recording.LocalSonioxKey()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := recording.TestSonioxKey(ctx, key); err != nil {
		return "", err
	}
	return locales.RecSttItWorks(), nil
}

// TestMicrophone records four seconds into no page and transcribes them,
// for Settings to try the microphone and the engine. RecordingChanged
// says it is testing, and then what it heard.
func (c *Core) TestMicrophone() (err error) {
	defer catch(&err)
	c.q.do(func() {
		s := c.core.Store.Settings()
		c.micTesting, c.micHeard = true, ""
		err = c.core.Rec.Test(s.Stt, c.online(s), 4*time.Second, func(text string, err error) {
			c.micTesting, c.micHeard = false, locales.RecSttHeard(text)
			if err != nil {
				c.micHeard = err.Error()
			}
		})
		if err != nil {
			c.micTesting = false
		}
		c.tellRecording()
	})
	return err
}
