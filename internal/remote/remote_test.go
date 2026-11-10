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

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/store"
)

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
			tok, _, err := c.token(context.Background())
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

func TestRefreshThatPanicsFails(t *testing.T) {
	c := expiredClient(t, &refreshServer{status: 400, body: `{"error":"invalid_grant"}`})
	c.st = signedInStore(t)
	c.st.Subscribe(func(store.Change) { panic("boom") })
	if _, _, err := c.token(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("token after a panic: %v", err)
	}
}

func TestSignOutDuringRefreshStays(t *testing.T) {
	rs := &refreshServer{status: 200, body: newSession, started: make(chan struct{}), release: make(chan struct{})}
	c := expiredClient(t, rs)
	errc := make(chan error)
	go func() {
		_, _, err := c.token(context.Background())
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
		tok, _, _ := c.token(context.Background())
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
		c.st = signedInStore(t)
		_, _, err := c.token(context.Background())
		told := !c.st.Session().SignedIn
		if signedOut := errors.Is(err, ErrSignedOut); signedOut != cs.signOut || c.SignedIn() == cs.signOut || told != cs.signOut {
			t.Errorf("%d: err %v, signed in %v, store signed out %v; want signed out %v", cs.status, err, c.SignedIn(), told, cs.signOut)
		}
	}
}

func signedInStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	return st
}

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
	if data, err := readAll(c.Download(ctx, "recordings", "u1/r.wav")); err != nil || string(data) != "RIFF" {
		t.Errorf("download %q, %v", data, err)
	}
	hang.Store(true)
	start := time.Now()
	if err := c.Upload(ctx, "recordings", "u1/r.wav", strings.NewReader("RIFF"), "audio/wav"); err == nil || !IsNetwork(err) {
		t.Errorf("upload to a server that does not answer: %v", err)
	}
	if _, err := c.Download(ctx, "recordings", "u1/r.wav"); err == nil || !IsNetwork(err) {
		t.Errorf("download from a server that does not answer: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up after %v", d)
	}
}

func TestInvokeEndsAtTheCallTimeout(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.http.Timeout = 100 * time.Millisecond
	if _, err := c.Invoke(context.Background(), "account", map[string]any{}); err == nil || !IsNetwork(err) {
		t.Errorf("Invoke outlasted its timeout: %v", err)
	}
}

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

func TestCallsRenewATokenRunningOutFirst(t *testing.T) {
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
	if _, err := c.Invoke(ctx, "account", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.sess.ExpiresAt = time.Now().Add(30 * time.Second).Unix()
	c.mu.Unlock()
	if _, err := c.Invoke(ctx, "account", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Invoke(ctx, "account", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/functions/v1/account Bearer test", "/functions/v1/account Bearer new1", "/functions/v1/account Bearer new1"}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Errorf("sent %q, want %q", sent, want)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d refreshes, want 1", n)
	}
}

func TestFunctionRefusingATokenRetries(t *testing.T) {
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
			_, _ = io.WriteString(w, `{"code":401,"message":"Invalid JWT"}`)
			return
		}
		data, _ := io.ReadAll(r.Body)
		_, _ = w.Write(data)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	c.sess.RefreshToken = "r1"
	res, err := c.Invoke(context.Background(), "account", map[string]any{"action": "usage"})
	if err != nil || res["action"] != "usage" {
		t.Fatalf("res %v, %v", res, err)
	}
	if refreshes.Load() != 1 || calls.Load() != 2 {
		t.Errorf("%d refreshes, %d calls", refreshes.Load(), calls.Load())
	}
}

