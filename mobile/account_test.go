package mobile

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/live"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/supabaseapi"
	syncsvc "leaflet/internal/sync"
)

func TestBeginSupabaseSignIn(t *testing.T) {
	raw, err := BeginSupabaseSignIn()
	if err != nil {
		t.Fatal(err)
	}
	var s supabaseSignIn
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.State == "" || s.Verifier == "" {
		t.Fatalf("state %q, verifier %q: want both", s.State, s.Verifier)
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != s.State {
		t.Errorf("the page's state is %q, want %q", q.Get("state"), s.State)
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge") == s.Verifier {
		t.Errorf("code_challenge %q: want the verifier's hash", q.Get("code_challenge"))
	}
	// The app's web session waits for this https callback.
	if r := q.Get("redirect_uri"); !strings.HasSuffix(r, "/auth/supabase") {
		t.Errorf("redirect_uri %q: want the Worker's /auth/supabase", r)
	}
}

// FinishSupabaseSignIn refuses an answer before it goes to the network.
func TestFinishSupabaseSignInRefuses(t *testing.T) {
	c, _ := open(t)
	raw, err := BeginSupabaseSignIn()
	if err != nil {
		t.Fatal(err)
	}
	var s supabaseSignIn
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	callback := "https://leaflet-auth.capyvibe.app/auth/supabase?"
	for name, tc := range map[string]struct{ url, want string }{
		"stale":     {callback + url.Values{"state": {"other"}, "code": {"c"}}.Encode(), "stale"},
		"cancelled": {callback + url.Values{"state": {s.State}, "error": {"access_denied"}}.Encode(), "cancelled"},
		"failed":    {callback + url.Values{"state": {s.State}, "error": {"server_error"}}.Encode(), "server_error"},
		"bad url":   {"https://leaflet-auth.capyvibe.app/%zz", "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.FinishSupabaseSignIn(tc.url, s.State, s.Verifier)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error saying %q", err, tc.want)
			}
		})
	}
	if acct := account0(t, c); acct.SignedIn {
		t.Error("signed in after a refused sign-in")
	}
}

// Signed out, there is no sign-in to confirm, and signing out leaves the
// device's own pages as they are.
func TestSignedOut(t *testing.T) {
	c, _ := open(t)
	if err := c.ConfirmSignIn(true); err != nil {
		t.Fatal(err)
	}
	before, err := c.Library()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := c.SignOut(true); err != nil || n != 0 {
		t.Fatalf("SignOut: %d, %v", n, err)
	}
	after, err := c.Library()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("signing out of no account changed the library:\n%s\n%s", before, after)
	}
	if acct := account0(t, c); acct.SignedIn || acct.Project != "" {
		t.Errorf("account %+v: want signed out, with no project", acct)
	}
}

