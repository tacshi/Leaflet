package app

import (
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
)

// Recordings hear through a small page of the app's own scheme: such pages
// are secure contexts that the system's web view lets use the microphone,
// so the app needs no native audio code. The page resamples to 16 kHz
// PCM16 and hands the chunks to the bound Recorder service.

// recScheme serves the capture page.
const recScheme = "leaflet-rec"

// captureURL is the capture page of a session.
func captureURL(session string) string { return recScheme + "://localhost/capture?s=" + session }

const capturePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Recorder</title></head>
<body>
<script>
const session = new URLSearchParams(location.search).get("s");
const call = (name, ...args) => window.mygo.call("Recorder." + name, session, ...args);
let ctx, stream, node, source, paused = false, buf = [], seq = 0;
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
  call("Chunk", seq++, btoa(s));
}
async function start() {
  try {
    stream = await navigator.mediaDevices.getUserMedia({audio: {channelCount: 1, echoCancellation: true, noiseSuppression: true}});
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
    call("Started", ctx.sampleRate);
  } catch (e) {
    call("Failed", String(e && e.message || e));
  }
}
window.pauseCapture = (p) => { paused = p; };
window.stopCapture = () => {
  send();
  try { node && node.disconnect(); source && source.disconnect(); } catch (_) {}
  stream && stream.getTracks().forEach((t) => t.stop());
  ctx && ctx.close();
  return true;
};
start();
</script>
</body></html>`

// Recorder is the service the capture page calls.
type Recorder struct {
	mu       sync.Mutex
	sessions map[string]*captureSink
}

type captureSink struct {
	chunk   func([]byte)
	started func()
	failed  func(string)

	// The page's calls run each on a goroutine of its own, so chunks can
	// come in out of order: those ahead of the next one wait in held.
	mu   sync.Mutex
	next int
	held map[int][]byte
}

// maxHeld is the most chunks that wait for an earlier one, 2 s of audio:
// past it, one that never came is given up on.
const maxHeld = 40

// deliver hands on chunk seq, and those after it it held, in order and
// one at a time.
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

var recorder = &Recorder{sessions: map[string]*captureSink{}}

// Chunk takes base64 PCM16 audio of a session, the seq-th chunk of it.
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

// Started tells the session the microphone is on.
func (r *Recorder) Started(session string, sampleRate float64) {
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s != nil && s.started != nil {
		s.started()
	}
}

// Failed tells the session the microphone could not start.
func (r *Recorder) Failed(session, message string) {
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s != nil && s.failed != nil {
		s.failed(message)
	}
}

func (r *Recorder) add(session string, s *captureSink) {
	r.mu.Lock()
	r.sessions[session] = s
	r.mu.Unlock()
}

func (r *Recorder) remove(session string) {
	r.mu.Lock()
	delete(r.sessions, session)
	r.mu.Unlock()
}

// registerCapture binds the capture service and serves its page. Call it
// before the app runs.
func registerCapture() {
	mygo.Bind(recorder)
	_ = mygo.Protocol.HandleFunc(recScheme, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(capturePage))
	})
}

// webCapture is the recordings' microphone on the desktop: the capture
// page, in a web view of the window.
type webCapture struct {
	win *mygo.Window

	mu sync.Mutex
	// view is the capture page started last, which the window must show
	// (it can be tiny) while it records.
	view *mygo.WebView
}

// Start opens a capture page for a recording. It captures at 16 kHz,
// whatever rate asks.
func (w *webCapture) Start(rate int, sink recording.Sink) (recording.Mic, error) {
	session := doc.NewBlockID()
	recorder.add(session, &captureSink{chunk: sink.Chunk, started: sink.Started, failed: sink.Failed})
	// The page captures while the window is hidden or minimized too, as
	// a recording started from the menu bar is: web views slow down then,
	// unless told not to.
	v, err := w.win.NewWebView(mygo.WebViewOptions{URL: captureURL(session),
		Page: mygo.PageOptions{BackgroundThrottling: mygo.ThrottlingOff}})
	if err != nil {
		recorder.remove(session)
		return nil, err
	}
	w.mu.Lock()
	w.view = v
	w.mu.Unlock()
	return &webMic{capture: w, session: session, view: v}, nil
}

// View is the capture page started last, until it closes.
func (w *webCapture) View() *mygo.WebView {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.view
}

// webMic is the capture page of one recording.
type webMic struct {
	capture *webCapture
	session string
	view    *mygo.WebView
}

func (m *webMic) Pause()  { _, _ = m.view.Page().Eval("window.pauseCapture(true)") }
func (m *webMic) Resume() { _, _ = m.view.Page().Eval("window.pauseCapture(false)") }

// Stop has the page send the audio it holds, and waits for it to arrive.
func (m *webMic) Stop() {
	_, _ = m.view.Page().Eval("return window.stopCapture()")
	time.Sleep(200 * time.Millisecond)
}

// Close forgets the page's session and closes it.
func (m *webMic) Close() {
	recorder.remove(m.session)
	m.capture.mu.Lock()
	if m.capture.view == m.view {
		m.capture.view = nil
	}
	m.capture.mu.Unlock()
	m.view.Destroy()
}
