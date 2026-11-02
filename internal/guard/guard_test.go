package guard

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"leaflet/locales"
)

// A panic on a goroutine Go started is logged, with where it happened,
// and the test goes on.
func TestGoRecovers(t *testing.T) {
	logged := make(chan string, 1)
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logged <- string(p)
		return len(p), nil
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	Go(func() { panic("boom") })
	select {
	case s := <-logged:
		if !strings.Contains(s, "panic: boom") || !strings.Contains(s, "guard_test.go") {
			t.Errorf("logged %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing logged")
	}
}

type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// A panic RecoverAs catches is the goroutine's error, worded as the app's
// own errors are.
func TestRecoverAsSaysWhatWentWrong(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	err := func() (err error) {
		defer RecoverAs(&err)
		panic("boom")
	}()
	if err == nil || err.Error() != locales.AppErrorInternal("boom") {
		t.Errorf("error %v", err)
	}
}
