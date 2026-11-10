package core

import (
	"log"
	"os"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
)

func TestTheProjectIsUpdatedOnceAndOnlyWhenNew(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInToProjectForTest("http://127.0.0.1:1", "abcdefghijklmnopqrst", st, "u1")
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	c.Sync.Offline = func() bool { return true }
	t.Cleanup(c.Sync.Stop)
	if !c.projectOutOfDate() {
		t.Fatal("a project this build never set up is up to date")
	}
	c.projectUpdated("abcdefghijklmnopqrst")
	if c.projectOutOfDate() {
		t.Error("the project this build set up is out of date")
	}
	c.Start()
	if c.updatingProject.Load() {
		t.Error("the changes wait for an update with nothing to bring")
	}
	if err := st.DeleteValue(projectUpdatedKey("abcdefghijklmnopqrst")); err != nil {
		t.Fatal(err)
	}
	c.Start()
	if c.updatingProject.Load() {
		t.Error("starting again holds the changes back")
	}
	triesEnd(t, c)
}

func triesEnd(t *testing.T, c *Core) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); c.updatingRun.Load(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the tries never ended")
		}
	}
}

func TestAFailedProjectUpdateIsTriedAgainOnStart(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.SignedInToProjectForTest("http://127.0.0.1:1", "abcdefghijklmnopqrst", st, "u1")
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	c.Sync.Offline = func() bool { return true }
	t.Cleanup(c.Sync.Stop)
	var logged lockedLog
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	tries := func() int {
		t.Helper()
		triesEnd(t, c)
		return logged.count("update project")
	}
	c.Start()
	if n := tries(); n != 1 || c.updatingProject.Load() {
		t.Fatalf("%d tries, the changes wait %v", n, c.updatingProject.Load())
	}
	c.updatingRun.Store(true)
	c.Start()
	c.updatingRun.Store(false)
	if n := tries(); n != 1 {
		t.Errorf("%d tries beside the run under way", n)
	}
	c.Start()
	if c.updatingProject.Load() {
		t.Error("a later start holds the changes back")
	}
	if n := tries(); n != 2 {
		t.Errorf("%d tries, want one more", n)
	}
	c.projectUpdated("abcdefghijklmnopqrst")
	c.Start()
	if n := tries(); n != 2 {
		t.Errorf("%d tries of a project up to date", n)
	}
}

type lockedLog struct {
	mu  gosync.Mutex
	buf strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), s)
}

func TestAProjectWhoseSignInEndedAsksForItAgain(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	const ref = "abcdefghijklmnopqrst"
	be := remote.SignedInToProjectForTest("http://127.0.0.1:1", ref, st, "u1")
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	t.Cleanup(c.Sync.Stop)
	if c.ProjectNeedsSignIn() {
		t.Fatal("a project asks for a sign-in that never ended")
	}
	c.projectSignIn(ref, true)
	if !c.ProjectNeedsSignIn() {
		t.Error("the project whose sign-in ended asks for none")
	}
	again := &Core{Store: st, Remote: be}
	if !again.ProjectNeedsSignIn() {
		t.Error("the next run asks for none")
	}
	c.projectUpdated(ref)
	if c.ProjectNeedsSignIn() {
		t.Error("the project brought up to date still asks")
	}
}
