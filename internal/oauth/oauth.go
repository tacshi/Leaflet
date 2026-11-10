package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"leaflet/internal/guard"
	"leaflet/locales"
)

func NewState() string { return RandomString(32) }

func NewVerifier() string { return RandomString(48) }

func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type Loopback struct {
	Redirect        string
	State, Verifier string

	name string
	ln   net.Listener
	srv  *http.Server
	done chan answer
}

type answer struct {
	query url.Values
	err   error
}

func Listen(name string, port int, anyPort bool) (*Loopback, error) {
	return ListenFor(name, port, anyPort, NewState(), NewVerifier())
}

func ListenFor(name string, port int, anyPort bool, state, verifier string) (*Loopback, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && anyPort {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, errors.New(locales.OauthPortTaken(name, port))
	}
	host := fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	l := &Loopback{
		Redirect: "http://" + host + "/auth/callback",
		State:    state,
		Verifier: verifier,
		name:     name,
		ln:       ln,
		done:     make(chan answer, 1),
	}
	finish := func(a answer) {
		select {
		case l.done <- a:
		default:
		}
	}
	l.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/auth/callback" || r.Host != host {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(v.Get("state")), []byte(l.State)) != 1 {
			http.Error(w, locales.OauthStale(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := v.Get("error"); e != "" {
			_, _ = fmt.Fprint(w, page(locales.OauthCancelledTitle(), locales.OauthCloseTab()))
			msg := locales.OauthCancelled()
			if e != "access_denied" {
				msg = locales.OauthFailed(name, e)
			}
			finish(answer{err: errors.New(msg)})
			return
		}
		_, _ = fmt.Fprint(w, page(locales.OauthSignedIn(), locales.OauthCloseTabBack()))
		finish(answer{query: v})
	})}
	guard.Go(func() { _ = l.srv.Serve(ln) })
	return l, nil
}

func (l *Loopback) Challenge() string { return Challenge(l.Verifier) }

func (l *Loopback) Wait(ctx context.Context) (url.Values, error) {
	select {
	case a := <-l.done:
		return a.query, a.err
	case <-ctx.Done():
		return nil, errors.New(locales.OauthTimedOut())
	}
}

func (l *Loopback) Close() {
	_ = l.srv.Close()
	_ = l.ln.Close()
}

func page(title, body string) string {
	return `<!doctype html><meta charset="utf-8"><title>Leaflet</title><body style="font:16px -apple-system,system-ui,sans-serif;display:grid;place-items:center;height:90vh;color:#16181d"><div style="text-align:center"><h2 style="margin:0 0 8px">` +
		title + `</h2><p style="color:#565c67;margin:0">` + body + `</p></div></body>`
}
