package mobile

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
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
