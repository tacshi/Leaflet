package remote

import (
	"context"
	"encoding/json"
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

// Storage transfers take as long as they need, past the timeout of the
// other calls, as a large recording's upload does; a server that does
// not answer once the request is sent still fails them.
func TestStorageTransfersOutlastTheCallTimeout(t *testing.T) {
	was := longHeaderWait
	longHeaderWait = 500 * time.Millisecond
	t.Cleanup(func() { longHeaderWait = was })
	var hang atomic.Bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang.Load() {
			_, _ = io.ReadAll(r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
		data, _ := io.ReadAll(r.Body)
		if r.Method == "GET" {
			_, _ = io.WriteString(w, "RIFF")
			return
		}
		_, _ = fmt.Fprintf(w, `{"Key": %q}`, data)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.http.Timeout = 100 * time.Millisecond
	ctx := context.Background()
	if err := c.Upload(ctx, "recordings", "u1/r.wav", strings.NewReader("RIFF"), "audio/wav"); err != nil {
		t.Errorf("upload: %v", err)
	}
	if data, err := c.Download(ctx, "recordings", "u1/r.wav"); err != nil || string(data) != "RIFF" {
		t.Errorf("download %q, %v", data, err)
	}
	hang.Store(true)
	start := time.Now()
	if err := c.Upload(ctx, "recordings", "u1/r.wav", strings.NewReader("RIFF"), "audio/wav"); err == nil || !IsNetwork(err) {
		t.Errorf("upload to a server that does not answer: %v", err)
	}
	if err := c.InvokeStream(ctx, "agent", map[string]any{}, func(map[string]any) {}); err == nil {
		t.Error("a run on a server that does not answer went")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up after %v", d)
	}
}

// A function that works a while before it answers, as transcribing does,
// is waited for past the other calls' timeout and the long client's wait
// for an answer, as long as ctx lets it.
func TestInvokeLongOutlastsTheCallTimeout(t *testing.T) {
	was := longHeaderWait
	longHeaderWait = 100 * time.Millisecond
	t.Cleanup(func() { longHeaderWait = was })
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{"segments": []}`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.http.Timeout = 100 * time.Millisecond
	if _, err := c.Invoke(context.Background(), "stt", map[string]any{}); err == nil {
		t.Error("Invoke outlasted its timeout")
	}
	res, err := c.InvokeLong(context.Background(), "stt", map[string]any{})
	if _, ok := res["segments"]; err != nil || !ok {
		t.Errorf("res %v, %v", res, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.InvokeLong(ctx, "stt", map[string]any{}); err == nil {
		t.Error("InvokeLong outlasted its ctx")
	}
}

// A session's expiry goes by how long the server says it lasts, from when
// it came by this device's clock, not by the server's clock.
func TestExpiryByThisDevicesClock(t *testing.T) {
	s, err := decodeSession(map[string]any{"access_token": "a", "expires_in": 3600.0, "expires_at": float64(time.Now().Add(-time.Hour).Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if left := s.ExpiresAt - time.Now().Unix(); left < 3590 || left > 3600 {
		t.Errorf("expires in %ds", left)
	}
	s, _ = decodeSession(map[string]any{"access_token": "a", "expires_at": 1234.0})
	if s.ExpiresAt != 1234 {
		t.Errorf("expires at %d, want the server's without expires_in", s.ExpiresAt)
	}
}

// A request the server refuses for an expired token renews the token and
// goes once more, with its body; one whose body cannot be read again, as
// an upload's file, fails instead.
func TestRefusedTokenRenewsAndRetries(t *testing.T) {
	var refreshes atomic.Int32
	var mu sync.Mutex
	var bodies []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/token" {
			refreshes.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":"new%d","refresh_token":"r2","expires_in":3600}`, refreshes.Load())
			return
		}
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"PGRST301","message":"JWT expired"}`)
			return
		}
		_, _ = io.WriteString(w, `[{"id":"1"}]`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.RefreshToken = "r1"
	rows, err := c.Insert(context.Background(), "pages", map[string]any{"id": "1"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v, %v", rows, err)
	}
	mu.Lock()
	if len(bodies) != 2 || bodies[0] != `{"id":"1"}` || bodies[1] != bodies[0] {
		t.Errorf("bodies %q", bodies)
	}
	mu.Unlock()
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d refreshes", n)
	}

	c.keep(&session{AccessToken: "test", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: "u1"}})
	err = c.Upload(context.Background(), "recordings", "u1/r.wav", io.MultiReader(strings.NewReader("RIFF")), "audio/wav")
	var re *Error
	if !errors.As(err, &re) || re.Status != http.StatusUnauthorized {
		t.Errorf("upload of a body read once: %v", err)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d refreshes", n)
	}
}

// A stream that stops sending, its connection gone half-open, ends rather
// than holding the run for the rest of its time.
func TestQuietStreamEnds(t *testing.T) {
	was := streamIdle
	streamIdle = 200 * time.Millisecond
	t.Cleanup(func() { streamIdle = was })
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"type":"status","status":"running"}`+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	events := 0
	start := time.Now()
	err := c.InvokeStream(context.Background(), "agent", map[string]any{}, func(map[string]any) { events++ })
	if !errors.Is(err, errStreamIdle) || events != 1 {
		t.Errorf("err %v, %d events", err, events)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("ended after %v", d)
	}
}

