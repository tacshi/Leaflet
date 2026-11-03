package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
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

// Recovered says the microphone a stall took is back, as a call ended
// and gave it back: the recording the stall paused goes on.
func (a *AudioSink) Recovered() {
	defer catch(nil)
	if a.s.Recovered != nil {
		a.s.Recovered()
	}
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
	// and returns its answer, as recording.OnDeviceAnswer reads it: its
	// transcript, or why it failed, with a code for the core to say it in
	// the user's language.
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
	return &stream{t: e.t, id: id, language: language}, nil
}

func (e engine) File(path, language string) ([]store.Segment, error) {
	var lasts time.Duration
	if fi, err := os.Stat(path); err == nil {
		lasts = audioLasts(fi.Size() - 44)
	}
	raw, err := giveUpAfter(lasts, func() (string, error) { return e.t.File(path, language) })
	if err != nil {
		return nil, err
	}
	return recording.OnDeviceTranscript(raw, language)
}

// onDeviceSlack is how much longer than its audio lasts the device's
// engine gets to transcribe it, as the Mac's speech helper gets: one
// stuck, as a speech model that stops answering, is given up on, rather
// than leave the recording transcribing until the app quits. Tests
// shorten it.
var onDeviceSlack = time.Minute

// audioLasts is how long size bytes of the recorder's audio last.
func audioLasts(size int64) time.Duration {
	return time.Duration(max(size, 0)/2) * time.Second / recording.SampleRate
}

// giveUpAfter waits for the Transcriber's call fn, of audio that lasts d,
// for d and onDeviceSlack more at the most: the call then goes on in
// Swift, its answer unheard, and the transcription fails saying so.
func giveUpAfter(d time.Duration, fn func() (string, error)) (string, error) {
	type answer struct {
		raw string
		err error
	}
	done := make(chan answer, 1)
	guard.Go(func() {
		raw, err := fn()
		done <- answer{raw, err}
	})
	timer := time.NewTimer(d + onDeviceSlack)
	defer timer.Stop()
	select {
	case a := <-done:
		return a.raw, a.err
	case <-timer.C:
		return "", errStuck
	}
}

// errStuck is giveUpAfter's for a call it gave up on, in the language the
// app is in then.
var errStuck error = stuck{}

type stuck struct{}

func (stuck) Error() string { return locales.RecErrOnDeviceStuck() }

// stream is one recording's transcription by the app's Transcriber.
type stream struct {
	t        Transcriber
	id       string
	language string
	// sent counts the bytes of audio it was sent.
	sent atomic.Int64
}

func (s *stream) Send(pcm []byte) {
	s.sent.Add(int64(len(pcm)))
	s.t.Send(s.id, pcm)
}