func account0(t *testing.T, c *Core) account {
	t.Helper()
	raw, err := c.Account()
	if err != nil {
		t.Fatal(err)
	}
	var a account
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// A free project Supabase paused is woken, and signed in to once it says
// it is Leaflet's; a project up with nothing of Leaflet's is passed over.
func TestLeafletProjectWakesAPausedOne(t *testing.T) {
	restored := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case path == "/v1/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"a","expires_in":3600,"token_type":"Bearer"}`)
		case path == "/v1/organizations":
			_, _ = io.WriteString(w, `[{"id":"1","slug":"me","name":"Me"}]`)
		case path == "/v1/projects":
			_, _ = io.WriteString(w, `[
				{"ref":"empty","name":"Leaflet 2","status":"ACTIVE_HEALTHY","created_at":"2026-08-01","organization_slug":"me"},
				{"ref":"paused","name":"Leaflet","status":"INACTIVE","created_at":"2026-08-02","organization_slug":"me"}]`)
		case path == "/v1/projects/paused/restore":
			restored = true
		case path == "/v1/projects/paused":
			_, _ = io.WriteString(w, `{"ref":"paused","name":"Leaflet","status":"ACTIVE_HEALTHY","organization_slug":"me"}`)
		case path == "/v1/projects/paused/database/migrations":
			_ = json.NewEncoder(w).Encode([]supabaseapi.Migration{{Version: provision.FirstMigration, Name: "init"}})
		case strings.HasSuffix(path, "/database/migrations"):
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old, auth := supabaseapi.Base, supabaseapi.Auth
	supabaseapi.Base, supabaseapi.Auth = srv.URL, ""
	t.Cleanup(func() { supabaseapi.Base, supabaseapi.Auth = old, auth })

	p, err := leafletProject(t.Context(), supabaseapi.Resume("r"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Ref != "paused" || !p.Healthy() || !restored {
		t.Fatalf("project %+v, restored %v", p, restored)
	}
}

// A sign-in with Supabase that fails before it signs in leaves the
// Supabase sign-in kept for the project in use as it was.
func TestFailedSignInKeepsTheManagementSignIn(t *testing.T) {
	c, _ := open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth/token" {
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"new","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	old, auth := supabaseapi.Base, supabaseapi.Auth
	supabaseapi.Base, supabaseapi.Auth = srv.URL, ""
	t.Cleanup(func() { supabaseapi.Base, supabaseapi.Auth = old, auth })
	if err := keychain.Set("supabase.management", []byte("working")); err != nil {
		t.Fatal(err)
	}

	callback := "https://leaflet-auth.capyvibe.app/auth/supabase?" + url.Values{"state": {"s"}, "code": {"c"}}.Encode()
	if _, err := c.FinishSupabaseSignIn(callback, "s", "v"); err == nil {
		t.Fatal("signed in")
	}
	if kept, err := keychain.Get("supabase.management"); err != nil || string(kept) != "working" {
		t.Errorf("kept %q, %v", kept, err)
	}
}

// Signed in for real, to a server that takes nothing up: signing out
// counts the changes still to go up and leaves all as it is; once they
// have gone, it signs out and removes the account's pages, and the
// device's own pages, made signed out, stay through signing out again.
// signInRocky signs c in to a server that answers only sign-in and
// sign-out, as rocky@example.com.
func signInRocky(t *testing.T, c *Core) { signInRockyTo(t, c, nil) }

// signInRockyTo signs c in as signInRocky does, to a server that answers
// the rest with other, or not at all without it.
func signInRockyTo(t *testing.T, c *Core, other http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/verify":
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,
				"user":{"id":"00000000-0000-0000-0000-0000000000a1","email":"rocky@example.com"}}`)
		case "/auth/v1/logout":
			if other != nil {
				other(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			if other != nil {
				other(w, r)
				return
			}
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	if err := c.core.Remote.Use(remote.Settings{URL: srv.URL, PublishableKey: "anon"}); err != nil {
		t.Fatal(err)
	}
	user, err := c.core.Remote.VerifyCode("rocky@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	c.q.do(func() {
		var lost int
		if lost, err = c.signedIn(user); err == nil && lost != 0 {
			t.Errorf("signing in loses %d changes", lost)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if acct := account0(t, c); !acct.SignedIn || acct.Email != "rocky@example.com" {
		t.Fatalf("account %+v: want signed in", acct)
	}
}

// uploadAll takes every change waiting off the outbox, as a pass would.
func uploadAll(t *testing.T, c *Core) {
	t.Helper()
	entries, err := c.core.Store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := c.core.Store.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSignOutOfAnAccount(t *testing.T) {
	c, _ := open(t)
	signInRocky(t, c)
	plan := create(t, c, `{"title":"Plan"}`)

	// Not up yet: nothing goes.
	n, err := c.SignOut(false)
	if err != nil || n == 0 {
		t.Fatalf("SignOut: %d, %v; want the changes counted", n, err)
	}
	if !account0(t, c).SignedIn || !hasPage(t, c, plan.ID) {
		t.Fatal("signed out with changes not up")
	}

	// Gone up, as a pass would take them.
	uploadAll(t, c)
	if n, err := c.SignOut(false); err != nil || n != 0 {
		t.Fatalf("SignOut: %d, %v", n, err)
	}
	if acct := account0(t, c); acct.SignedIn || acct.Project != "" {
		t.Fatalf("account %+v: want signed out, with no project", acct)
	}
	if hasPage(t, c, plan.ID) {
		t.Fatal("the account's page stayed")
	}

	// The device's own page stays: there is no account to leave.
	own := create(t, c, `{"title":"Mine"}`)
	if n, err := c.SignOut(false); err != nil || n != 0 || !hasPage(t, c, own.ID) {
		t.Fatalf("SignOut signed out: %d, %v; the page stayed %v", n, err, hasPage(t, c, own.ID))
	}
}

// onlinePlatform is a device with a connection.
type onlinePlatform struct{ fakePlatform }

func (*onlinePlatform) Online() bool { return true }

// A file that takes longer to go up than a push waits is waited for: it
// is no change left behind, to ask about.
func TestSignOutWaitsForAnUpload(t *testing.T) {
	defer syncsvc.UploadWaitForTest(10 * time.Millisecond)()
	cfg, _ := json.Marshal(config{DataDir: t.TempDir(), Locale: "en"})
	p := &onlinePlatform{fakePlatform{t: t, secrets: map[string][]byte{}}}
	p.logUntilEnd()
	ev := &fakeEvents{updates: make(chan update, 64), saving: make(chan bool, 64), readOnly: make(chan bool, 16), notices: make(chan string, 16)}
	c, err := Open(string(cfg), p, ev)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Stop()
		_ = c.core.Store.Close()
	})
	signInRockyTo(t, c, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/") {
			_, _ = io.Copy(io.Discard, r.Body)
			time.Sleep(200 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "[]")
	})
	st := c.core.Store
	if err := st.SetValue("space.00000000-0000-0000-0000-0000000000a1", st.Session().SpaceID); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir() + "/film.mov"
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFile(source, "", "", "video/quicktime"); err != nil {
		t.Fatal(err)
	}
	if n, err := c.SignOut(false); err != nil || n != 0 {
		t.Fatalf("SignOut: %d, %v", n, err)
	}
	if account0(t, c).SignedIn {
		t.Error("still signed in")
	}
}

// A recording in progress is a change not up yet, though the outbox gets
// it only once it is saved: signing out keeps it, and its audio, until its
// upload is queued and gone.
func TestSignOutWaitsForARecording(t *testing.T) {
	c, _ := open(t)
	signInRocky(t, c)
	page := create(t, c, `{"title":"Lecture"}`)
	if _, err := c.OpenPage(page.ID); err != nil {
		t.Fatal(err)
	}
	mic := &fakeMic{closed: make(chan struct{}, 1)}
	c.UseMicrophone(mic)
	raw, err := c.StartRecording(page.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var block doc.Node
	_ = json.Unmarshal([]byte(raw), &block)
	id := block.Str("recording_id")
	uploadAll(t, c)

	if n, err := c.SignOut(false); err != nil || n == 0 {
		t.Fatalf("SignOut while recording: %d, %v; want it counted", n, err)
	}
	path, err := c.RecordingAudio(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil || !account0(t, c).SignedIn {
		t.Fatalf("signed out while recording: the audio %v", err)
	}

	c.StopRecording()
	<-mic.closed
	for deadline := time.Now().Add(5 * time.Second); c.core.Rec.Saving() > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the recording was never saved")
		}
	}
	uploadAll(t, c)
	if n, err := c.SignOut(false); err != nil || n != 0 {
		t.Fatalf("SignOut once saved: %d, %v", n, err)
	}
}

// Deleting the account closes the open pages as it removes them: a save
// waiting for the typing to stop, which comes due while the sign-out
// goes to the server, writes nothing of the account back to the device.
func TestDeleteAccountLeavesNoSaveOfAnOpenPage(t *testing.T) {
	c, _ := open(t)
	signInRockyTo(t, c, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/functions/v1/account":
			_, _ = io.WriteString(w, `{}`)
		case "/auth/v1/logout":
			// Past the page's save, which would come due meanwhile.
			time.Sleep(live.SaveDelay + 500*time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	})
	// A page whose content has not come to this device yet: its first
	// save is over no content.
	page := create(t, c, `{"title":"Plan"}`)
	if _, err := c.core.Store.DB().Exec(`DELETE FROM page_contents WHERE page_id = ?`, page.ID); err != nil {
		t.Fatal(err)
	}
	pg := openEditor(t, c, page.ID)
	edit := doc.FromBlocks([]*doc.Node{paragraph("a", "Typed just now")})
	if _, err := c.EditPage(page.ID, pg.Editor, edit.Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAccount(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(live.SaveDelay)
	if _, err := c.core.Store.ContentRow(page.ID); !store.IsNotFound(err) {
		t.Errorf("the deleted account's page has content again: %v", err)
	}
	if n := c.core.Store.PendingCount(); n != 0 {
		t.Errorf("%d changes wait to go up for the deleted account", n)
	}
}

func hasPage(t *testing.T, c *Core, id string) bool {
	t.Helper()
	raw, err := c.Library()
	if err != nil {
		t.Fatal(err)
	}
	var lib library
	_ = json.Unmarshal([]byte(raw), &lib)
	for _, p := range lib.Pages {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Signing in as another account that fails other than for changes not up
// yet signs that account out again, rather than leave it signed in over
// the pages of the one before.
func TestFailedUseAccountSignsOut(t *testing.T) {
	c, _ := open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/verify":
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,
				"user":{"id":"00000000-0000-0000-0000-0000000000a2","email":"sam@example.com"}}`)
		case "/auth/v1/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := c.core.Remote.Use(remote.Settings{URL: srv.URL, PublishableKey: "anon"}); err != nil {
		t.Fatal(err)
	}
	// The pages of another account, all gone up, which can't be removed.
	create(t, c, `{"title":"Uploaded"}`)
	st := c.core.Store
	uploadAll(t, c)
	if err := st.SetValue("account", "00000000-0000-0000-0000-0000000000a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER full BEFORE DELETE ON pages BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	user, err := c.core.Remote.VerifyCode("sam@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	c.q.do(func() { _, err = c.signedIn(user) })
	if err == nil {
		t.Fatal("signed in over pages that could not go")
	}
	for deadline := time.Now().Add(5 * time.Second); c.core.Remote.SignedIn(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the new account is still signed in")
		}
	}
}

