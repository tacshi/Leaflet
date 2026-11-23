package app

import (
	"bytes"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

func TestChunksGoInOrder(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	for _, seq := range []int{1, 0, 3, 2, 2, 0} {
		s.deliver(seq, []byte{byte(seq)})
	}
	if !bytes.Equal(got, []byte{0, 1, 2, 3}) {
		t.Fatalf("wrote %v", got)
	}
	got = nil
	for seq := 5; seq <= 5+maxHeld; seq++ {
		s.deliver(seq, []byte{byte(seq)})
	}
	if len(got) != maxHeld+1 || got[0] != 5 {
		t.Fatalf("wrote %v", got)
	}
}

func TestRefusedMicSaysWhereToAllowIt(t *testing.T) {
	const webKit = "The request is not allowed by the user agent or the platform in the current context, possibly because the user denied permission."
	for _, tc := range []struct{ name, goos, want string }{
		{"NotAllowedError", "darwin", locales.RecErrMicDeniedMac()},
		{"NotAllowedError", "windows", locales.RecErrMicDeniedWindows()},
		{"NotAllowedError", "linux", webKit},
		{"NotReadableError", "darwin", webKit},
		{"", "darwin", webKit},
	} {
		if got := micFailure(tc.name, webKit, tc.goos); got != tc.want {
			t.Errorf("%s on %s: %q, want %q", tc.name, tc.goos, got, tc.want)
		}
	}
}

func TestHeldChunksGoOnAsTheMicStops(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	for _, seq := range []int{0, 4, 2, 3} {
		s.deliver(seq, []byte{byte(seq)})
	}
	if !bytes.Equal(got, []byte{0}) {
		t.Fatalf("wrote %v before stopping", got)
	}
	s.flush()
	s.deliver(3, []byte{3})
	s.deliver(1, []byte{1})
	if !bytes.Equal(got, []byte{0, 2, 3, 4}) {
		t.Fatalf("wrote %v", got)
	}
}

func TestCaptureStartsOverAfterThePageIsLoadedAgain(t *testing.T) {
	var got []byte
	s := &captureSink{chunk: func(pcm []byte) { got = append(got, pcm...) }}
	// The page's process ends with the chunk after 1 under way.
	for _, seq := range []int{0, 1, 3} {
		s.deliver(seq, []byte{byte(seq)})
	}
	s.again()
	s.deliver(0, []byte{10})
	s.deliver(1, []byte{11})
	if !bytes.Equal(got, []byte{0, 1, 3, 10, 11}) {
		t.Fatalf("wrote %v", got)
	}
}

// pageCapture records as the capture page does, the test calling the
// Recorder as the page's script would.
type pageCapture struct{ session string }

func (c *pageCapture) Start(_ int, sink recording.Sink) (recording.Mic, error) {
	c.session = doc.NewBlockID()
	recorder.add(c.session, newCaptureSink(sink))
	return testMic{}, nil
}

func TestRecordingPausesWhileItsMicrophoneIsAway(t *testing.T) {
	a := templatesApp(t)
	c := &pageCapture{}
	a.rec.Attach(c, a.update)
	if err := a.rec.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recorder.remove(c.session) })
	recorder.Started(c.session, 48000)
	paused := func(why string) {
		t.Helper()
		if st := a.rec.State; !st.Paused || st.Error != locales.RecErrMicStalled(why) {
			t.Errorf("paused %v, error %q, want %q", st.Paused, st.Error, locales.RecErrMicStalled(why))
		}
	}
	recorder.Stalled(c.session, "muted")
	paused(locales.RecMicHeld())
	recorder.Recovered(c.session)
	if st := a.rec.State; st.Paused || st.Error != "" {
		t.Errorf("given back: paused %v, error %q", st.Paused, st.Error)
	}
	recorder.Stalled(c.session, "ended")
	paused(locales.RecMicDisconnected())
	a.rec.TogglePause()
	recorder.sink(c.session).lost()
	paused(locales.RecMicRecorderGone())
	a.rec.Stop()
	if !a.rec.Settle(5 * time.Second) {
		t.Error("the recording was never saved")
	}
}

func TestRecordingFailsWhenItsPageGoesBeforeItBegins(t *testing.T) {
	a := templatesApp(t)
	c := &pageCapture{}
	a.rec.Attach(c, a.update)
	if err := a.rec.Start("p1", store.SttSettings{}, false, func(*doc.Node) {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recorder.remove(c.session) })
	id := a.rec.State.ID
	recorder.sink(c.session).lost()
	if a.rec.Active() {
		t.Fatal("still starting with no page to record")
	}
	if rec, err := a.st.Recording(id); err != nil || rec.Status != "failed" || rec.Error != locales.RecErrMicDetail(locales.RecMicRecorderGone()) {
		t.Errorf("recording %+v, %v", rec, err)
	}
}
