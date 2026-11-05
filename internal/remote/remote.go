// Package remote talks to Supabase over HTTP: Auth (GoTrue), the tables
// (PostgREST), Storage and the Edge Functions. Without a configured URL and
// key the app runs on this device only.
package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/guard"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Build-time defaults, set with -ldflags "-X leaflet/internal/remote.DefaultURL=…".
var (
	DefaultURL            = ""
	DefaultPublishableKey = ""
)

// Settings say which Supabase project to use.
type Settings struct {
	URL string `json:"url"`
	// PublishableKey is kept under its older name, anon key, as devices
	// saved it.
	PublishableKey string `json:"anon_key"`
	// Ref and Name are the project's, when the person chose it with their
	// Supabase account; a server the build or environment fixes has none.
	Ref  string `json:"ref,omitempty"`
	Name string `json:"name,omitempty"`
}

// Config reads the project from SUPABASE_URL and
// SUPABASE_PUBLISHABLE_KEY, or the build's defaults. Without them, New
// uses the project kept on this device.
func Config() Settings {
	s := Settings{URL: DefaultURL, PublishableKey: DefaultPublishableKey}
	if v := os.Getenv("SUPABASE_URL"); v != "" {
		s.URL = v
	}
	if v := os.Getenv("SUPABASE_PUBLISHABLE_KEY"); v != "" {
		s.PublishableKey = v
	}
	s.URL = strings.TrimRight(s.URL, "/")
	return s
}

// backendKey keeps the project chosen on this device.
const backendKey = "backend"

// Error is a failed request, with the server's message.
type Error struct {
	Status  int
	Message string
	// Code is the error code in the answer, or "": the database's, which
	// PostgREST answers with, as "23514" for a check the change fails, or
	// Storage's status, as "413" for an object too large.
	Code string
	// Name is what Storage calls the refusal, as "InvalidKey", or "".
	Name string
	// RetryAfter is how long the server asked to be left before it is
	// asked again (Retry-After), 0 when it did not say.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }

// ErrSignedOut is returned by calls that need an account. Errors such as
// it say themselves in the app's language, as they are shown.
var ErrSignedOut error = &sentence{locales.RemoteSignedOut}

// ErrNotConfigured is returned when the build has no server.
var ErrNotConfigured error = &sentence{locales.RemoteNotConfigured}

// sentence is an error shown to people as it is, said in the app's
// language when it is shown.
type sentence struct{ say func() string }

func (s *sentence) Error() string { return s.say() }

type session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    int64     `json:"expires_at"`
	User         *authUser `json:"user,omitempty"`
}

type authUser struct {
	ID           string         `json:"id"`
	Email        string         `json:"email"`
	UserMetadata map[string]any `json:"user_metadata"`
}

// Client is the connection to the server.
type Client struct {
	// cfg is guarded by mu: Use changes it while requests run.
	cfg Settings
	// fixed says the build or environment set cfg, which Use then leaves.
	fixed bool
	st    *store.Store
	http  *http.Client
	// long is for what can outlast http's timeout: Storage uploads and
	// downloads, and streamed runs (newLongClient).
	long *http.Client

	mu   sync.Mutex
	sess *session
	// refused is an access token the server refused before it expired,
	// which token renews.
	refused string
	// refreshing is the refresh under way, which other callers wait for.
	refreshing *refresh
	// sessions counts the sessions the client has had (Sessions); guarded
	// by mu.
	sessions uint64
	// keepMu has the keychain written in the order sess changes.
	keepMu sync.Mutex
	// noKeychain keeps the session in memory only, for tests.
	noKeychain bool
}

// refresh is one exchange of a refresh token for a new session.
type refresh struct {
	from *session
	done chan struct{}
	// After done: the new access token, or why there is none. stale says
	// the session changed meanwhile, so the result was thrown away.
	tok   string
	err   error
	stale bool
}

// New returns a client for a project.
func New(cfg Settings, st *store.Store) *Client {
	c := &Client{cfg: cfg, fixed: cfg.URL != "", st: st, http: &http.Client{Timeout: 90 * time.Second}, long: newLongClient()}
	if !c.fixed && st != nil {
		if raw, ok := st.Value(backendKey); ok {
			_ = json.Unmarshal([]byte(raw), &c.cfg)
		}
	}
	return c
}

// settings returns the project in use.
func (c *Client) settings() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Fixed reports whether the build or environment sets the server, rather
// than the person choosing their project.
func (c *Client) Fixed() bool { return c != nil && c.fixed }

// Project returns the project in use.
func (c *Client) Project() Settings { return c.settings() }

