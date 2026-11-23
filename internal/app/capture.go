package app

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/locales"
)

const recScheme = "leaflet-rec"

func captureURL(session string) string { return recScheme + "://localhost/capture?s=" + session }

const capturePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Recorder</title></head>
<body>
<script>
const session = new URLSearchParams(location.search).get("s");
const call = (name, ...args) => window.mygo.call("Recorder." + name, session, ...args);
const constraints = {audio: {channelCount: 1, echoCancellation: true, noiseSuppression: true}};
let ctx, stream, node, source, paused = false, stopping = false, buf = [], seq = 0;
// sending are the chunks' calls not answered yet.
const sending = new Set();
// tail is what the last buffer left over, which the next output sample
// starts in, and pos is where in it that sample starts.
let tail = new Float32Array(0), pos = 0;
function send() {
  if (!buf.length) return;
  let n = 0; for (const b of buf) n += b.length;
  const out = new Int16Array(n); let o = 0;
  for (const b of buf) { out.set(b, o); o += b.length; }
  buf = [];
  const bytes = new Uint8Array(out.buffer);
  let s = ""; for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  // Calls may run in any order: the number puts the chunks back in it.
  const sent = call("Chunk", seq++, btoa(s)).catch(() => {});
  sending.add(sent);
  sent.then(() => sending.delete(sent));
}
// A microphone that stops is told to the app, which pauses the recording
// rather than fill it with silence: one unplugged or gone with its
// Bluetooth headset ends, one the system or another app holds for a while
// is muted, and told again when it is given back.
function watch(s) {
  for (const t of s.getAudioTracks()) {
    t.onended = () => { if (!stopping) call("Stalled", "ended"); };
    t.onmute = () => { if (!stopping) call("Stalled", "muted"); };
    t.onunmute = () => { if (!stopping) call("Recovered"); };
  }
}
async function start() {
  try {
    stream = await navigator.mediaDevices.getUserMedia(constraints);
    ctx = new (window.AudioContext || window.webkitAudioContext)();
    if (ctx.state === "suspended") await ctx.resume();
    source = ctx.createMediaStreamSource(stream);
    // Small buffers sent often keep the meter close to the voice.
    node = ctx.createScriptProcessor(1024, 1, 1);
    const ratio = ctx.sampleRate / 16000;
    node.onaudioprocess = (e) => {
      if (paused) return;
      // The last buffer's tail goes first, so an output sample that
      // spans two buffers hears both, and no sample is lost.
      const fresh = e.inputBuffer.getChannelData(0);
      const input = new Float32Array(tail.length + fresh.length);
      input.set(tail);
      input.set(fresh, tail.length);
      const out = new Int16Array(Math.max(0, Math.floor((input.length - pos) / ratio)));
      for (let i = 0; i < out.length; i++) {
        // Average the input samples this output sample covers.
        const a = pos + i * ratio, b = Math.min(a + ratio, input.length);
        let sum = 0, cnt = 0;
        for (let j = Math.floor(a); j < b; j++) { sum += input[j]; cnt++; }
        const v = Math.max(-1, Math.min(1, cnt ? sum / cnt : 0));
        out[i] = v < 0 ? v * 0x8000 : v * 0x7fff;
      }
      const next = pos + out.length * ratio, keep = Math.floor(next);
      tail = input.slice(keep);
      pos = next - keep;
      buf.push(out);
    };
    source.connect(node);
    node.connect(ctx.destination);
    setInterval(send, 50);
    watch(stream);
    // The system can stop the page's sound too, and start it again.
    ctx.onstatechange = () => {
      if (stopping || ctx.state === "closed") return;
      if (ctx.state === "running") call("Recovered");
      else call("Stalled", "suspended");
    };
    call("Started", ctx.sampleRate);
  } catch (e) {
    call("Failed", String(e && e.name || ""), String(e && e.message || e));
  }
}
// again, as the recording resumes, asks once more for a microphone that
// ended, so that it goes on from the one there is now, and starts the
// sound again if the system stopped it.
async function again() {
  try {
    if (stream.getAudioTracks().every((t) => t.readyState === "ended")) {
      const s = await navigator.mediaDevices.getUserMedia(constraints);
      source.disconnect();
      stream = s;
      watch(stream);
      source = ctx.createMediaStreamSource(stream);
      source.connect(node);
    }
    if (ctx.state !== "running") await ctx.resume();
  } catch (e) {
    call("Stalled", "ended");
  }
}
window.pauseCapture = (p) => {
  paused = p;
  if (!p && ctx && !stopping) again();
};
// stopCapture sends what is left, and is done once every chunk's call
// has been answered: the app has them all then.
window.stopCapture = async () => {
  stopping = true;
  send();
  try { node && node.disconnect(); source && source.disconnect(); } catch (_) {}
  stream && stream.getTracks().forEach((t) => t.stop());
  ctx && ctx.close();
  await Promise.all([...sending]);
  return true;
};
start();
</script>
</body></html>`

type Recorder struct {
	mu       sync.Mutex
	sessions map[string]*captureSink
}

func newCaptureSink(sink recording.Sink) *captureSink {
	return &captureSink{chunk: sink.Chunk, started: sink.Started, failed: sink.Failed, stalled: sink.Stalled, recovered: sink.Recovered}
}

type captureSink struct {
	chunk     func([]byte)
	started   func()
	failed    func(string)
	stalled   func(string)
	recovered func()

	mu    sync.Mutex
	next  int
	held  map[int][]byte
	began bool
}

const maxHeld = 40

func (s *captureSink) deliver(seq int, pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < s.next || s.chunk == nil {
		return
	}
	if s.held == nil {
		s.held = map[int][]byte{}
	}
	s.held[seq] = pcm
	if len(s.held) > maxHeld {
		s.next = seq
		for k := range s.held {
			s.next = min(s.next, k)
		}
	}
	for {
		p, ok := s.held[s.next]
		if !ok {
			return
		}
		delete(s.held, s.next)
		s.next++
		s.chunk(p)
	}
}

// again readies the sink for a capture page loaded again, which counts its
// chunks from the first. What the page before it left held, waiting for a
// chunk that went with it, goes in first.
func (s *captureSink) again() {
	s.flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = 0
}

// lost tells the recording that its capture page is gone, with the process
// it ran in: one that had not begun fails, one under way stalls, and the
// page is loaded again as it resumes.
func (s *captureSink) lost() {
	s.mu.Lock()
	began := s.began
	s.mu.Unlock()
	switch {
	case !began && s.failed != nil:
		s.failed(locales.RecMicRecorderGone())
	case began && s.stalled != nil:
		s.stalled(locales.RecMicRecorderGone())
	}
}

func (s *captureSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chunk == nil {
		return
	}
	for _, seq := range slices.Sorted(maps.Keys(s.held)) {
		s.chunk(s.held[seq])
		s.next = seq + 1
	}
	s.held = nil
}

var recorder = &Recorder{sessions: map[string]*captureSink{}}

func (r *Recorder) Chunk(session string, seq int, data string) error {
	pcm, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s != nil {
		s.deliver(seq, pcm)
	}
	return nil
}

func (r *Recorder) Started(session string, sampleRate float64) {
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.began = true
	s.mu.Unlock()
	if s.started != nil {
		s.started()
	}
}

// Stalled tells that the microphone stopped giving sound: why is "ended"
// for one gone, "muted" for one held by the system or another app, and
// "suspended" for sound the system stopped.
func (r *Recorder) Stalled(session, why string) {
	s := r.sink(session)
	if s == nil || s.stalled == nil {
		return
	}
	msg := locales.RecMicHeld()
	if why == "ended" {
		msg = locales.RecMicDisconnected()
	}
	s.stalled(msg)
}

// Recovered tells that the microphone gives sound again.
func (r *Recorder) Recovered(session string) {
	if s := r.sink(session); s != nil && s.recovered != nil {
		s.recovered()
	}
}

func (r *Recorder) Failed(session, name, message string) {
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s != nil && s.failed != nil {
		s.failed(micFailure(name, message, runtime.GOOS))
	}
}

func micFailure(name, message, goos string) string {
	if name != "NotAllowedError" {
		return message
	}
	switch goos {
	case "darwin":
		return locales.RecErrMicDeniedMac()
	case "windows":
		return locales.RecErrMicDeniedWindows()
	}
	return message
}

func (r *Recorder) add(session string, s *captureSink) {
	r.mu.Lock()
	r.sessions[session] = s
	r.mu.Unlock()
}

func (r *Recorder) sink(session string) *captureSink {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[session]
}

func (r *Recorder) remove(session string) {
	r.mu.Lock()
	delete(r.sessions, session)
	r.mu.Unlock()
}

func registerCapture() {
	mygo.Bind(recorder)
	_ = mygo.Protocol.HandleFunc(recScheme, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(capturePage))
	})
}

type webCapture struct {
	win *mygo.Window

	mu   sync.Mutex
	view *mygo.WebView
}

func (w *webCapture) Start(rate int, sink recording.Sink) (recording.Mic, error) {
	session := doc.NewBlockID()
	s := newCaptureSink(sink)
	recorder.add(session, s)
	v, err := w.win.NewWebView(mygo.WebViewOptions{URL: captureURL(session),
		Page: mygo.PageOptions{BackgroundThrottling: mygo.ThrottlingOff}})
	if err != nil {
		recorder.remove(session)
		return nil, err
	}
	w.mu.Lock()
	w.view = v
	w.mu.Unlock()
	m := &webMic{capture: w, session: session, view: v}
	m.off = v.Page().OnRenderProcessGone(func(string) {
		m.gone.Store(true)
		if recorder.sink(session) == s {
			s.lost()
		}
	})
	return m, nil
}

func (w *webCapture) View() *mygo.WebView {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.view
}

type webMic struct {
	capture *webCapture
	session string
	view    *mygo.WebView
	off     func()
	// gone is set once the page's process crashed or was ended.
	gone atomic.Bool
}

func (m *webMic) Pause() { _, _ = m.view.Page().Eval("window.pauseCapture(true)") }

func (m *webMic) Resume() {
	if m.gone.Swap(false) {
		// The page went with its process: it is loaded again, and captures
		// from the start.
		if s := recorder.sink(m.session); s != nil {
			s.again()
		}
		m.view.Page().Reload()
		return
	}
	_, _ = m.view.Page().Eval("window.pauseCapture(false)")
}

func (m *webMic) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()
	_, _ = m.view.Page().EvalContext(ctx, "return window.stopCapture()")
	if s := recorder.sink(m.session); s != nil {
		s.flush()
	}
}

const stopWait = 5 * time.Second

func (m *webMic) Close() {
	m.off()
	recorder.remove(m.session)
	m.capture.mu.Lock()
	if m.capture.view == m.view {
		m.capture.view = nil
	}
	m.capture.mu.Unlock()
	m.view.Destroy()
}
