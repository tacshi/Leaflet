package oauth

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listen(t *testing.T) *Loopback {
	t.Helper()
	l, err := ListenFor("Example", 0, false, "the-state", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

func comeBack(t *testing.T, l *Loopback, query, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", l.Redirect+"?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func waited(l *Loopback) (code string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	v, err := l.Wait(ctx)
	if err != nil && ctx.Err() != nil {
		return "waits", nil
	}
	return v.Get("code"), err
}

func TestChallengeIsS256(t *testing.T) {
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge %q", got)
	}
}

func TestLoopbackTakesTheAnswer(t *testing.T) {
	l := listen(t)
	if status, body := comeBack(t, l, "state=the-state&code=first", ""); status != http.StatusOK || !strings.Contains(body, "Signed in") {
		t.Errorf("answered %d %q", status, body)
	}
	if status, _ := comeBack(t, l, "state=the-state&code=again", ""); status != http.StatusOK {
		t.Errorf("answered again %d", status)
	}
	if code, err := waited(l); code != "first" || err != nil {
		t.Errorf("waited for %q, %v", code, err)
	}
}

func TestLoopbackRefusesAStaleState(t *testing.T) {
	l := listen(t)
	for _, q := range []string{"state=earlier&code=old", "code=old", "state=earlier&error=access_denied"} {
		if status, body := comeBack(t, l, q, ""); status != http.StatusBadRequest || !strings.Contains(body, "stale") {
			t.Errorf("%s: answered %d %q", q, status, body)
		}
	}
	if code, err := waited(l); code != "waits" || err != nil {
		t.Errorf("a stale answer ended the wait: %q, %v", code, err)
	}
	comeBack(t, l, "state=the-state&code=new", "")
	if code, err := waited(l); code != "new" || err != nil {
		t.Errorf("waited for %q, %v", code, err)
	}
}

func TestLoopbackRefusesAnotherHost(t *testing.T) {
	l := listen(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimSuffix(l.Redirect, "/auth/callback"), "http://"))
	for _, host := range []string{"localhost:" + port, "evil.example:" + port} {
		if status, _ := comeBack(t, l, "state=the-state&code=c", host); status != http.StatusNotFound {
			t.Errorf("%s: answered %d", host, status)
		}
	}
	res, err := http.Get(strings.TrimSuffix(l.Redirect, "callback") + "other?state=the-state&code=c")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("another path answered %d", res.StatusCode)
	}
	if code, err := waited(l); code != "waits" || err != nil {
		t.Errorf("another host's answer ended the wait: %q, %v", code, err)
	}
}

func TestLoopbackErrorEndsTheSignIn(t *testing.T) {
	for _, c := range []struct{ code, want string }{
		{"access_denied", "Sign-in was cancelled."},
		{"server_error", "Example sign-in failed: server_error"},
	} {
		l := listen(t)
		if status, body := comeBack(t, l, "state=the-state&error="+c.code, ""); status != http.StatusOK || !strings.Contains(body, "cancelled") {
			t.Errorf("%s: answered %d %q", c.code, status, body)
		}
		if _, err := waited(l); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.code, err, c.want)
		}
	}
}

func TestListenOnATakenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := Listen("Example", port, false); err == nil || !strings.Contains(err.Error(), "Example") {
		t.Errorf("listen on a taken port: %v", err)
	}
	l, err := Listen("Example", port, true)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if strings.Contains(l.Redirect, ":"+strconv.Itoa(port)+"/") {
		t.Errorf("redirect %s on the taken port", l.Redirect)
	}
}
