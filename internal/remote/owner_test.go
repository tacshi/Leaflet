package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"leaflet/internal/store"
)

func TestUseKeepsTheProject(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	c := New(Settings{}, st)
	c.noKeychain = true
	if c.Configured() || c.Fixed() {
		t.Fatal("a new device has a project")
	}
	p := Settings{URL: "https://abc.supabase.co/", PublishableKey: "sb_publishable_x", Ref: "abc", Name: "Leaflet"}
	if err := c.Use(p); err != nil {
		t.Fatal(err)
	}
	c.keep(&session{AccessToken: "t", RefreshToken: "r", User: &authUser{ID: "u"}})
	if got := New(Settings{}, st).Project(); got.URL != "https://abc.supabase.co" || got.Ref != "abc" {
		t.Errorf("kept %+v", got)
	}
	if err := c.Use(Settings{URL: "https://def.supabase.co", PublishableKey: "k", Ref: "def"}); err != nil {
		t.Fatal(err)
	}
	if c.SignedIn() {
		t.Error("signed in to the project before")
	}
	if err := c.Use(Settings{}); err != nil || New(Settings{}, st).Configured() {
		t.Errorf("forgot the project: %v", err)
	}
	fixed := New(Settings{URL: "http://127.0.0.1:54321", PublishableKey: "k"}, st)
	if !fixed.Fixed() || fixed.Use(p) == nil {
		t.Error("a fixed server changed")
	}
}

