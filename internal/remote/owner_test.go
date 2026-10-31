package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"leaflet/internal/store"
)

// The project chosen is kept on this device and comes back with the next
// client; another one forgets the session of the one before.
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
	p := Settings{URL: "https://abc.supabase.co/", AnonKey: "sb_publishable_x", Ref: "abc", Name: "Leaflet"}
	if err := c.Use(p); err != nil {
		t.Fatal(err)
	}
	c.keep(&session{AccessToken: "t", RefreshToken: "r", User: &authUser{ID: "u"}})
	if got := New(Settings{}, st).Project(); got.URL != "https://abc.supabase.co" || got.Ref != "abc" {
		t.Errorf("kept %+v", got)
	}
	if err := c.Use(Settings{URL: "https://def.supabase.co", AnonKey: "k", Ref: "def"}); err != nil {
		t.Fatal(err)
	}
	if c.SignedIn() {
		t.Error("signed in to the project before")
	}
	if err := c.Use(Settings{}); err != nil || New(Settings{}, st).Configured() {
		t.Errorf("forgot the project: %v", err)
	}
	fixed := New(Settings{URL: "http://127.0.0.1:54321", AnonKey: "k"}, st)
	if !fixed.Fixed() || fixed.Use(p) == nil {
		t.Error("a fixed server changed")
	}
}

// Signing in as the owner makes the account once, then signs in with a
// link made for it, with the secret key only on admin requests.
func TestSignInAsOwner(t *testing.T) {
	users := map[string]bool{}
	marked := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		admin := r.Header.Get("apikey") == "sb_secret_s" && r.Header.Get("Authorization") == ""
		switch {
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
			meta, _ := body["app_metadata"].(map[string]any)
			marked["me@example.com"] = admin && meta["leaflet_owner"] == true
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
	c := New(Settings{URL: srv.URL, AnonKey: "sb_publishable_p"}, nil)
	c.noKeychain = true
	if email, _, err := c.ProjectOwner(context.Background(), "sb_secret_s"); err != nil || email != "" {
		t.Fatalf("a new project has an owner: %q, %v", email, err)
	}
	for range 2 {
		u, err := c.SignInAsOwner(context.Background(), "sb_secret_s", "me@example.com", "Me")
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != "u1" || u.DisplayName != "Me" || !c.SignedIn() {
			t.Errorf("signed in as %+v", u)
		}
	}
	if email, name, err := c.ProjectOwner(context.Background(), "sb_secret_s"); err != nil || email != "me@example.com" || name != "Me" {
		t.Errorf("owner %q %q, %v", email, name, err)
	}

	// Another refusal is not an account that is there.
	if _, err := c.SignInAsOwner(context.Background(), "sb_secret_s", "not-an-email", "Me"); err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Errorf("an invalid email: %v", err)
	}

	// An account there before, unmarked, becomes the owner's.
	marked["me@example.com"] = false
	if _, err := c.SignInAsOwner(context.Background(), "sb_secret_s", "me@example.com", "Me"); err != nil || !marked["me@example.com"] {
		t.Errorf("not marked: %v", err)
	}
}
