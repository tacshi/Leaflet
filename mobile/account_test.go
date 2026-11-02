package mobile

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"leaflet/internal/keychain"
	"leaflet/internal/provision"
	"leaflet/internal/remote"
	"leaflet/internal/supabaseapi"
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
func TestSignOutOfAnAccount(t *testing.T) {
	c, _ := open(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/verify":
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,
				"user":{"id":"00000000-0000-0000-0000-0000000000a1","email":"rocky@example.com"}}`)
		case "/auth/v1/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := c.core.Remote.Use(remote.Settings{URL: srv.URL, AnonKey: "anon"}); err != nil {
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
	entries, err := c.core.Store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := c.core.Store.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
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
	if err := c.core.Remote.Use(remote.Settings{URL: srv.URL, AnonKey: "anon"}); err != nil {
		t.Fatal(err)
	}
	// The pages of another account, all gone up, which can't be removed.
	create(t, c, `{"title":"Uploaded"}`)
	st := c.core.Store
	entries, err := st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
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