func TestSignInAsOwner(t *testing.T) {
	users := map[string]bool{}
	marked := map[string]bool{}
	passwords, others := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		admin := r.Header.Get("apikey") == "sb_secret_s" && r.Header.Get("Authorization") == ""
		switch {
		case r.URL.Path == "/auth/v1/admin/users/u1" && r.Method == "GET":
			if !admin {
				http.Error(w, `{"msg":"forbidden"}`, http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "app_metadata": map[string]any{"leaflet_owner": marked["me@example.com"]}})
		case r.URL.Path == "/auth/v1/logout" && r.URL.Query().Get("scope") == "others":
			if r.Header.Get("Authorization") == "Bearer a" {
				others++
			}
		case r.URL.Path == "/auth/v1/admin/users" && r.Method == "GET":
			if !admin {
				http.Error(w, `{"msg":"forbidden"}`, http.StatusForbidden)
				return
			}
			var list []map[string]any
			for email := range users {
				list = append(list, map[string]any{"id": "u1", "email": email, "app_metadata": map[string]any{"leaflet_owner": marked[email]},
					"user_metadata": map[string]any{"display_name": "Me"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"users": list})
		case r.URL.Path == "/auth/v1/admin/users/u1" && r.Method == "PUT":
			if p, _ := body["password"].(string); admin && len(p) >= 32 {
				passwords++
			}
			if meta, ok := body["app_metadata"].(map[string]any); ok {
				marked["me@example.com"] = admin && meta["leaflet_owner"] == true
			}
		case r.URL.Path == "/auth/v1/admin/users":
			email, _ := body["email"].(string)
			if !strings.Contains(email, "@") {
				http.Error(w, `{"code":422,"error_code":"email_address_invalid","msg":"Unable to validate email address: invalid format"}`, http.StatusUnprocessableEntity)
				return
			}
			if !admin || body["email_confirm"] != true {
				http.Error(w, `{"msg":"forbidden"}`, http.StatusForbidden)
				return
			}
			if users[email] {
				http.Error(w, `{"code":422,"error_code":"email_exists","msg":"A user with this email address has already been registered"}`, http.StatusUnprocessableEntity)
				return
			}
			users[email] = true
			meta, _ := body["app_metadata"].(map[string]any)
			marked[email] = meta["leaflet_owner"] == true
			_, _ = io.WriteString(w, `{"id":"u1"}`)
		case r.URL.Path == "/auth/v1/admin/generate_link":
			if !admin || !users[body["email"].(string)] {
				http.Error(w, `{"msg":"no user"}`, http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"id":"u1","hashed_token":"h1","verification_type":"magiclink"}`)
		case r.URL.Path == "/auth/v1/verify":
			if r.Header.Get("apikey") != "sb_publishable_p" || body["token_hash"] != "h1" || body["type"] != "magiclink" {
				http.Error(w, `{"msg":"bad link"}`, http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,
				"user":{"id":"u1","email":"me@example.com","user_metadata":{"display_name":"Me"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	project := Settings{URL: srv.URL, PublishableKey: "sb_publishable_p", Ref: "p"}
	c := New(Settings{}, nil)
	c.noKeychain = true
	ctx := context.Background()
	if email, err := c.ProjectOwner(ctx, project, "sb_secret_s"); err != nil || email != "" {
		t.Fatalf("a new project has an owner: %q, %v", email, err)
	}
	for range 2 {
		before := c.sess
		p, err := c.SignInAsOwner(ctx, project, "sb_secret_s", "me@example.com", "Me")
		if err != nil {
			t.Fatal(err)
		}
		if c.sess != before {
			t.Error("the session changed before adopting")
		}
		u, err := c.Adopt(p)
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != "u1" || u.DisplayName != "Me" || !c.SignedIn() || c.Project().Ref != "p" {
			t.Errorf("signed in as %+v to %+v", u, c.Project())
		}
	}
	if email, err := c.ProjectOwner(ctx, project, "sb_secret_s"); err != nil || email != "me@example.com" {
		t.Errorf("owner %q, %v", email, err)
	}

	if _, err := c.SignInAsOwner(ctx, project, "sb_secret_s", "not-an-email", "Me"); err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Errorf("an invalid email: %v", err)
	}

	if passwords != 0 || others != 0 {
		t.Errorf("the owner's account had %d passwords set and %d sign-outs of other sessions", passwords, others)
	}

	marked["me@example.com"] = false
	if _, err := c.SignInAsOwner(ctx, project, "sb_secret_s", "me@example.com", "Me"); err != nil || !marked["me@example.com"] {
		t.Errorf("not marked: %v", err)
	}
	if passwords != 1 || others != 1 {
		t.Errorf("an account there before had %d passwords set and %d sign-outs of other sessions, want 1 and 1", passwords, others)
	}
}

func TestSignInAsOwnerEndsOthersBeforeMarking(t *testing.T) {
	var marked bool
	var loggedOut []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/auth/v1/admin/users" && r.Method == "POST":
			http.Error(w, `{"code":422,"error_code":"email_exists","msg":"exists"}`, http.StatusUnprocessableEntity)
		case r.URL.Path == "/auth/v1/admin/generate_link":
			_, _ = io.WriteString(w, `{"id":"u1","hashed_token":"h1","verification_type":"magiclink"}`)
		case r.URL.Path == "/auth/v1/admin/users/u1" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"id":"u1","app_metadata":{}}`)
		case r.URL.Path == "/auth/v1/admin/users/u1" && r.Method == "PUT":
			if _, ok := body["app_metadata"]; ok {
				marked = true
			}
		case r.URL.Path == "/auth/v1/verify":
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,"user":{"id":"u1","email":"me@example.com"}}`)
		case r.URL.Path == "/auth/v1/logout":
			scope := r.URL.Query().Get("scope")
			loggedOut = append(loggedOut, scope)
			if scope == "others" {
				http.Error(w, `{"msg":"unavailable"}`, http.StatusServiceUnavailable)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(Settings{}, nil)
	c.noKeychain = true
	project := Settings{URL: srv.URL, PublishableKey: "k", Ref: "p"}
	if _, err := c.SignInAsOwner(context.Background(), project, "sb_secret_s", "me@example.com", "Me"); err == nil {
		t.Fatal("signed in although the other sessions went on")
	}
	if marked {
		t.Error("marked as the owner's with its other sessions going on")
	}
	if strings.Join(loggedOut, " ") != "others local" {
		t.Errorf("logouts %v, want [others local]", loggedOut)
	}
}

func TestDiscardLeavesTheSession(t *testing.T) {
	var loggedOut []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/logout" {
			loggedOut = append(loggedOut, r.Header.Get("Authorization")+" "+r.URL.Query().Get("scope"))
		}
	}))
	defer srv.Close()
	project := Settings{URL: srv.URL, PublishableKey: "k"}
	c := New(Settings{}, nil)
	c.noKeychain = true
	if _, err := c.Adopt(&Pending{project: project, s: &session{AccessToken: "new", RefreshToken: "r", User: &authUser{ID: "u"}}}); err != nil {
		t.Fatal(err)
	}
	c.Discard(&Pending{project: project, s: &session{AccessToken: "old", User: &authUser{ID: "u"}}})
	if !c.SignedIn() || c.sess.AccessToken != "new" {
		t.Error("the session in use went")
	}
	if len(loggedOut) != 1 || loggedOut[0] != "Bearer old local" {
		t.Errorf("signed out %v", loggedOut)
	}
}

func TestSignOutEndsOnlyThisSession(t *testing.T) {
	var scopes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/logout" {
			scopes = append(scopes, r.URL.Query().Get("scope"))
		}
	}))
	defer srv.Close()
	c := New(Settings{URL: srv.URL, PublishableKey: "k"}, nil)
	c.noKeychain = true
	c.keep(&session{AccessToken: "t", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: "u"}})
	c.SignOut()
	if c.SignedIn() {
		t.Error("still signed in")
	}
	if len(scopes) != 1 || scopes[0] != "local" {
		t.Errorf("logout scopes %q, want [local]", scopes)
	}
}
