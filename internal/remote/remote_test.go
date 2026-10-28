package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"leaflet/internal/store"
)

// refreshServer answers refresh_token grants with status and body (%d
// numbering the answers), after signalling started and waiting for
// release when they are set.
type refreshServer struct {
	status   int
	body     string
	started  chan struct{}
	release  chan struct{}
	requests atomic.Int32
}

func (rs *refreshServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/auth/v1/token" || r.URL.Query().Get("grant_type") != "refresh_token" {
		http.NotFound(w, r)
		return
	}
	n := rs.requests.Add(1)
	if rs.started != nil {
		rs.started <- struct{}{}
	}
	if rs.release != nil {
		<-rs.release
	}
	w.WriteHeader(rs.status)
	_, _ = io.WriteString(w, strings.ReplaceAll(rs.body, "%d", fmt.Sprint(n)))
}

const newSession = `{"access_token":"new%d","refresh_token":"r2","expires_in":3600}`

// expiredClient is signed in with a session whose access token has run out.
func expiredClient(t *testing.T, rs *refreshServer) *Client {
	t.Helper()
	hs := httptest.NewServer(rs)
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.ExpiresAt = time.Now().Unix()
	c.sess.RefreshToken = "r1"
	return c
}

func TestTokenRefreshesOnceForAll(t *testing.T) {
	rs := &refreshServer{status: 200, body: newSession, release: make(chan struct{})}
	c := expiredClient(t, rs)
	var wg sync.WaitGroup
	toks := make([]string, 8)
	for i := range toks {
		wg.Go(func() {
			tok, err := c.token(context.Background())
			if err != nil {
				t.Error(err)
			}
			toks[i] = tok
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(rs.release)
	wg.Wait()
	if n := rs.requests.Load(); n != 1 {
		t.Errorf("%d refreshes, want 1", n)
	}
	for _, tok := range toks {
		if tok != "new1" {
			t.Errorf("token %q, want new1", tok)
		}
	}
	if c.User() == nil || c.UserID() != "u1" {
		t.Errorf("refresh lost the user: %+v", c.User())
	}
}

func TestSignOutDuringRefreshStays(t *testing.T) {
	rs := &refreshServer{status: 200, body: newSession, started: make(chan struct{}), release: make(chan struct{})}
	c := expiredClient(t, rs)
	errc := make(chan error)
	go func() {
		_, err := c.token(context.Background())
		errc <- err
	}()
	<-rs.started
	c.keep(nil)
	close(rs.release)
	if err := <-errc; !errors.Is(err, ErrSignedOut) {
		t.Errorf("token after sign-out: %v", err)
	}
	if c.SignedIn() {
		t.Error("the refresh signed back in")
	}
}

func TestSignInDuringRefreshStays(t *testing.T) {
	rs := &refreshServer{status: 200, body: newSession, started: make(chan struct{}), release: make(chan struct{})}
	c := expiredClient(t, rs)
	tokc := make(chan string)
	go func() {
		tok, _ := c.token(context.Background())
		tokc <- tok
	}()
	<-rs.started
	c.keep(&session{AccessToken: "other", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: "u2"}})
	close(rs.release)
	if tok := <-tokc; tok != "other" {
		t.Errorf("token %q, want the new account's", tok)
	}
	if id := c.UserID(); id != "u2" {
		t.Errorf("signed in as %q, want u2", id)
	}
}

func TestRefreshSignsOutOnlyWhenRevoked(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		signOut bool
	}{
		{400, `{"code":400,"error_code":"refresh_token_not_found","msg":"Invalid Refresh Token: Refresh Token Not Found"}`, true},
		{400, `{"error":"invalid_grant","error_description":"Invalid Refresh Token: Already Used"}`, true},
		{429, `{"msg":"Request rate limit reached"}`, false},
		{403, `{"msg":"Forbidden"}`, false},
		{503, `upstream down`, false},
	}
	for _, cs := range cases {
		c := expiredClient(t, &refreshServer{status: cs.status, body: cs.body})
		var told atomic.Bool
		c.OnAuth = func(u *store.User) {
			if u == nil {
				told.Store(true)
			}
		}
		_, err := c.token(context.Background())
		if signedOut := errors.Is(err, ErrSignedOut); signedOut != cs.signOut || c.SignedIn() == cs.signOut || told.Load() != cs.signOut {
			t.Errorf("%d: err %v, signed in %v, told %v; want signed out %v", cs.status, err, c.SignedIn(), told.Load(), cs.signOut)
		}
	}
}
