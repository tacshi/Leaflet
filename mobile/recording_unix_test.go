//go:build unix

package mobile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A transcription on the device stops with the app while iOS has it frozen
// in the background, and the clock goes on. The test freezes a copy of
// itself, as iOS would the app, while it waits on a transcription that
// needs less time to run than it is given, but not the time it is frozen
// for as well.
func TestGiveUpAfterCountsOnlyTheTimeTheAppRuns(t *testing.T) {
	if os.Getenv("LEAFLET_FROZEN_WAIT") != "" {
		waitWhileFrozen()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGiveUpAfterCountsOnlyTheTimeTheAppRuns$") // #nosec G204
	cmd.Env = append(os.Environ(), "LEAFLET_FROZEN_WAIT=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ended := false
	t.Cleanup(func() {
		if !ended {
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	r := bufio.NewReader(out)
	if line, err := r.ReadString('\n'); err != nil || strings.TrimSpace(line) != "waiting" {
		t.Fatalf("the copy says %q, %v", line, err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(r)
	err = cmd.Wait()
	ended = true
	if err != nil || !strings.Contains(string(rest), "answered") {
		t.Errorf("after the freeze: %v\n%s", err, rest)
	}
}

// waitWhileFrozen waits, as the app would for the device's speech
// recognition, on work that needs 400ms of the time the app runs, given
// two seconds of it.
func waitWhileFrozen() {
	onDeviceSlack = 2 * time.Second
	fmt.Println("waiting")
	_, err := giveUpAfter(0, func() (string, error) {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for range 20 {
			<-tick.C
		}
		return `{"done":true,"segments":[]}`, nil
	})
	if err != nil {
		fmt.Println("gave up:", err)
		return
	}
	fmt.Println("answered")
}
