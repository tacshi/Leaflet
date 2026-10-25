package recording

import (
	"encoding/base64"
	"net/http"
	"sync"

	"github.com/egoist/mygo"
)

// Capture runs in a small page of the app's own scheme: such pages are
// secure contexts that the system's web view lets use the microphone, so
// the app needs no native audio code. The page resamples to 16 kHz PCM16
// and hands the chunks to the bound Recorder service.

// Scheme serves the capture page.
const Scheme = "leaflet-rec"

// CaptureURL is the capture page of a session.
func CaptureURL(session string) string { return Scheme + "://localhost/capture?s=" + session }

const capturePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>Recorder</title></head>
<body>
<script>
const session = new URLSearchParams(location.search).get("s");
const call = (name, ...args) => window.mygo.call("Recorder." + name, session, ...args);
let ctx, stream, node, source, paused = false, buf = [], pos = 0;
function send() {
  if (!buf.length) return;
  let n = 0; for (const b of buf) n += b.length;
  const out = new Int16Array(n); let o = 0;
  for (const b of buf) { out.set(b, o); o += b.length; }
  buf = [];
  const bytes = new Uint8Array(out.buffer);
  let s = ""; for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  call("Chunk", btoa(s));
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
      const input = e.inputBuffer.getChannelData(0);
      const n = Math.floor((input.length - pos) / ratio);
      const out = new Int16Array(Math.max(n, 0));
      for (let i = 0; i < out.length; i++) {
        // Average the input samples this output sample covers.
        const a = pos + i * ratio, b = Math.min(a + ratio, input.length);
        let sum = 0, cnt = 0;
        for (let j = Math.floor(a); j < b; j++) { sum += input[j]; cnt++; }
        const v = Math.max(-1, Math.min(1, cnt ? sum / cnt : 0));
        out[i] = v < 0 ? v * 0x8000 : v * 0x7fff;
      }
      pos = pos + out.length * ratio - input.length;
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
}

var service = &Recorder{sessions: map[string]*captureSink{}}

// Chunk takes base64 PCM16 audio of a session.
func (r *Recorder) Chunk(session, data string) error {
	pcm, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	s := r.sessions[session]
	r.mu.Unlock()
	if s != nil && s.chunk != nil {
		s.chunk(pcm)
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

// Register binds the capture service and serves its page. Call it before
// the app runs.
func Register() {
	mygo.Bind(service)
	_ = mygo.Protocol.HandleFunc(Scheme, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(capturePage))
	})
}