// Finish waits for the words of the audio sent, about as long as the
// recording at worst, as the engine catches up: one stuck is ended.
func (s *stream) Finish() ([]store.Segment, error) {
	raw, err := giveUpAfter(audioLasts(s.sent.Load()), func() (string, error) { return s.t.Finish(s.id) })
	if err != nil {
		if errors.Is(err, errStuck) {
			s.t.Close(s.id)
		}
		return nil, err
	}
	return recording.OnDeviceTranscript(raw, s.language)
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
		if !recording.PreferOnDevice(&s.Stt) {
			return
		}
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

// tellRecording tells the app what changed of the recording; on the
// queue. Its levels, which change each 50 ms, go alone; the rest, with
// the words heard so far, which grow through a long meeting, only as it
// changes, but for the time recorded, which the app counts on.
func (c *Core) tellRecording() {
	st := c.recState()
	levels := st.Levels
	st.Levels = []float64{}
	elapsed := st.ElapsedMs
	st.ElapsedMs = 0
	same, _ := encode(st)
	if same != c.recSent {
		c.recSent = same
		st.ElapsedMs = elapsed
		raw, _ := encode(st)
		c.ev.recordingChanged(raw)
	}
	if !slices.Equal(levels, c.levelsSent) {
		c.levelsSent = slices.Clone(levels)
		raw, _ := encode(levels)
		c.ev.recordingLevels(raw)
	}
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
	// Label is the line under the block's title while nothing runs for
	// the recording here (recording.Status).
	Label string `json:"label"`
	// CreatedMs is when it was made, and PageTitle the title of the page
	// it was made in, for RecordingTitle.
	CreatedMs int64  `json:"created_ms"`
	PageTitle string `json:"page_title"`
}

// Recording returns a recording block's recording as recordingRow, its
// transcript tidied as the block shows it.
func (c *Core) Recording(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	var page store.Page
	c.q.do(func() {
		if rec, err = c.core.Store.Recording(id); err == nil {
			page, _ = c.core.Store.Page(rec.PageID)
		}
	})
	if err != nil {
		return "", err
	}
	row := recordingRow{
		ID: rec.ID, Status: rec.Status, DurationMs: rec.DurationMs, SttProvider: rec.SttProvider,
		Error: rec.Error, Transcript: recording.Tidy(rec.Transcript), Label: recording.Status(rec),
		PageTitle: page.Title,
	}
	if !rec.CreatedAt.IsZero() {
		row.CreatedMs = rec.CreatedAt.UnixMilli()
	}
	if row.Transcript == nil {
		row.Transcript = []store.Segment{}
	}
	row.Plain = recording.TranscriptText(rec)
	return encode(row)
}

// RecordingTitle returns the name a recording block of title shows, as
// the desktop's does (recording.BlockTitle): for the recording made at
// createdMs, as Recording gives it, in the page titled pageTitle, or 0
// for one not on this device.
func RecordingTitle(title string, createdMs int64, pageTitle string) string {
	defer catch(nil)
	return recording.BlockTitle(title, time.UnixMilli(createdMs), createdMs != 0, pageTitle)
}

// RecordingAudio returns the path of a recording's WAV audio on this
// device, downloading it first from where it went up when it is not. The
// app may give up on it by token, as LocalFile says.
func (c *Core) RecordingAudio(id string, token int64) (_ string, err error) {
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
	ctx, done := c.cancellable(token, 5*time.Minute)
	defer done()
	data, err := c.core.Rec.Audio(ctx, rec)
	if err != nil {
		return "", err
	}
	// Off the core's queue too, as LocalFile's file.
	return c.core.Store.DownloadedAudio(id, data)
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

// transcript is a recording's transcript as the blocks its page's editor
// puts after the recording's block, as TranscriptBlocks returns it.
type transcript struct {
	Blocks []*doc.Node `json:"blocks"`
}

// TranscriptBlocks returns recording id's transcript as transcript: a
// paragraph a line, each after its time in bold, for its page's editor to
// put after its block, as the desktop's does, as one step to undo.
func (c *Core) TranscriptBlocks(id string) (_ string, err error) {
	defer catch(&err)
	var rec store.Recording
	c.q.do(func() { rec, err = c.core.Store.Recording(id) })
	if err != nil {
		return "", err
	}
	return encode(transcript{Blocks: recording.TranscriptBlocks(rec)})
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
	out.EndpointKey = recording.EndpointKey()
	out.SonioxSaved = recording.LocalSonioxKey() != ""
	out.LocalEndpoint = recording.IsLocalEndpoint(out.Stt.BaseURL)
	out.Status = recording.EngineStatus(out.Stt, out.SonioxSaved || out.SignedIn, out.OnDevice)
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
		c.core.Rec.MirrorSettings(next)
	}
	return err
}

// SetEndpointKey keeps the key of an endpoint on this device's network
// in its keychain; "" removes it.
func (c *Core) SetEndpointKey(key string) (err error) {
	defer catch(&err)
	return recording.SetEndpointKey(key)
}

// SaveSonioxKey keeps a Soniox key on the server, signed in, or else in
// this device's keychain. It returns where it went, in words for the user.
func (c *Core) SaveSonioxKey(key string) (_ string, err error) {
	defer catch(&err)
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	onServer, err := c.core.Rec.SaveSonioxKey(key)
	switch {
	case err != nil:
		return "", err
	case onServer:
		return locales.RecSttSavedOnServer(), nil
	}
	return locales.RecSttSavedOnDevice(), nil
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
		err = c.core.Rec.TestMicrophone(s.Stt, c.online(s), func(heard string) {
			c.micTesting, c.micHeard = false, heard
		})
		if err != nil {
			c.micTesting = false
		}
		c.tellRecording()
	})
	return err
}
