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
	URL     string
	AnonKey string
}

// Config reads the project from SUPABASE_URL and SUPABASE_ANON_KEY, or the
// build's defaults.
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
	cfg  Settings
	st   *store.Store
	http *http.Client

	mu   sync.Mutex
	sess *session
	// OnAuth runs after signing in or out.
	OnAuth func(user *store.User)
}

// New returns a client for a project.
func New(cfg Settings, st *store.Store) *Client {
	return &Client{cfg: cfg, st: st, http: &http.Client{Timeout: 90 * time.Second}}
}

// Configured reports whether the build has a server.
func (c *Client) Configured() bool { return c != nil && c.cfg.URL != "" && c.cfg.AnonKey != "" }

// URL is the project's URL.
func (c *Client) URL() string { return c.cfg.URL }

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

func (c *Client) keep(s *session) {
	c.mu.Lock()
	c.sess = s
	c.mu.Unlock()
	if s == nil {
		_ = keychain.Delete(secretKey)
		return
	}
	// Keep what fits in the keychain; the access token is fetched again.
	small := *s
	if raw, _ := json.Marshal(small); len(raw) > mygo.MaxSecretSize { // #nosec G117 -- the session goes to the keychain.
		small.AccessToken = ""
		small.ExpiresAt = 0
	}
	raw, _ := json.Marshal(small) // #nosec G117 -- the session goes to the keychain.
	_ = keychain.Set(secretKey, raw)
}

func (c *Client) authRequest(ctx context.Context, path string, body any, withToken bool) (map[string]any, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.URL+"/auth/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.cfg.AnonKey)
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

func (c *Client) fromToken(out map[string]any) (*store.User, error) {
	raw, _ := json.Marshal(out)
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if exp, ok := out["expires_in"].(float64); ok && s.ExpiresAt == 0 {
		s.ExpiresAt = time.Now().Unix() + int64(exp)
	}
	if s.AccessToken == "" || s.User == nil {
		return nil, errors.New("The server did not sign you in.") //nolint:staticcheck // shown to people as a sentence.
	}
	c.keep(&s)
	u := toUser(s.User)
	if c.OnAuth != nil {
		c.OnAuth(u)
	}
	return u, nil
}

// SignIn signs in with an email and password.
func (c *Client) SignIn(email, password string) (*store.User, error) {
	out, err := c.authRequest(context.Background(), "token?grant_type=password", map[string]any{"email": email, "password": password}, false)
	if err != nil {
		return nil, err
	}
	return c.fromToken(out)
}

// SignUp creates an account. The server may ask to confirm the email
// first, in which case no user is returned.
func (c *Client) SignUp(email, password, name string) (*store.User, error) {
	out, err := c.authRequest(context.Background(), "signup", map[string]any{
		"email": email, "password": password, "data": map[string]any{"display_name": name},
	}, false)
	if err != nil {
		return nil, err
	}
	if _, ok := out["access_token"]; !ok {
		return nil, nil
	}
	return c.fromToken(out)
}

// SendCode emails a sign-in link and code.
func (c *Client) SendCode(email string) error {
	_, err := c.authRequest(context.Background(), "otp", map[string]any{"email": email, "create_user": true}, false)
	return err
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

// token returns a fresh access token, refreshing it when it is about to
// expire.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return "", ErrSignedOut
	}
	if s.AccessToken != "" && time.Now().Unix() < s.ExpiresAt-60 {
		return s.AccessToken, nil
	}
	out, err := c.authRequest(ctx, "token?grant_type=refresh_token", map[string]any{"refresh_token": s.RefreshToken}, false)
	if err != nil {
		var re *Error
		if errors.As(err, &re) && re.Status >= 400 && re.Status < 500 {
			// The session is gone: signed out elsewhere or revoked.
			c.keep(nil)
			if c.OnAuth != nil {
				c.OnAuth(nil)
			}
			return "", ErrSignedOut
		}
		return "", err
	}
	if _, err := c.fromTokenQuiet(out); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess.AccessToken, nil
}

func (c *Client) fromTokenQuiet(out map[string]any) (*session, error) {
	raw, _ := json.Marshal(out)
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if exp, ok := out["expires_in"].(float64); ok && s.ExpiresAt == 0 {
		s.ExpiresAt = time.Now().Unix() + int64(exp)
	}
	if s.User == nil {
		c.mu.Lock()
		if c.sess != nil {
			s.User = c.sess.User
		}
		c.mu.Unlock()
	}
	c.keep(&s)
	return &s, nil
}

// UpdateUser changes the account's profile data.
func (c *Client) UpdateUser(data map[string]any) error {
	tok, err := c.token(context.Background())
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"data": data})
	req, _ := http.NewRequest("PUT", c.cfg.URL+"/auth/v1/user", bytes.NewReader(raw))
	req.Header.Set("apikey", c.cfg.AnonKey)
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
	return c.requestWith(c.http, ctx, method, path, body, headers)
}

func (c *Client) requestWith(hc *http.Client, ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.cfg.AnonKey)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer func() { _ = res.Body.Close() }()
		data, _ := io.ReadAll(res.Body)
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		return nil, &Error{Status: res.StatusCode, Message: message(m, string(data))}
	}
	return res, nil
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

// Eq, Gt and In build filters.
func Eq(col, v string) string { return col + "=eq." + v }
func Gt(col, v string) string { return col + "=gt." + v }
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
	raw, _ := json.Marshal(body)
	res, err := c.request(ctx, "POST", "/functions/v1/"+name, bytes.NewReader(raw), nil)
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

// InvokeStream calls an Edge Function answering with NDJSON and calls fn
// with each event.
func (c *Client) InvokeStream(ctx context.Context, name string, body map[string]any, fn func(map[string]any)) error {
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// No overall timeout: runs take a while.
	res, err := c.requestWith(&http.Client{}, ctx, "POST", "/functions/v1/"+name, bytes.NewReader(raw), nil)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err == nil {
			fn(ev)
		}
	}
	return sc.Err()
}

// Upload puts a file in a Storage bucket.
func (c *Client) Upload(ctx context.Context, bucket, path string, data io.Reader, contentType string) error {
	res, err := c.request(ctx, "POST", "/storage/v1/object/"+bucket+"/"+escapePath(path), data, map[string]string{
		"Content-Type": contentType, "x-upsert": "true",
	})
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

// Download reads a file of a Storage bucket.
func (c *Client) Download(ctx context.Context, bucket, path string) ([]byte, error) {
	res, err := c.request(ctx, "GET", "/storage/v1/object/authenticated/"+bucket+"/"+escapePath(path), nil, nil)
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
