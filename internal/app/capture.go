package app

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"sync"
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
let ctx, stream, node, source, paused = false, buf = [], seq = 0;
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
    call("Failed", String(e && e.name || ""), String(e && e.message || e));
  }
}
window.pauseCapture = (p) => { paused = p; };
// stopCapture sends what is left, and is done once every chunk's call
// has been answered: the app has them all then.
window.stopCapture = async () => {
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

type captureSink struct {
	chunk   func([]byte)
	started func()
	failed  func(string)

	mu   sync.Mutex
	next int
	held map[int][]byte
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
	if s != nil && s.started != nil {
		s.started()
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
	recorder.add(session, &captureSink{chunk: sink.Chunk, started: sink.Started, failed: sink.Failed})
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

func (w *webCapture) View() *mygo.WebView {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.view
}

type webMic struct {
	capture *webCapture
	session string
	view    *mygo.WebView
}

func (m *webMic) Pause()  { _, _ = m.view.Page().Eval("window.pauseCapture(true)") }
func (m *webMic) Resume() { _, _ = m.view.Page().Eval("window.pauseCapture(false)") }

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
	recorder.remove(m.session)
	m.capture.mu.Lock()
	if m.capture.view == m.view {
		m.capture.view = nil
	}
	m.capture.mu.Unlock()
	m.view.Destroy()
}
