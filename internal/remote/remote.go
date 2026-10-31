// Package remote talks to Supabase over HTTP: Auth (GoTrue), the tables
// (PostgREST), Storage and the Edge Functions. Without a configured URL and
// key the app runs on this device only.
package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/keychain"
	"leaflet/internal/store"
)

// Build-time defaults, set with -ldflags "-X leaflet/internal/remote.DefaultURL=…".
var (
	DefaultURL     = ""
	DefaultAnonKey = ""
)

// Settings say which Supabase project to use.
type Settings struct {
	URL     string `json:"url"`
	AnonKey string `json:"anon_key"`
	// Ref and Name are the project's, when the person chose it with their
	// Supabase account; a server the build or environment fixes has none.
	Ref  string `json:"ref,omitempty"`
	Name string `json:"name,omitempty"`
}

// Config reads the project from SUPABASE_URL and SUPABASE_ANON_KEY, or the
// build's defaults. Without them, New uses the project kept on this
// device.
func Config() Settings {
	s := Settings{URL: DefaultURL, AnonKey: DefaultAnonKey}
	if v := os.Getenv("SUPABASE_URL"); v != "" {
		s.URL = v
	}
	if v := os.Getenv("SUPABASE_ANON_KEY"); v != "" {
		s.AnonKey = v
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
}

func (e *Error) Error() string { return e.Message }

// ErrSignedOut is returned by calls that need an account.
var ErrSignedOut = errors.New("Sign in to use this.") //nolint:staticcheck // shown to people as a sentence.

// ErrNotConfigured is returned when the build has no server.
var ErrNotConfigured = errors.New("This build has no server; it works on this device only.") //nolint:staticcheck // shown to people as a sentence.

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
	// keepMu has the keychain written in the order sess changes.
	keepMu sync.Mutex
	// noKeychain keeps the session in memory only, for tests.
	noKeychain bool
	// OnAuth runs after signing in or out.
	OnAuth func(user *store.User)
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
		return errors.New("This build's server is fixed.") //nolint:staticcheck // shown to people as a sentence.
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
	return cfg.URL != "" && cfg.AnonKey != ""
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
	if raw, _ := json.Marshal(small); len(raw) > mygo.MaxSecretSize { // #nosec G117 -- the session goes to the keychain.
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
	cfg := c.settings()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL+"/auth/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", cfg.AnonKey)
	req.Header.Set("Content-Type", "application/json")
	if withToken {
		tok, err := c.token(ctx)
		if err != nil {
			return nil, err
		}
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
		return out, &Error{Status: res.StatusCode, Message: message(out, string(data))}
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

var errNoSession = errors.New("The server did not sign you in.") //nolint:staticcheck // shown to people as a sentence.

func (c *Client) fromToken(out map[string]any) (*store.User, error) {
	s, err := decodeSession(out)
	if err != nil {
		return nil, err
	}
	if s.User == nil {
		return nil, errNoSession
	}
	c.keep(s)
	u := toUser(s.User)
	if c.OnAuth != nil {
		c.OnAuth(u)
	}
	return u, nil
}

// SendCode emails a sign-in link and code.
func (c *Client) SendCode(email string) error {
	_, err := c.authRequest(context.Background(), "otp", map[string]any{"email": email, "create_user": true}, false)
	return err
}

// ownerFlag marks, in app_metadata, the account SignInAsOwner made: the
// person who owns the project, whose Supabase account can't tell the API
// its email.
const ownerFlag = "leaflet_owner"

// ProjectOwner returns the email and name of the project's owner, the
// account SignInAsOwner made, or "" when there is none yet. secret is the
// project's secret key, which is not kept.
func (c *Client) ProjectOwner(ctx context.Context, secret string) (email, name string, err error) {
	out, err := c.adminRequest(ctx, secret, "GET", "admin/users?per_page=1000", nil)
	if err != nil {
		return "", "", err
	}
	raw, _ := json.Marshal(out["users"])
	var users []struct {
		authUser
		AppMetadata map[string]any `json:"app_metadata"`
	}
	_ = json.Unmarshal(raw, &users)
	for _, u := range users {
		if u.AppMetadata[ownerFlag] == true {
			return u.Email, toUser(&u.authUser).DisplayName, nil
		}
	}
	return "", "", nil
}

// SignInAsOwner signs in to the project as the person who owns it, by
// email. secret is the project's secret key: it makes their account when
// there is none, marked as the owner's, then a sign-in link used at once,
// and is not kept.
func (c *Client) SignInAsOwner(ctx context.Context, secret, email, name string) (*store.User, error) {
	owner := map[string]any{ownerFlag: true}
	out, err := c.adminRequest(ctx, secret, "POST", "admin/users", map[string]any{
		"email": email, "email_confirm": true, "user_metadata": map[string]any{"display_name": name}, "app_metadata": owner,
	})
	made := err == nil
	if err != nil && !accountExists(out) {
		return nil, err
	}
	link, err := c.adminRequest(ctx, secret, "POST", "admin/generate_link", map[string]any{"type": "magiclink", "email": email})
	if err != nil {
		return nil, err
	}
	if id, _ := link["id"].(string); !made && id != "" {
		// An account made before it was marked, as by signing up.
		if _, err := c.adminRequest(ctx, secret, "PUT", "admin/users/"+url.PathEscape(id), map[string]any{"app_metadata": owner}); err != nil {
			return nil, err
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
	session, err := c.authRequest(ctx, "verify", map[string]any{"type": kind, "token_hash": hash}, false)
	if err != nil {
		return nil, err
	}
	return c.fromToken(session)
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

// adminRequest makes a request of Auth's admin API with the project's
// secret key.
func (c *Client) adminRequest(ctx context.Context, secret, method, path string, body any) (map[string]any, error) {
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.settings().URL+"/auth/v1/"+path, r)
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
		return out, &Error{Status: res.StatusCode, Message: message(out, string(data))}
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

// SignOut signs out on this device.
func (c *Client) SignOut() {
	if c.SignedIn() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = c.authRequest(ctx, "logout", map[string]any{}, true)
		cancel()
	}
	c.keep(nil)
	if c.OnAuth != nil {
		c.OnAuth(nil)
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
			if c.OnAuth != nil {
				c.OnAuth(nil)
			}
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

// UpdateUser changes the account's profile data.
func (c *Client) UpdateUser(data map[string]any) error {
	tok, err := c.token(context.Background())
	if err != nil {
		return err
	}
	cfg := c.settings()
	raw, _ := json.Marshal(map[string]any{"data": data})
	req, _ := http.NewRequest("PUT", cfg.URL+"/auth/v1/user", bytes.NewReader(raw))
	req.Header.Set("apikey", cfg.AnonKey)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode >= 400 {
		return &Error{Status: res.StatusCode, Message: "Could not update the profile"}
	}
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
	req.Header.Set("apikey", cfg.AnonKey)
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
		return nil, &Error{Status: res.StatusCode, Message: message(m, string(data))}
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
	raw, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	path := "/rest/v1/" + table
	if onConflict != "" {
		path += "?on_conflict=" + url.QueryEscape(onConflict)
	}
	res, err := c.request(ctx, "POST", path, bytes.NewReader(raw), map[string]string{
		"Prefer": "resolution=merge-duplicates,return=representation",
	})
	if err != nil {
		return nil, err
	}
	return decodeRows(res)
}

// Update changes the rows that match the filters and returns them as
// stored; none when nothing matched.
func (c *Client) Update(ctx context.Context, table string, filters []string, values map[string]any) ([]map[string]any, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	q := make([]string, len(filters))
	for i, f := range filters {
		k, v, _ := strings.Cut(f, "=")
		q[i] = url.QueryEscape(k) + "=" + url.QueryEscape(v)
	}
	res, err := c.request(ctx, "PATCH", "/rest/v1/"+table+"?"+strings.Join(q, "&"), bytes.NewReader(raw), map[string]string{"Prefer": "return=representation"})
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
	raw, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "POST", "/rest/v1/"+table, bytes.NewReader(raw), map[string]string{"Prefer": "return=representation"})
	if err != nil {
		return nil, err
	}
	return decodeRows(res)
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
	data, _ := io.ReadAll(res.Body)
	if len(bytes.TrimSpace(data)) > 0 {
		_ = json.Unmarshal(data, &out)
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
	if err != nil && context.Cause(ctx) == errStreamIdle {
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

// Download reads a file of a Storage bucket, taking as long as ctx lets
// it.
func (c *Client) Download(ctx context.Context, bucket, path string) ([]byte, error) {
	res, err := c.requestWith(c.long, ctx, tokenLasts, "GET", "/storage/v1/object/authenticated/"+bucket+"/"+escapePath(path), nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return io.ReadAll(res.Body)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// IsNetwork reports whether err means the server could not be reached.
func IsNetwork(err error) bool {
	if err == nil {
		return false
	}
	var re *Error
	if errors.As(err, &re) {
		return false
	}
	return !errors.Is(err, ErrSignedOut) && !errors.Is(err, ErrNotConfigured)
}