// Signing in as another account, the pages of the one before all gone up,
// forgets its agents' chats with its pages, as signing out does: none
// shows to the next account, nor goes in its runs as history.
func TestAnotherAccountForgetsTheAgentsChats(t *testing.T) {
	c, _ := open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/verify":
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,
				"user":{"id":"00000000-0000-0000-0000-0000000000a2","email":"sam@example.com"}}`)
		case "/auth/v1/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := c.core.Remote.Use(remote.Settings{URL: srv.URL, PublishableKey: "anon"}); err != nil {
		t.Fatal(err)
	}
	create(t, c, `{"title":"Uploaded"}`)
	uploadAll(t, c)
	if err := c.core.Store.SetValue("account", "00000000-0000-0000-0000-0000000000a1"); err != nil {
		t.Fatal(err)
	}
	c.q.do(func() {
		c.core.Hub.Session(agents.WorkspaceID).Messages = []*agents.Message{{Mine: true, Text: "File my secret plans"}}
	})
	user, err := c.core.Remote.VerifyCode("sam@example.com", "123456")
	if err != nil {
		t.Fatal(err)
	}
	c.q.do(func() { _, err = c.signedIn(user) })
	if err != nil {
		t.Fatal(err)
	}
	var left []*agents.Message
	c.q.do(func() { left = c.core.Hub.Session(agents.WorkspaceID).Messages })
	if len(left) != 0 {
		t.Errorf("the next account sees %+v", left)
	}
}