// A function that writes as the caller until it ends, an agent run or a
// transcription, gets a token that lasts as long as it may run: one with
// less left is renewed first, though it would do for other calls.
func TestLongCallsRenewATokenRunningOutFirst(t *testing.T) {
	var refreshes atomic.Int32
	var mu sync.Mutex
	var sent []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/token" {
			refreshes.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":"new%d","refresh_token":"r2","expires_in":3600}`, refreshes.Load())
			return
		}
		mu.Lock()
		sent = append(sent, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.RefreshToken = "r1"
	c.sess.ExpiresAt = time.Now().Add(3 * time.Minute).Unix()
	ctx := context.Background()
	if _, err := c.Invoke(ctx, "providers", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := c.InvokeStream(ctx, "agent", map[string]any{}, func(map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InvokeLong(ctx, "stt", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/functions/v1/providers Bearer test", "/functions/v1/agent Bearer new1", "/functions/v1/stt Bearer new1"}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Errorf("sent %q, want %q", sent, want)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d refreshes, want 1", n)
	}
}

// A function that refuses a token running out before it would finish has
// the call renew it and go again, as an expired one does.
func TestFunctionRefusingAShortTokenRetries(t *testing.T) {
	var refreshes, calls atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/token" {
			refreshes.Add(1)
			_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"r2","expires_in":3600}`)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"JWT expires before this would finish: renew it and try again"}`)
			return
		}
		_, _ = io.WriteString(w, `{"segments": []}`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.RefreshToken = "r1"
	res, err := c.InvokeLong(context.Background(), "stt", map[string]any{"action": "transcribe"})
	if _, ok := res["segments"]; err != nil || !ok {
		t.Fatalf("res %v, %v", res, err)
	}
	if refreshes.Load() != 1 || calls.Load() != 2 {
		t.Errorf("%d refreshes, %d calls", refreshes.Load(), calls.Load())
	}
}

// Only what says the server could not be reached counts as the network's:
// a failed connection, one that broke or went quiet, a gateway that could
// not reach the server. The server's refusals, answers that do not parse
// and the caller's own waits running out are not.
func TestIsNetwork(t *testing.T) {
	c := SignedInForTest("http://127.0.0.1:1", nil, "u1")
	_, refused := c.Select(context.Background(), "pages", Query{})
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{refused, true},
		{fmt.Errorf("reading: %w", io.ErrUnexpectedEOF), true},
		{errStreamIdle, true},
		{&Error{Status: http.StatusServiceUnavailable}, true},
		{&Error{Status: http.StatusConflict}, false},
		{errors.New("the page's content on this device could not be read"), false},
		{expired.Err(), false},
		{fmt.Errorf("fetch: %w", context.DeadlineExceeded), false},
		{ErrSignedOut, false},
		{nil, false},
	} {
		if got := IsNetwork(tc.err); got != tc.want {
			t.Errorf("IsNetwork(%v) = %v", tc.err, got)
		}
	}
}

// The owner is found on any page of the project's users.
func TestProjectOwnerReadsEveryPage(t *testing.T) {
	was, wasPages := usersPerPage, maxUserPages
	usersPerPage = 2
	t.Cleanup(func() { usersPerPage, maxUserPages = was, wasPages })
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var users []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			users = []map[string]any{{"email": "a@example.com"}, {"email": "b@example.com"}}
		case "2":
			users = []map[string]any{{"email": "c@example.com"}, {"email": "me@example.com", "app_metadata": map[string]any{"leaflet_owner": true}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	}))
	t.Cleanup(hs.Close)
	got, err := New(Settings{}, nil).ProjectOwner(context.Background(), Settings{URL: hs.URL, AnonKey: "k"}, "sb_secret_s")
	if err != nil || got != "me@example.com" {
		t.Errorf("owner %q, %v", got, err)
	}
	maxUserPages = 1
	if got, _ := New(Settings{}, nil).ProjectOwner(context.Background(), Settings{URL: hs.URL, AnonKey: "k"}, "sb_secret_s"); got != "" {
		t.Errorf("read past the last page it may: %q", got)
	}
}

// A profile change refused for an expired token renews the token and goes
// again, as the other requests do.
func TestUpdateUserRenewsARefusedToken(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/token" {
			_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"r2","expires_in":3600}`)
			return
		}
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, r.Method+" "+r.URL.Path+" "+string(data))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"msg":"invalid JWT: token is expired"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"u1"}`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.RefreshToken = "r1"
	if err := c.UpdateUser(map[string]any{"display_name": "Ana"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := `PUT /auth/v1/user {"data":{"display_name":"Ana"}}`; len(sent) != 2 || sent[1] != want {
		t.Errorf("sent %q", sent)
	}
}