// Use makes s the project, kept on this device; an empty s forgets it.
// The session, which belongs to the project before, goes.
func (c *Client) Use(s Settings) error {
	if c.fixed {
		return errors.New(locales.RemoteServerFixed())
	}
	s.URL = strings.TrimRight(s.URL, "/")
	c.mu.Lock()
	same := c.cfg == s
	c.mu.Unlock()
	if same {
		return nil
	}
	c.keep(nil)
	c.mu.Lock()
	c.cfg = s
	c.mu.Unlock()
	if c.st == nil {
		return nil
	}
	if s.URL == "" {
		return c.st.DeleteValue(backendKey)
	}
	raw, _ := json.Marshal(s)
	return c.st.SetValue(backendKey, string(raw))
}

// longHeaderWait is how long the long client waits for a server to start
// answering, once the request is sent; tests shorten it.
var longHeaderWait = time.Minute

// newLongClient returns a client with no timeout on a whole request,
// which a large recording takes longer than to upload: the caller's ctx
// ends one. It still gives up on a server that does not connect, or
// does not answer once the request is sent, and on connections left
// idle.
func newLongClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = longHeaderWait
	t.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: t}
}

// Configured reports whether the build has a server.
func (c *Client) Configured() bool {
	if c == nil {
		return false
	}
	cfg := c.settings()
	return cfg.URL != "" && cfg.PublishableKey != ""
}

// URL is the project's URL.
func (c *Client) URL() string { return c.settings().URL }

// SignedIn reports whether someone is signed in.
func (c *Client) SignedIn() bool {
	if !c.Configured() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

// User returns the signed-in account.
func (c *Client) User() *store.User {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil || c.sess.User == nil {
		return nil
	}
	return toUser(c.sess.User)
}

// Sessions counts the sessions the client has had: each sign-in,
// sign-out, session kept from before, or one that ended, as its refresh
// token was refused, begins another; renewing its token does not. A
// change tells sync to learn at once whether the device was signed out
// from another.
func (c *Client) Sessions() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions
}

// UserID is the signed-in account's ID, or "".
func (c *Client) UserID() string {
	if u := c.User(); u != nil {
		return u.ID
	}
	return ""
}

func toUser(u *authUser) *store.User {
	name, _ := u.UserMetadata["display_name"].(string)
	return &store.User{ID: u.ID, Email: u.Email, DisplayName: name}
}

const secretKey = "supabase.session"

// RestoreSession signs back in with the session kept from last time.
func (c *Client) RestoreSession() *store.User {
	raw, err := keychain.Get(secretKey)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var s session
	if json.Unmarshal(raw, &s) != nil || s.RefreshToken == "" {
		return nil
	}
	c.mu.Lock()
	c.sess = &s
	c.sessions++
	c.mu.Unlock()
	if s.User == nil {
		return nil
	}
	return toUser(s.User)
}

// keep makes s the session, in memory and in the keychain.
func (c *Client) keep(s *session) { c.replace(nil, s, true) }

// replace is keep while the session is still old, or always with force; it
// reports whether it kept s.
func (c *Client) replace(old, s *session, force bool) bool {
	c.keepMu.Lock()
	defer c.keepMu.Unlock()
	c.mu.Lock()
	if !force && c.sess != old {
		c.mu.Unlock()
		return false
	}
	// A refresh renews the session; anything else, a sign-in, a sign-out
	// or a session that ended, is another.
	if force || s == nil {
		c.sessions++
	}
	c.sess, c.refused = s, ""
	c.mu.Unlock()
	if c.noKeychain {
		return true
	}
	if s == nil {
		_ = keychain.Delete(secretKey)
		return true
	}
	// Keep what fits in the keychain; the access token is fetched again.
	small := *s
	if raw, _ := json.Marshal(small); len(raw) > keychain.MaxSecretSize { // #nosec G117 -- the session goes to the keychain.
		small.AccessToken = ""
		small.ExpiresAt = 0
	}
	raw, _ := json.Marshal(small) // #nosec G117 -- the session goes to the keychain.
	_ = keychain.Set(secretKey, raw)
	return true
}

func (c *Client) authRequest(ctx context.Context, path string, body any, withToken bool) (map[string]any, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	tok := ""
	if withToken {
		var err error
		if tok, err = c.token(ctx); err != nil {
			return nil, err
		}
	}
	return c.authRequestTo(ctx, c.settings(), path, body, tok)
}

// authRequestTo posts to Auth of the project cfg, as tok when it is set.
func (c *Client) authRequestTo(ctx context.Context, cfg Settings, path string, body any, tok string) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL+"/auth/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", cfg.PublishableKey)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	data, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(data, &out)
	if res.StatusCode >= 400 {
		return out, &Error{Status: res.StatusCode, Message: message(out, string(data)), RetryAfter: retryAfter(res.Header)}
	}
	return out, nil
}