func TestIsNetwork(t *testing.T) {
	c := SignedInForTest("http://127.0.0.1:1", nil, "u1")
	_, refused := c.Select(context.Background(), "pages", Query{})
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	waited, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	req, _ := http.NewRequestWithContext(waited, http.MethodGet, slow.URL, nil)
	_, callersWait := http.DefaultClient.Do(req)
	req, _ = http.NewRequest(http.MethodGet, slow.URL, nil)
	_, clientsTimeout := (&http.Client{Timeout: 20 * time.Millisecond}).Do(req)
	called, callOff := context.WithCancel(context.Background())
	callOff()
	req, _ = http.NewRequestWithContext(called, http.MethodGet, slow.URL, nil)
	_, calledOff := http.DefaultClient.Do(req)
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{refused, true},
		{fmt.Errorf("reading: %w", io.ErrUnexpectedEOF), true},
		{&Error{Status: http.StatusServiceUnavailable}, true},
		{&Error{Status: http.StatusConflict}, false},
		{errors.New("the page's content on this device could not be read"), false},
		{expired.Err(), false},
		{callersWait, false},
		{calledOff, false},
		{clientsTimeout, true},
		{fmt.Errorf("fetch: %w", context.DeadlineExceeded), false},
		{ErrSignedOut, false},
		{nil, false},
	} {
		if got := IsNetwork(tc.err); got != tc.want {
			t.Errorf("IsNetwork(%v) = %v", tc.err, got)
		}
	}
}

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
	got, err := New(Settings{}, nil).ProjectOwner(context.Background(), Settings{URL: hs.URL, PublishableKey: "k"}, "sb_secret_s")
	if err != nil || got != "me@example.com" {
		t.Errorf("owner %q, %v", got, err)
	}
	maxUserPages = 1
	if got, _ := New(Settings{}, nil).ProjectOwner(context.Background(), Settings{URL: hs.URL, PublishableKey: "k"}, "sb_secret_s"); got != "" {
		t.Errorf("read past the last page it may: %q", got)
	}
}

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

