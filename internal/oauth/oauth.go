// Package oauth is the part of a browser sign-in that runs on this device:
// a loopback redirect, PKCE and the page the browser lands on.
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
)

// RandomString is n random bytes, base64url.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Loopback waits for the browser to come back to this device with a
// sign-in's answer.
type Loopback struct {
	// Redirect is the redirect_uri to send.
	Redirect string
	// State and Verifier are the sign-in's state and PKCE verifier.
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

// Listen starts a loopback for a sign-in with name, such as "ChatGPT", on
// port; anyPort lets another port do when that one is taken, for providers
// that take any loopback port.
func Listen(name string, port int, anyPort bool) (*Loopback, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && anyPort {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, fmt.Errorf("Leaflet couldn't wait for %s's sign-in on port %d, which another app is using. Quit it, then try again.", name, port) //nolint:staticcheck // shown to people as a sentence.
	}
	host := fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	l := &Loopback{
		Redirect: "http://" + host + "/auth/callback",
		State:    RandomString(32),
		Verifier: RandomString(48),
		name:     name,
		ln:       ln,
		done:     make(chan answer, 1),
	}
	// The first answer counts: another, such as the tab loaded again,
	// finds done full and is dropped rather than waiting for ever.
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
			http.Error(w, "This sign-in link is stale. Start again from Leaflet.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := v.Get("error"); e != "" {
			_, _ = fmt.Fprint(w, page("Sign-in was cancelled", "You can close this tab."))
			msg := "Sign-in was cancelled."
			if e != "access_denied" {
				msg = name + " sign-in failed: " + e
			}
			finish(answer{err: errors.New(msg)})
			return
		}
		_, _ = fmt.Fprint(w, page("Signed in to Leaflet", "You can close this tab and go back to Leaflet."))
		finish(answer{query: v})
	})}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

// Challenge is the S256 PKCE challenge of the verifier.
func (l *Loopback) Challenge() string {
	sum := sha256.Sum256([]byte(l.Verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Wait returns the query the browser came back with, which has the code.
func (l *Loopback) Wait(ctx context.Context) (url.Values, error) {
	select {
	case a := <-l.done:
		return a.query, a.err
	case <-ctx.Done():
		return nil, errors.New("Sign-in timed out. Try again.") //nolint:staticcheck // shown to people as a sentence.
	}
}

// Close stops waiting.
func (l *Loopback) Close() {
	_ = l.srv.Close()
	_ = l.ln.Close()
}

func page(title, body string) string {
	return `<!doctype html><meta charset="utf-8"><title>Leaflet</title><body style="font:16px -apple-system,system-ui,sans-serif;display:grid;place-items:center;height:90vh;color:#16181d"><div style="text-align:center"><h2 style="margin:0 0 8px">` +
		title + `</h2><p style="color:#565c67;margin:0">` + body + `</p></div></body>`
}
