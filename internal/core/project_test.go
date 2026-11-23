package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
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

// managementServer is the Supabase Management API of a project this build has set up, whose answers to a renewal of
// the sign-in's token and to the project's migrations can be held back.
type managementServer struct {
	holdToken, holdMigrations   chan struct{}
	tokenAsked, migrationsAsked chan struct{}
	tokenStatus                 int
	renewed                     string
}

func (m *managementServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ask := func(asked, hold chan struct{}) {
		if asked != nil {
			select {
			case asked <- struct{}{}:
			default:
			}
		}
		if hold != nil {
			<-hold
		}
	}
	switch p := r.URL.Path; {
	case p == "/v1/oauth/token":
		ask(m.tokenAsked, m.holdToken)
		if m.tokenStatus != 0 {
			w.WriteHeader(m.tokenStatus)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid refresh token"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"`+m.renewed+`","expires_in":3600}`)
	case strings.HasSuffix(p, "/database/migrations"):
		ask(m.migrationsAsked, m.holdMigrations)
		all, _ := provision.Migrations()
		applied := make([]supabaseapi.Migration, 0, len(all))
		for _, mg := range all {
			applied = append(applied, supabaseapi.Migration{Version: mg.Version, Name: mg.Name})
		}
		_ = json.NewEncoder(w).Encode(applied)
	case strings.HasSuffix(p, "/secrets"):
		_, _ = io.WriteString(w, `[{"name":"LEAFLET_FUNCTIONS_V99_0123456789ABCDEF","value":"x"}]`)
	case strings.HasSuffix(p, "/config/auth"):
		_, _ = io.WriteString(w, `{"disable_signup":true,"mailer_autoconfirm":false}`)
	case strings.HasSuffix(p, "/database/query"):
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func useManagementServer(t *testing.T, m *managementServer) {
	t.Helper()
	hs := httptest.NewServer(m)
	t.Cleanup(hs.Close)
	base, auth := supabaseapi.Base, supabaseapi.Auth
	supabaseapi.Base, supabaseapi.Auth = hs.URL, ""
	t.Cleanup(func() { supabaseapi.Base, supabaseapi.Auth = base, auth })
}

const projectRef = "abcdefghijklmnopqrst"

// projectCore is a core signed in to a project chosen with Continue with Supabase, rather than one the build is fixed
// to, which signing out forgets, with a keychain of its own.
func projectCore(t *testing.T) (*Core, memSecrets) {
	t.Helper()
	keys := memSecrets{}
	keychain.Use(keys)
	t.Cleanup(func() { keychain.Use(nil) })
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "null") }))
	t.Cleanup(hs.Close)
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	be := remote.New(remote.Settings{}, st)
	if err := be.Use(remote.Settings{URL: hs.URL, PublishableKey: "key", Ref: projectRef, Name: "Leaflet"}); err != nil {
		t.Fatal(err)
	}
	be.SignInForTest("u1")
	c := &Core{Store: st, Remote: be, Sync: syncsvc.New(st, be)}
	c.Sync.Offline = func() bool { return true }
	t.Cleanup(c.Sync.Stop)
	return c, keys
}

func signedOutMeanwhile(t *testing.T, c *Core, keys memSecrets, asked chan struct{}) {
	t.Helper()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the Management API was never asked")
	}
	if _, err := c.Leave(c.Store.ForgetAccount, false); err != nil {
		t.Fatal(err)
	}
	if _, kept := keys[managementKey]; kept || c.Remote.SignedIn() {
		t.Fatalf("signing out kept the Supabase sign-in %v, signed in %v", kept, c.Remote.SignedIn())
	}
}

// TestAProjectSignInUnderWayKeepsNothingAfterASignOut signs out while the sign-in in Settings sets the project up:
// once the setup ends, the Supabase sign-in is not kept after the sign-out forgot the project's.
func TestAProjectSignInUnderWayKeepsNothingAfterASignOut(t *testing.T) {
	c, keys := projectCore(t)
	m := &managementServer{holdMigrations: make(chan struct{}), migrationsAsked: make(chan struct{}, 1), renewed: "R2"}
	useManagementServer(t, m)
	done := make(chan error, 1)
	go func() { done <- c.RenewProject(context.Background(), supabaseapi.Resume("R1")) }()
	signedOutMeanwhile(t, c, keys, m.migrationsAsked)
	close(m.holdMigrations)
	if err := <-done; !errors.Is(err, remote.ErrSignedOut) {
		t.Errorf("the sign-in ended with %v, want signed out", err)
	}
	if v, kept := keys[managementKey]; kept || c.mgmt != nil {
		t.Errorf("the Supabase sign-in is kept after signing out: %q", v)
	}
}

// TestARenewalAfterASignOutKeepsNothing signs out while the project's update at the start renews its Supabase
// sign-in's token: the token renewed after the sign-out is not kept.
func TestARenewalAfterASignOutKeepsNothing(t *testing.T) {
	c, keys := projectCore(t)
	keys[managementKey] = []byte("R1")
	m := &managementServer{holdToken: make(chan struct{}), tokenAsked: make(chan struct{}, 1), renewed: "R2"}
	useManagementServer(t, m)
	done := make(chan bool, 1)
	go func() { done <- c.updateProject() }()
	signedOutMeanwhile(t, c, keys, m.tokenAsked)
	close(m.holdToken)
	<-done
	if v, kept := keys[managementKey]; kept {
		t.Errorf("the token renewed after signing out is kept: %q", v)
	}
}

// TestAnEndedSignInForgetsOnlyItself has the project's update find its Supabase sign-in ended as a newer sign-in takes
// its place: the newer one stays kept, and the project asks for no sign-in.
func TestAnEndedSignInForgetsOnlyItself(t *testing.T) {
	c, keys := projectCore(t)
	keys[managementKey] = []byte("R1")
	m := &managementServer{holdToken: make(chan struct{}), tokenAsked: make(chan struct{}, 1), tokenStatus: http.StatusBadRequest}
	useManagementServer(t, m)
	done := make(chan bool, 1)
	go func() { done <- c.updateProject() }()
	select {
	case <-m.tokenAsked:
	case <-time.After(5 * time.Second):
		t.Fatal("the token was never renewed")
	}
	newer := supabaseapi.Resume("N1")
	c.mgmtMu.Lock()
	c.adoptManagement(projectRef, newer)
	c.mgmtMu.Unlock()
	close(m.holdToken)
	<-done
	if v := keys[managementKey]; string(v) != "N1" || c.mgmt != newer {
		t.Errorf("the newer sign-in became %q", v)
	}
	if c.ProjectNeedsSignIn() {
		t.Error("the project asks for a sign-in right after one")
	}
}