func TestIsInvalidReadsTheCode(t *testing.T) {
	answers := map[string]struct {
		status int
		body   string
	}{
		"/rest/v1/pages":           {http.StatusBadRequest, `{"code":"23514","message":"page_cycle"}`},
		"/rest/v1/folders":         {http.StatusInternalServerError, `{"code":"P0002","message":"parent_not_found"}`},
		"/storage/v1/object/big/x": {http.StatusBadRequest, `{"statusCode":"413","error":"Payload too large"}`},
		"/storage/v1/object/ok/x":  {http.StatusRequestEntityTooLarge, `<html>too large</html>`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := answers[r.URL.Path]
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	defer srv.Close()
	c := SignedInForTest(srv.URL, nil, "u")
	ctx := context.Background()
	_, cycle := c.Update(ctx, "pages", []string{"id=eq.p"}, map[string]any{"parent_id": "q"})
	_, waits := c.Update(ctx, "folders", []string{"id=eq.f"}, map[string]any{"name": "x"})
	big := c.Upload(ctx, "big", "x", strings.NewReader("data"), "text/plain")
	huge := c.Upload(ctx, "ok", "x", strings.NewReader("data"), "text/plain")
	for _, tc := range []struct {
		err  error
		want bool
	}{{cycle, true}, {waits, false}, {big, true}, {huge, true}, {&Error{Status: http.StatusForbidden}, false}, {nil, false}} {
		if got := IsInvalid(tc.err); got != tc.want {
			t.Errorf("IsInvalid(%v) = %v", tc.err, got)
		}
	}
}

func TestStorageRefusalsReadTheirNames(t *testing.T) {
	answers := map[string]string{
		"/storage/v1/object/old/x": `{"statusCode":"400","error":"InvalidKey","message":"Invalid key: x"}`,
		"/storage/v1/object/new/x": `{"statusCode":"400","code":"InvalidKey","error":"InvalidKey","message":"Invalid key: x"}`,
		"/storage/v1/object/big/x": `{"statusCode":"413","code":"EntityTooLarge","error":"EntityTooLarge","message":"too large"}`,
		"/storage/v1/object/no/x":  `{"statusCode":"403","error":"Unauthorized","message":"new row violates row-level security policy"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, answers[r.URL.Path])
	}))
	defer srv.Close()
	c := SignedInForTest(srv.URL, nil, "u")
	for bucket, want := range map[string]struct{ key, invalid bool }{
		"old": {true, true}, "new": {true, true}, "big": {false, true}, "no": {false, false},
	} {
		err := c.Upload(context.Background(), bucket, "x", strings.NewReader("data"), "text/plain")
		if IsInvalidKey(err) != want.key || IsInvalid(err) != want.invalid {
			t.Errorf("%s: key %v, invalid %v: %v", bucket, IsInvalidKey(err), IsInvalid(err), err)
		}
	}
}

func TestABusyServerIsToldFromARefusedRequest(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&Error{Status: http.StatusInternalServerError, Code: "P0002"}, false},
		{&Error{Status: http.StatusInternalServerError, Code: "57014"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "57P01"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "40001"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "40P01"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "58000"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "XX000"}, true},
		{&Error{Status: http.StatusInternalServerError}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "500"}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "PGRST000"}, true},
		{&Error{Status: http.StatusBadGateway}, true},
		{&Error{Status: http.StatusTooManyRequests}, true},
		{&Error{Status: http.StatusBadRequest, Code: "23514"}, false},
		{errors.New("no"), false},
	} {
		if got := IsBusy(tc.err); got != tc.want {
			t.Errorf("IsBusy(%+v) = %v", tc.err, got)
		}
	}
}

func TestTooSlowIsToldWhoeverGaveUp(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c := SignedInForTest(srv.URL, nil, "u")
	c.TimeoutForTest(10 * time.Millisecond)
	_, client := c.Select(context.Background(), "pages", Query{})
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{client, true},
		{&Error{Status: http.StatusInternalServerError, Code: "57014"}, true},
		{&Error{Status: http.StatusGatewayTimeout}, true},
		{&Error{Status: 524}, true},
		{&Error{Status: http.StatusInternalServerError, Code: "XX000"}, false},
		{&Error{Status: http.StatusBadGateway}, false},
		{errors.New("no"), false},
	} {
		if got := IsTooSlow(tc.err); got != tc.want {
			t.Errorf("IsTooSlow(%v) = %v", tc.err, got)
		}
	}
	if !IsStatementTimeout(&Error{Status: http.StatusInternalServerError, Code: "57014"}) || IsStatementTimeout(&Error{Status: http.StatusGatewayTimeout}) {
		t.Error("IsStatementTimeout told them apart wrongly")
	}
}

func TestRPCAnswers(t *testing.T) {
	body := "<html>Sign in to the wifi</html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := SignedInForTest(srv.URL, nil, "u")
	if out, err := c.RPC(context.Background(), "page_members", nil); err == nil {
		t.Errorf("read %v", out)
	}
	body = ""
	if out, err := c.RPC(context.Background(), "sign_out_device", nil); err != nil || out != nil {
		t.Errorf("no answer: %v, %v", out, err)
	}
	body = `[{"user_id":"u1"}]`
	if out, err := c.RPC(context.Background(), "page_members", nil); err != nil || len(out.([]any)) != 1 {
		t.Errorf("a list: %v, %v", out, err)
	}
}

func TestErrorsInTheAppsLanguage(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "ja" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	if got := ErrSignedOut.Error(); got != "これを使うにはサインインしてください。" {
		t.Errorf("signed out: %q", got)
	}
	if !errors.Is(fmt.Errorf("sync: %w", ErrSignedOut), ErrSignedOut) || errors.Is(ErrSignedOut, ErrNotConfigured) {
		t.Error("ErrSignedOut is not itself alone")
	}
}

func readAll(body io.ReadCloser, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

func TestWritesAnswerWithTheColumnsAskedFor(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.Method+" "+r.URL.RawQuery)
		mu.Unlock()
		_, _ = io.WriteString(w, `[{"revision":2}]`)
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	ctx := context.Background()
	if _, err := c.UpdateReturning(ctx, "page_content", []string{Eq("page_id", "p")}, map[string]any{"plain_text": "x"}, "revision, updated_at"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InsertReturning(ctx, "page_content", map[string]any{"page_id": "p"}, "revision"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpsertReturning(ctx, "devices", map[string]any{"id": "d"}, "id", "revoked_at"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, "pages", []string{Eq("id", "p")}, map[string]any{"title": "x"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PATCH page_id=eq.p&select=revision%2C+updated_at",
		"POST select=revision",
		"POST on_conflict=id&select=revoked_at",
		"PATCH id=eq.p",
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(queries, "\n") != strings.Join(want, "\n") {
		t.Errorf("queries\n%s\nwant\n%s", strings.Join(queries, "\n"), strings.Join(want, "\n"))
	}
}

func TestDownloadComesAsItIsRead(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "RIFF")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	t.Cleanup(hs.Close)
	c := SignedInForTest(hs.URL, nil, "u1")
	data, err := readAll(c.Download(context.Background(), "recordings", "u1/r.wav"))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read %q, %v", data, err)
	}
}

func TestSessionsCount(t *testing.T) {
	c := expiredClient(t, &refreshServer{status: 200, body: newSession})
	n := c.Sessions()
	if _, _, err := c.token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Sessions() != n {
		t.Errorf("a token renewed began a session: %d, was %d", c.Sessions(), n)
	}
	c.SignInForTest("u1")
	if c.Sessions() != n+1 {
		t.Errorf("a sign-in: %d, was %d", c.Sessions(), n)
	}
	refused := expiredClient(t, &refreshServer{status: 400, body: `{"error":"invalid_grant"}`})
	m := refused.Sessions()
	if _, _, err := refused.token(context.Background()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("token: %v", err)
	}
	if refused.Sessions() != m+1 {
		t.Errorf("a session ended: %d, was %d", refused.Sessions(), m)
	}
}

type projectServer struct {
	mu      sync.Mutex
	seen    []string
	started chan struct{}
	release chan struct{}
}

func (ps *projectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	ps.mu.Lock()
	ps.seen = append(ps.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
	ps.mu.Unlock()
	if r.URL.Path != "/auth/v1/token" {
		_, _ = io.WriteString(w, "[]")
		return
	}
	if ps.started != nil {
		ps.started <- struct{}{}
		<-ps.release
	}
	_, _ = io.WriteString(w, strings.ReplaceAll(newSession, "%d", "-refreshed"))
}

func (ps *projectServer) saw(part string) []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []string
	for _, r := range ps.seen {
		if strings.Contains(r, part) {
			out = append(out, r)
		}
	}
	return out
}

func twoProjects(t *testing.T, a, b *projectServer) (c *Client, other *Pending) {
	t.Helper()
	ha, hb := httptest.NewServer(a), httptest.NewServer(b)
	t.Cleanup(ha.Close)
	t.Cleanup(hb.Close)
	c = New(Settings{}, nil)
	c.noKeychain = true
	c.cfg = Settings{URL: ha.URL, PublishableKey: "key-a"}
	c.sess = &session{AccessToken: "token-a", RefreshToken: "refresh-a", ExpiresAt: time.Now().Unix(), User: &authUser{ID: "u1"}}
	return c, &Pending{project: Settings{URL: hb.URL, PublishableKey: "key-b"},
		s: &session{AccessToken: "token-b", RefreshToken: "refresh-b", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: &authUser{ID: "u2"}}}
}

func TestSwitchingProjectsDuringARefreshSendsNoTokenAcross(t *testing.T) {
	a, b := &projectServer{started: make(chan struct{}), release: make(chan struct{})}, &projectServer{}
	c, other := twoProjects(t, a, b)
	errc := make(chan error)
	go func() {
		_, err := c.Select(context.Background(), "pages", Query{})
		errc <- err
	}()
	<-a.started
	if _, err := c.Adopt(other); err != nil {
		t.Fatal(err)
	}
	close(a.release)
	if err := <-errc; !errors.Is(err, ErrSignedOut) {
		t.Errorf("the request begun in the project left behind ended with %v, want signed out", err)
	}
	if got := a.saw("token-b"); len(got) != 0 {
		t.Errorf("the project left behind was sent the new project's token: %q", got)
	}
	if got := b.saw("-a"); len(got) != 0 {
		t.Errorf("the new project was sent what belongs to the one left behind: %q", got)
	}
	if got := b.saw("/rest/v1/pages"); len(got) != 0 {
		t.Errorf("the new project got the request meant for the one left behind: %q", got)
	}
	if _, err := c.Select(context.Background(), "pages", Query{}); err != nil {
		t.Fatal(err)
	}
	if got := b.saw("GET /rest/v1/pages Bearer token-b"); len(got) != 1 {
		t.Errorf("a request begun after the switch: %q", b.saw(""))
	}
}

func TestARefreshGoesToTheProjectOfItsSession(t *testing.T) {
	a, b := &projectServer{}, &projectServer{}
	c, other := twoProjects(t, a, b)
	c.mu.Lock()
	r := &refresh{from: c.sess, cfg: c.cfg, done: make(chan struct{})}
	c.refreshing = r
	c.mu.Unlock()
	if _, err := c.Adopt(other); err != nil {
		t.Fatal(err)
	}
	c.refresh(r)
	if got := b.saw("refresh-a"); len(got) != 0 {
		t.Errorf("the new project was sent the refresh token of the one left behind: %q", got)
	}
	if got := a.saw("POST /auth/v1/token"); len(got) != 1 {
		t.Errorf("the refresh went to its own project %d times, want 1", len(got))
	}
	if !r.stale || c.UserID() != "u2" {
		t.Errorf("the refresh of the session left behind replaced the new one: stale %v, user %q", r.stale, c.UserID())
	}
}