func message(m map[string]any, fallback string) string {
	for _, k := range []string{"error_description", "msg", "message", "error"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	if fallback = strings.TrimSpace(fallback); fallback == "" || len(fallback) > 200 || strings.HasPrefix(fallback, "<") {
		return "Request failed"
	}
	return fallback
}

// decodeSession reads the session in the server's answer to a sign-in or
// a refresh.
func decodeSession(out map[string]any) (*session, error) {
	raw, _ := json.Marshal(out)
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	// How long the token lasts, from when it came, goes by this device's
	// clock, which may be off from the server's that expires_at is on.
	if exp, ok := out["expires_in"].(float64); ok && exp > 0 {
		s.ExpiresAt = time.Now().Unix() + int64(exp)
	}
	if s.AccessToken == "" {
		return nil, errNoSession
	}
	return &s, nil
}

var errNoSession error = &sentence{locales.RemoteNoSession}

func (c *Client) fromToken(out map[string]any) (*store.User, error) {
	s, err := decodeSession(out)
	if err != nil {
		return nil, err
	}
	if s.User == nil {
		return nil, errNoSession
	}
	c.keep(s)
	return toUser(s.User), nil
}

// SendCode emails a sign-in link and code.
func (c *Client) SendCode(email string) error {
	_, err := c.authRequest(context.Background(), "otp", map[string]any{"email": email, "create_user": true}, false)
	return err
}

// ownerFlag marks, in app_metadata, the account SignInAsOwner made: the
// person who owns the project.
const ownerFlag = "leaflet_owner"

// ProjectOwner returns the email of project's owner, the account
// SignInAsOwner made, or "" when there is none yet. secret is the
// project's secret key, which is not kept.
func (c *Client) ProjectOwner(ctx context.Context, project Settings, secret string) (string, error) {
	// Auth lists its users a page at a time: a project with more of them
	// than a page holds has the owner on any one.
	for page := 1; page <= maxUserPages; page++ {
		out, err := c.adminRequest(ctx, project, secret, "GET", fmt.Sprintf("admin/users?page=%d&per_page=%d", page, usersPerPage), nil)
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(out["users"])
		var users []struct {
			Email       string         `json:"email"`
			AppMetadata map[string]any `json:"app_metadata"`
		}
		_ = json.Unmarshal(raw, &users)
		for _, u := range users {
			if u.AppMetadata[ownerFlag] == true {
				return u.Email, nil
			}
		}
		if len(users) < usersPerPage {
			break
		}
	}
	return "", nil
}

// usersPerPage is how many users ProjectOwner asks Auth for at a time, and
// maxUserPages how many pages it reads at most, in case a server sends
// the same page whatever is asked. Tests make the pages smaller.
var usersPerPage, maxUserPages = 1000, 1000

// Pending is a sign-in to a project that is not the client's yet: Adopt
// makes it so, Discard throws it away.
type Pending struct {
	project Settings
	s       *session
}

// SignInAsOwner signs in to project as the person who owns it, by email,
// leaving the client as it is. secret is the project's secret key: it
// makes their account when there is none, marked as the owner's, then a
// sign-in link used at once, and is not kept.
//
// An account there before, not marked, was made some other way, as by
// signing up, and perhaps by someone else, who used the person's email
// before them: its password and its sessions are then that someone's. So
// it gets a new password no one knows, and its other sessions end, before
// it is marked as the owner's; until it is, the next sign-in does all of
// this again.
func (c *Client) SignInAsOwner(ctx context.Context, project Settings, secret, email, name string) (*Pending, error) {
	project.URL = strings.TrimRight(project.URL, "/")
	owner := map[string]any{ownerFlag: true}
	out, err := c.adminRequest(ctx, project, secret, "POST", "admin/users", map[string]any{
		"email": email, "email_confirm": true, "user_metadata": map[string]any{"display_name": name}, "app_metadata": owner,
	})
	made := err == nil
	if err != nil && !accountExists(out) {
		return nil, err
	}
	magicLink := map[string]any{"type": "magiclink", "email": email}
	link, err := c.adminRequest(ctx, project, secret, "POST", "admin/generate_link", magicLink)
	if err != nil {
		return nil, err
	}
	id, _ := link["id"].(string)
	takeOver := false
	if !made && id != "" {
		user, err := c.adminRequest(ctx, project, secret, "GET", "admin/users/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		if meta, _ := user["app_metadata"].(map[string]any); meta[ownerFlag] != true {
			password, err := newPassword()
			if err != nil {
				return nil, err
			}
			if _, err := c.adminRequest(ctx, project, secret, "PUT", "admin/users/"+url.PathEscape(id), map[string]any{"password": password}); err != nil {
				return nil, err
			}
			// A link made before the password changed may not outlive it.
			if link, err = c.adminRequest(ctx, project, secret, "POST", "admin/generate_link", magicLink); err != nil {
				return nil, err
			}
			takeOver = true
		}
	}
	if props, ok := link["properties"].(map[string]any); ok {
		link = props
	}
	hash, _ := link["hashed_token"].(string)
	kind, _ := link["verification_type"].(string)
	if hash == "" {
		return nil, errNoSession
	}
	if kind == "" {
		kind = "magiclink"
	}
	out, err = c.authRequestTo(ctx, project, "verify", map[string]any{"type": kind, "token_hash": hash}, "")
	if err != nil {
		return nil, err
	}
	s, err := decodeSession(out)
	if err != nil {
		return nil, err
	}
	if s.User == nil {
		return nil, errNoSession
	}
	p := &Pending{project: project, s: s}
	if takeOver {
		if _, err := c.authRequestTo(ctx, project, logoutOthers, map[string]any{}, s.AccessToken); err != nil {
			c.Discard(p)
			return nil, err
		}
		if _, err := c.adminRequest(ctx, project, secret, "PUT", "admin/users/"+url.PathEscape(id), map[string]any{"app_metadata": owner}); err != nil {
			c.Discard(p)
			return nil, err
		}
	}
	return p, nil
}

// newPassword is a password for an account that signs in only by links:
// random, and never kept or shown.
func newPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Adopt makes p's project the client's and its sign-in the session.
func (c *Client) Adopt(p *Pending) (*store.User, error) {
	if err := c.Use(p.project); err != nil {
		return nil, err
	}
	c.keep(p.s)
	return toUser(p.s.User), nil
}

// logoutHere ends only the session the token is of: Auth's logout ends
// every session of the user, on every device, unless told otherwise.
const logoutHere = "logout?scope=local"

// logoutOthers ends every session of the user but the one the token is of.
const logoutOthers = "logout?scope=others"

// Discard signs p out on the server, leaving the client's session alone.
func (c *Client) Discard(p *Pending) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.authRequestTo(ctx, p.project, logoutHere, map[string]any{}, p.s.AccessToken)
}

// accountExists reports whether Auth refused to make an account because
// it has one by that email, and not for another reason it also answers
// 422 for, such as the email being invalid.
func accountExists(out map[string]any) bool {
	code, _ := out["error_code"].(string)
	if code == "" {
		code, _ = out["code"].(string)
	}
	return code == "email_exists" || code == "user_already_exists"
}

// adminRequest makes a request of Auth's admin API of project with its
// secret key.
func (c *Client) adminRequest(ctx context.Context, project Settings, secret, method, path string, body any) (map[string]any, error) {
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, project.URL+"/auth/v1/"+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(secret, "eyJ") {
		// A legacy service_role key is a JWT, which Auth also wants as
		// the bearer; a secret key goes in apikey alone.
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	data, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(data, &out)
	if res.StatusCode >= 400 {
		return out, &Error{Status: res.StatusCode, Message: message(out, string(data)), RetryAfter: retryAfter(res.Header)}
	}
	return out, nil
}

// VerifyCode signs in with the code from the email.
func (c *Client) VerifyCode(email, code string) (*store.User, error) {
	out, err := c.authRequest(context.Background(), "verify", map[string]any{"type": "email", "email": email, "token": strings.TrimSpace(code)}, false)
	if err != nil {
		return nil, err
	}
	return c.fromToken(out)
}

// SignOut signs out on this device, and the store's session follows
// (signedOut).
func (c *Client) SignOut() {
	if c.SignedIn() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = c.authRequest(ctx, logoutHere, map[string]any{}, true)
		cancel()
	}
	c.keep(nil)
	c.signedOut()
}

// signedOut makes the store's session the device's own once the client's
// has ended, as the next launch loads it: signed out on purpose, or on its
// own, as when the server refused the refresh token or sync found the
// device revoked. The account's pages stay, for signing in again, until
// the caller clears them. A sign-in, which may replace another account's
// pages, becomes the store's session only once the user agrees to that
// (core.UseAccount); one made meanwhile is left alone.
func (c *Client) signedOut() {
	if c.st == nil || c.SignedIn() {
		return
	}
	if _, err := c.st.LoadSession(nil); err != nil {
		log.Printf("sign out: %v", err)
	}
}

// tokenLasts is how long an access token must still last for a request:
// one that runs out sooner is renewed first.
const tokenLasts = time.Minute

// longTokenLasts is tokenLasts for a function that works a while, an agent
// run or a transcription: it writes as the caller until it ends, up to the
// platform's 400 s limit on paid plans, and fails where the token runs out
// first. The function refuses a token that would (requireTokenUntil); this
// renews it before asking.
const longTokenLasts = 8 * time.Minute

// token returns a fresh access token, refreshing it when it is about to
// expire. Callers that need a refresh at once share one.
func (c *Client) token(ctx context.Context) (string, error) {
	return c.tokenLasting(ctx, tokenLasts)
}

// tokenLasting is token for a token that lasts at least d more. When even
// a new one lasts less, as the project's tokens may if set that short, the
// new one goes as it is: the function lets it through, since renewing it
// again would not help.
func (c *Client) tokenLasting(ctx context.Context, d time.Duration) (string, error) {
	for {
		c.mu.Lock()
		s := c.sess
		if s == nil {
			c.mu.Unlock()
			return "", ErrSignedOut
		}
		if s.AccessToken != "" && s.AccessToken != c.refused && time.Now().Add(d).Unix() < s.ExpiresAt {
			c.mu.Unlock()
			return s.AccessToken, nil
		}
		r := c.refreshing
		if r == nil || r.from != s {
			r = &refresh{from: s, done: make(chan struct{})}
			c.refreshing = r
			go c.refresh(r)
		}
		c.mu.Unlock()
		select {
		case <-r.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		// Signed out or in as someone else meanwhile: go by the session now.
		if !r.stale {
			return r.tok, r.err
		}
	}
}

// refresh trades r's refresh token for a new session, and keeps it only
// if r's session is still the one in use, so a sign-out or another sign-in
// meanwhile is not undone.
func (c *Client) refresh(r *refresh) {
	defer close(r.done)
	defer func() {
		c.mu.Lock()
		if c.refreshing == r {
			c.refreshing = nil
		}
		c.mu.Unlock()
	}()
	// A panic is the refresh's failure, which those waiting get, rather
	// than the app's end.
	defer guard.RecoverAs(&r.err)
	// Not a caller's context: others wait for this too, and one giving up
	// should not fail it for the rest. The client's timeout still applies.
	out, err := c.authRequest(context.Background(), "token?grant_type=refresh_token", map[string]any{"refresh_token": r.from.RefreshToken}, false)
	var s *session
	if err == nil {
		s, err = decodeSession(out)
	}
	if err != nil {
		var re *Error
		// GoTrue answers a revoked, used or unknown refresh token with 400
		// ("invalid_grant", "refresh_token_not_found" and the like). Other
		// failures, a 429 among them, may pass, so they keep the session.
		if errors.As(err, &re) && (re.Status == http.StatusBadRequest || re.Status == http.StatusUnauthorized) {
			if !c.replace(r.from, nil, false) {
				r.stale = true
				return
			}
			c.signedOut()
			r.err = ErrSignedOut
			return
		}
		c.mu.Lock()
		r.stale = c.sess != r.from
		c.mu.Unlock()
		r.err = err
		return
	}
	if s.User == nil {
		s.User = r.from.User
	}
	if !c.replace(r.from, s, false) {
		r.stale = true
		return
	}
	r.tok = s.AccessToken
}

// UpdateUser changes the account's profile data. It goes as the other
// requests do, so a token the server refused is renewed and the change
// sent again.
func (c *Client) UpdateUser(data map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return err
	}
	res, err := c.request(ctx, "PUT", "/auth/v1/user", bytes.NewReader(raw), nil)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

// request makes an authenticated request to a path of the project.
func (c *Client) request(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	return c.requestWith(c.http, ctx, tokenLasts, method, path, body, headers)
}

// requestWith is request through hc, with a token that lasts at least
// lasting more.
func (c *Client) requestWith(hc *http.Client, ctx context.Context, lasting time.Duration, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	cfg := c.settings()
	req, err := http.NewRequestWithContext(ctx, method, cfg.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", cfg.PublishableKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// A body that can be read again (bytes, not a file being uploaded)
	// lets a request refused for its token go again.
	again := body == nil || req.GetBody != nil
	for {
		tok, err := c.tokenLasting(ctx, lasting)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode < 400 {
			return res, nil
		}
		data, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if again && staleToken(res.StatusCode, data) {
			// The token expired early, by the server's clock, was
			// revoked, or would run out before a function finished:
			// renew it and try once more.
			again = false
			c.refuse(tok)
			next := req.Clone(ctx)
			if req.GetBody != nil {
				if next.Body, err = req.GetBody(); err != nil {
					return nil, err
				}
			}
			req = next
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		// Storage says its own status in the body, which may differ from
		// the response's, as a 400 for an object too large, and names the
		// refusal beside it.
		code, _ := m["code"].(string)
		name := ""
		if status, _ := m["statusCode"].(string); status != "" {
			name, _ = m["error"].(string)
			if name == "" {
				name = code
			}
			code = status
		}
		return nil, &Error{Status: res.StatusCode, Message: message(m, string(data)), Code: code, Name: name, RetryAfter: retryAfter(res.Header)}
	}
}

// refuse has token renew tok, which the server refused, unless it has
// been renewed already.
func (c *Client) refuse(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil && c.sess.AccessToken == tok {
		c.refused = tok
	}
}

// staleToken reports whether the server refused a request for its access
// token, expired or no longer valid: PostgREST and the Functions gateway
// answer 401 ("JWT expired", "Invalid JWT"), Storage 400 or 403 ("jwt
// expired", "exp" claim timestamp check failed).
func staleToken(status int, body []byte) bool {
	if status != http.StatusUnauthorized && status != http.StatusBadRequest && status != http.StatusForbidden {
		return false
	}
	b := strings.ToLower(string(body))
	return strings.Contains(b, "jwt") || strings.Contains(b, "claim timestamp check failed")
}

func decodeRows(res *http.Response) ([]map[string]any, error) {
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var rows []map[string]any
	if data[0] == '{' {
		var one map[string]any
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, err
		}
		return []map[string]any{one}, nil
	}
	err = json.Unmarshal(data, &rows)
	return rows, err
}

// Query is a PostgREST query: filters as "column=op.value", select, order
// and limit.
type Query struct {
	Select  string
	Filters []string
	Order   string
	Limit   int
}

// Eq, Gt, NotNull and In build filters.
func Eq(col, v string) string   { return col + "=eq." + v }
func Gt(col, v string) string   { return col + "=gt." + v }
func NotNull(col string) string { return col + "=not.is.null" }
func In(col string, vs []string) string {
	quoted := make([]string, len(vs))
	for i, v := range vs {
		quoted[i] = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return col + "=in.(" + strings.Join(quoted, ",") + ")"
}

// Select reads rows of a table.
func (c *Client) Select(ctx context.Context, table string, q Query) ([]map[string]any, error) {
	v := url.Values{}
	sel := q.Select
	if sel == "" {
		sel = "*"
	}
	v.Set("select", sel)
	if q.Order != "" {
		v.Set("order", q.Order)
	}
	if q.Limit > 0 {
		v.Set("limit", fmt.Sprint(q.Limit))
	}
	path := "/rest/v1/" + table + "?" + v.Encode()
	for _, f := range q.Filters {
		k, val, _ := strings.Cut(f, "=")
		path += "&" + url.QueryEscape(k) + "=" + url.QueryEscape(val)
	}
	res, err := c.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeRows(res)
}

// SelectOne reads one row, or nil.
func (c *Client) SelectOne(ctx context.Context, table string, q Query) (map[string]any, error) {
	q.Limit = 1
	rows, err := c.Select(ctx, table, q)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// Upsert inserts or updates rows and returns them as stored.
func (c *Client) Upsert(ctx context.Context, table string, rows any, onConflict string) ([]map[string]any, error) {
	return c.UpsertReturning(ctx, table, rows, onConflict, "")
}

// UpsertReturning is Upsert answered with only the columns sel names, as
// a select does, or every column for "".
func (c *Client) UpsertReturning(ctx context.Context, table string, rows any, onConflict, sel string) ([]map[string]any, error) {
	var q []string
	if onConflict != "" {
		q = append(q, "on_conflict="+url.QueryEscape(onConflict))
	}
	return c.write(ctx, "POST", table, q, rows, "resolution=merge-duplicates,return=representation", sel)
}

// Update changes the rows that match the filters and returns them as
// stored; none when nothing matched.
func (c *Client) Update(ctx context.Context, table string, filters []string, values map[string]any) ([]map[string]any, error) {
	return c.UpdateReturning(ctx, table, filters, values, "")
}

// UpdateReturning is Update answered with only the columns sel names, as a
// select does, or every column for "": a page's content, saved whole, need
// not come back whole to say which revision it is now.
func (c *Client) UpdateReturning(ctx context.Context, table string, filters []string, values map[string]any, sel string) ([]map[string]any, error) {
	q := make([]string, len(filters))
	for i, f := range filters {
		k, v, _ := strings.Cut(f, "=")
		q[i] = url.QueryEscape(k) + "=" + url.QueryEscape(v)
	}
	return c.write(ctx, "PATCH", table, q, values, "return=representation", sel)
}

// write sends body to a table with method and the query q, and returns the
// rows the server answers with, of the columns sel names when it is set.
func (c *Client) write(ctx context.Context, method, table string, q []string, body any, prefer, sel string) ([]map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if sel != "" {
		q = append(q, "select="+url.QueryEscape(sel))
	}
	path := "/rest/v1/" + table
	if len(q) > 0 {
		path += "?" + strings.Join(q, "&")
	}
	res, err := c.request(ctx, method, path, bytes.NewReader(raw), map[string]string{"Prefer": prefer})
	if err != nil {
		return nil, err
	}
	return decodeRows(res)
}

// IsConflict reports whether err is the server refusing a row that already
// exists.
func IsConflict(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusConflict
}

// IsInvalid reports whether err is the server refusing for good what it
// was sent, whoever sends it: a change that breaks a rule of its rows (a
// check, 23514), as moving a page under one another device moved under it
// meanwhile, a body larger than it takes (413), or an object's key Storage
// does not take. Sent again, it fails again.
func IsInvalid(err error) bool {
	var re *Error
	return errors.As(err, &re) && (re.Code == "23514" || re.Code == "413" || re.Status == http.StatusRequestEntityTooLarge || re.Name == invalidKey)
}

// invalidKey is what Storage calls a refusal of an object's key.
const invalidKey = "InvalidKey"

// IsInvalidKey reports whether err is Storage refusing an object's key:
// there can be no object by that key.
func IsInvalidKey(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Name == invalidKey
}

// IsForbidden reports whether err is the server refusing a change the
// user may not make, as its row-level security does.
func IsForbidden(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusForbidden
}

// IsNotFound reports whether err is the server having no such thing.
func IsNotFound(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusNotFound
}

// Insert inserts rows and returns them as stored.
func (c *Client) Insert(ctx context.Context, table string, rows any) ([]map[string]any, error) {
	return c.InsertReturning(ctx, table, rows, "")
}

// InsertReturning is Insert answered with only the columns sel names, as
// a select does, or every column for "".
func (c *Client) InsertReturning(ctx context.Context, table string, rows any, sel string) ([]map[string]any, error) {
	return c.write(ctx, "POST", table, nil, rows, "return=representation", sel)
}

// InsertNew inserts rows the server may have already, as an upload whose
// answer was lost and goes again: one it has is left as it is, and no row
// comes back, so no rule of who may change or read the row stands in the
// way of a row that is up.
func (c *Client) InsertNew(ctx context.Context, table string, rows any) error {
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	res, err := c.request(ctx, "POST", "/rest/v1/"+table, bytes.NewReader(raw), map[string]string{"Prefer": "resolution=ignore-duplicates,return=minimal"})
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

// RPC calls a database function.
func (c *Client) RPC(ctx context.Context, name string, args map[string]any) (any, error) {
	raw, _ := json.Marshal(args)
	res, err := c.request(ctx, "POST", "/rest/v1/rpc/"+name, bytes.NewReader(raw), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var out any
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	// An answer that does not parse is no answer, as a sign-in page a
	// network puts in the server's place: read as none, it would say a page
	// has no members.
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return out, nil
}

// Invoke calls an Edge Function and returns its JSON body.
func (c *Client) Invoke(ctx context.Context, name string, body map[string]any) (map[string]any, error) {
	return c.invoke(c.http, ctx, tokenLasts, name, body)
}

// InvokeLong is Invoke for a function that works a while before it
// answers, longer than the other calls' timeout and the long client's
// wait for an answer, as transcribing does: ctx ends it. Its token lasts
// as long as a function may run.
func (c *Client) InvokeLong(ctx context.Context, name string, body map[string]any) (map[string]any, error) {
	return c.invoke(&http.Client{Transport: c.http.Transport}, ctx, longTokenLasts, name, body)
}

func (c *Client) invoke(hc *http.Client, ctx context.Context, lasting time.Duration, name string, body map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	res, err := c.requestWith(hc, ctx, lasting, "POST", "/functions/v1/"+name, bytes.NewReader(raw), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	out := map[string]any{}
	data, _ := io.ReadAll(res.Body)
	if len(bytes.TrimSpace(data)) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return out, nil
}

// streamIdle is how long a stream may go without a line before it is
// given up on: the agent function sends one at least every 15 seconds,
// so one quiet for longer is a connection gone half-open. Tests shorten
// it.
var streamIdle = time.Minute

// errStreamIdle is a stream that stopped coming.
var errStreamIdle = errors.New("the server stopped answering")

// InvokeStream calls an Edge Function answering with NDJSON and calls fn
// with each event. Its token lasts as long as a function may run.
func (c *Client) InvokeStream(ctx context.Context, name string, body map[string]any, fn func(map[string]any)) error {
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// No overall timeout: runs take a while. A stream that goes quiet
	// ends instead.
	idle := time.AfterFunc(streamIdle, func() { cancel(errStreamIdle) })
	defer idle.Stop()
	res, err := c.requestWith(c.long, ctx, longTokenLasts, "POST", "/functions/v1/"+name, bytes.NewReader(raw), nil)
	if err != nil {
		return quiet(ctx, err)
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		idle.Reset(streamIdle)
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err == nil {
			fn(ev)
		}
	}
	return quiet(ctx, sc.Err())
}

// quiet is err, or errStreamIdle when the stream ended for going quiet.
func quiet(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), errStreamIdle) {
		return errStreamIdle
	}
	return err
}

// Upload puts a file in a Storage bucket, taking as long as ctx lets it.
func (c *Client) Upload(ctx context.Context, bucket, path string, data io.Reader, contentType string) error {
	res, err := c.requestWith(c.long, ctx, tokenLasts, "POST", "/storage/v1/object/"+bucket+"/"+escapePath(path), data, map[string]string{
		"Content-Type": contentType, "x-upsert": "true",
	})
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

// Remove removes files of a Storage bucket. One the bucket does not have
// is no error: Storage leaves it out of what it removed.
func (c *Client) Remove(ctx context.Context, bucket string, paths ...string) error {
	raw, err := json.Marshal(map[string]any{"prefixes": paths})
	if err != nil {
		return err
	}
	res, err := c.request(ctx, "DELETE", "/storage/v1/object/"+bucket, bytes.NewReader(raw), nil)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

// Download opens a file of a Storage bucket to read as it comes, taking as
// long as ctx lets it: a file may be hundreds of megabytes, which are never
// all in memory. The caller closes it. One that ends before the length the
// server gave is io.ErrUnexpectedEOF as it is read, never a file cut short.
func (c *Client) Download(ctx context.Context, bucket, path string) (io.ReadCloser, error) {
	res, err := c.requestWith(c.long, ctx, tokenLasts, "GET", "/storage/v1/object/authenticated/"+bucket+"/"+escapePath(path), nil, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// retryAfter is the wait a response's Retry-After asks for, in seconds or
// until a time, 0 for none.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// IsBusy reports whether err is the server's trouble, not the request's:
// one it failed on (5xx), or asked to be left a while for (429). What is
// sent next would fare alike, so none of it is the request's to count. A
// 5xx with an error a trigger raised (class P0, as parent_not_found,
// P0002, which PostgREST answers 500) is the request's, which the next one
// does not meet. A statement that ran out of time may be either: it counts
// as the server's here, for the caller to tell (IsStatementTimeout).
func IsBusy(err error) bool {
	re, ok := errors.AsType[*Error](err)
	return ok && (re.Status == http.StatusTooManyRequests || re.Status >= 500 && !strings.HasPrefix(re.Code, "P0"))
}

// IsStatementTimeout reports whether err is the database ending a
// statement that ran out of time (57014), which PostgREST answers 500: a
// request too heavy, or a database in trouble.
func IsStatementTimeout(err error) bool {
	re, ok := errors.AsType[*Error](err)
	return ok && re.Code == "57014"
}

// gatewayTimeout is the status Cloudflare, in front of a hosted project,
// answers when the server takes too long.
const gatewayTimeout = 524

// IsTooSlow reports whether err is a request that took too long, by the
// client's own wait or the caller's, the database's (IsStatementTimeout),
// or that of a gateway on the way: asked for less, it may go.
func IsTooSlow(err error) bool {
	if re, ok := errors.AsType[*Error](err); ok {
		return IsStatementTimeout(err) || re.Status == http.StatusGatewayTimeout || re.Status == gatewayTimeout
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

// IsStillTranscribing reports whether err is the stt function saying the
// transcriber goes on with a recording past the call's deadline: the next
// call picks it up, at no further cost.
func IsStillTranscribing(err error) bool {
	re, ok := errors.AsType[*Error](err)
	return ok && re.Code == "still_transcribing"
}

// IsNetwork reports whether err means the server could not be reached:
// the connection failed, broke or timed out, a stream went quiet, or the
// gateway in front of the server answered that it could not reach it.
// Anything else, a refusal by the server, an answer that does not parse
// or a wait of the caller's own that ran out, is no network error, so a
// caller that counts these as being offline does not stop at it.
func IsNetwork(err error) bool {
	if err == nil {
		return false
	}
	if re, ok := errors.AsType[*Error](err); ok {
		return re.Status == http.StatusBadGateway || re.Status == http.StatusServiceUnavailable || re.Status == http.StatusGatewayTimeout
	}
	if errors.Is(err, errStreamIdle) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// A request that failed, its own timeout among the reasons, but not
	// one the caller's context ended: a wait of its own that ran out, or
	// that it called off, while the request went. The client's own timeout
	// is an error of its own kind, which errors.Is takes for
	// DeadlineExceeded too: only the caller's deadline is that error itself.
	if ue, ok := errors.AsType[*url.Error](err); ok {
		callers := ue.Err == context.DeadlineExceeded //nolint:errorlint // the error itself, not one that matches it.
		return !callers && !errors.Is(ue.Err, context.Canceled)
	}
	// A context's deadline is a net.Error too, but one that ran out outside
	// a request is the caller's, not the network's.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	// Reading a body after its connection broke.
	_, ok := errors.AsType[net.Error](err)
	return ok
}
