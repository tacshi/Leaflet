package remote

import (
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

var (
	DefaultURL            = ""
	DefaultPublishableKey = ""
)

type Settings struct {
	URL            string `json:"url"`
	PublishableKey string `json:"anon_key"`
	Ref            string `json:"ref,omitempty"`
	Name           string `json:"name,omitempty"`
}

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

const backendKey = "backend"

type Error struct {
	Status     int
	Message    string
	Code       string
	Name       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }

var ErrSignedOut error = &sentence{locales.RemoteSignedOut}

var ErrNotConfigured error = &sentence{locales.RemoteNotConfigured}

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

type Client struct {
	cfg   Settings
	fixed bool
	st    *store.Store
	http  *http.Client
	long  *http.Client

	mu         sync.Mutex
	sess       *session
	refused    string
	refreshing *refresh
	sessions   uint64
	keepMu     sync.Mutex
	noKeychain bool
}

type refresh struct {
	from  *session
	done  chan struct{}
	tok   string
	err   error
	stale bool
}

func New(cfg Settings, st *store.Store) *Client {
	c := &Client{cfg: cfg, fixed: cfg.URL != "", st: st, http: &http.Client{Timeout: 90 * time.Second}, long: newLongClient()}
	if !c.fixed && st != nil {
		if raw, ok := st.Value(backendKey); ok {
			_ = json.Unmarshal([]byte(raw), &c.cfg)
		}
	}
	return c
}

func (c *Client) settings() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

func (c *Client) Fixed() bool { return c != nil && c.fixed }

func (c *Client) Project() Settings { return c.settings() }

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

var longHeaderWait = time.Minute

func newLongClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = longHeaderWait
	t.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: t}
}

func (c *Client) Configured() bool {
	if c == nil {
		return false
	}
	cfg := c.settings()
	return cfg.URL != "" && cfg.PublishableKey != ""
}

func (c *Client) URL() string { return c.settings().URL }

func (c *Client) SignedIn() bool {
	if !c.Configured() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

func (c *Client) User() *store.User {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil || c.sess.User == nil {
		return nil
	}
	return toUser(c.sess.User)
}

func (c *Client) Sessions() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions
}

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

func (c *Client) keep(s *session) { c.replace(nil, s, true) }

func (c *Client) replace(old, s *session, force bool) bool {
	c.keepMu.Lock()
	defer c.keepMu.Unlock()
	c.mu.Lock()
	if !force && c.sess != old {
		c.mu.Unlock()
		return false
	}
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
	small := *s
	if raw, _ := json.Marshal(small); len(raw) > keychain.MaxSecretSize { // #nosec G117
		small.AccessToken = ""
		small.ExpiresAt = 0
	}
	raw, _ := json.Marshal(small) // #nosec G117
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

func decodeSession(out map[string]any) (*session, error) {
	raw, _ := json.Marshal(out)
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
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

func (c *Client) SendCode(email string) error {
	_, err := c.authRequest(context.Background(), "otp", map[string]any{"email": email, "create_user": true}, false)
	return err
}

const ownerFlag = "leaflet_owner"

func (c *Client) ProjectOwner(ctx context.Context, project Settings, secret string) (string, error) {
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

var usersPerPage, maxUserPages = 1000, 1000

type Pending struct {
	project Settings
	s       *session
}

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

func newPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c *Client) Adopt(p *Pending) (*store.User, error) {
	if err := c.Use(p.project); err != nil {
		return nil, err
	}
	c.keep(p.s)
	return toUser(p.s.User), nil
}

const logoutHere = "logout?scope=local"

const logoutOthers = "logout?scope=others"

func (c *Client) Discard(p *Pending) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.authRequestTo(ctx, p.project, logoutHere, map[string]any{}, p.s.AccessToken)
}

func accountExists(out map[string]any) bool {
	code, _ := out["error_code"].(string)
	if code == "" {
		code, _ = out["code"].(string)
	}
	return code == "email_exists" || code == "user_already_exists"
}

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

func (c *Client) VerifyCode(email, code string) (*store.User, error) {
	out, err := c.authRequest(context.Background(), "verify", map[string]any{"type": "email", "email": email, "token": strings.TrimSpace(code)}, false)
	if err != nil {
		return nil, err
	}
	return c.fromToken(out)
}

func (c *Client) SignOut() {
	if c.SignedIn() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = c.authRequest(ctx, logoutHere, map[string]any{}, true)
		cancel()
	}
	c.keep(nil)
	c.signedOut()
}

func (c *Client) signedOut() {
	if c.st == nil || c.SignedIn() {
		return
	}
	if _, err := c.st.LoadSession(nil); err != nil {
		log.Printf("sign out: %v", err)
	}
}

const tokenLasts = time.Minute

func (c *Client) token(ctx context.Context) (string, error) {
	for {
		c.mu.Lock()
		s := c.sess
		if s == nil {
			c.mu.Unlock()
			return "", ErrSignedOut
		}
		if s.AccessToken != "" && s.AccessToken != c.refused && time.Now().Add(tokenLasts).Unix() < s.ExpiresAt {
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
		if !r.stale {
			return r.tok, r.err
		}
	}
}

func (c *Client) refresh(r *refresh) {
	defer close(r.done)
	defer func() {
		c.mu.Lock()
		if c.refreshing == r {
			c.refreshing = nil
		}
		c.mu.Unlock()
	}()
	defer guard.RecoverAs(&r.err)
	out, err := c.authRequest(context.Background(), "token?grant_type=refresh_token", map[string]any{"refresh_token": r.from.RefreshToken}, false)
	var s *session
	if err == nil {
		s, err = decodeSession(out)
	}
	if err != nil {
		var re *Error
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

func (c *Client) request(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	return c.requestWith(c.http, ctx, method, path, body, headers)
}

func (c *Client) requestWith(hc *http.Client, ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
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
	again := body == nil || req.GetBody != nil
	for {
		tok, err := c.token(ctx)
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

func (c *Client) refuse(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil && c.sess.AccessToken == tok {
		c.refused = tok
	}
}

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

type Query struct {
	Select  string
	Filters []string
	Order   string
	Limit   int
}

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

func (c *Client) SelectOne(ctx context.Context, table string, q Query) (map[string]any, error) {
	q.Limit = 1
	rows, err := c.Select(ctx, table, q)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (c *Client) Upsert(ctx context.Context, table string, rows any, onConflict string) ([]map[string]any, error) {
	return c.UpsertReturning(ctx, table, rows, onConflict, "")
}

func (c *Client) UpsertReturning(ctx context.Context, table string, rows any, onConflict, sel string) ([]map[string]any, error) {
	var q []string
	if onConflict != "" {
		q = append(q, "on_conflict="+url.QueryEscape(onConflict))
	}
	return c.write(ctx, "POST", table, q, rows, "resolution=merge-duplicates,return=representation", sel)
}

func (c *Client) Update(ctx context.Context, table string, filters []string, values map[string]any) ([]map[string]any, error) {
	return c.UpdateReturning(ctx, table, filters, values, "")
}

func (c *Client) UpdateReturning(ctx context.Context, table string, filters []string, values map[string]any, sel string) ([]map[string]any, error) {
	q := make([]string, len(filters))
	for i, f := range filters {
		k, v, _ := strings.Cut(f, "=")
		q[i] = url.QueryEscape(k) + "=" + url.QueryEscape(v)
	}
	return c.write(ctx, "PATCH", table, q, values, "return=representation", sel)
}

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

func IsConflict(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusConflict
}

func IsInvalid(err error) bool {
	var re *Error
	return errors.As(err, &re) && (re.Code == "23514" || re.Code == "413" || re.Status == http.StatusRequestEntityTooLarge || re.Name == invalidKey)
}

const invalidKey = "InvalidKey"

func IsInvalidKey(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Name == invalidKey
}

func IsForbidden(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusForbidden
}

func IsNotFound(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Status == http.StatusNotFound
}

func (c *Client) Insert(ctx context.Context, table string, rows any) ([]map[string]any, error) {
	return c.InsertReturning(ctx, table, rows, "")
}

func (c *Client) InsertReturning(ctx context.Context, table string, rows any, sel string) ([]map[string]any, error) {
	return c.write(ctx, "POST", table, nil, rows, "return=representation", sel)
}

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
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return out, nil
}

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

func (c *Client) Upload(ctx context.Context, bucket, path string, data io.Reader, contentType string) error {
	res, err := c.requestWith(c.long, ctx, "POST", "/storage/v1/object/"+bucket+"/"+escapePath(path), data, map[string]string{
		"Content-Type": contentType, "x-upsert": "true",
	})
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}

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

func (c *Client) Download(ctx context.Context, bucket, path string) (io.ReadCloser, error) {
	res, err := c.requestWith(c.long, ctx, "GET", "/storage/v1/object/authenticated/"+bucket+"/"+escapePath(path), nil, nil)
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

func IsBusy(err error) bool {
	re, ok := errors.AsType[*Error](err)
	return ok && (re.Status == http.StatusTooManyRequests || re.Status >= 500 && !strings.HasPrefix(re.Code, "P0"))
}

func IsStatementTimeout(err error) bool {
	re, ok := errors.AsType[*Error](err)
	return ok && re.Code == "57014"
}

const gatewayTimeout = 524

func IsTooSlow(err error) bool {
	if re, ok := errors.AsType[*Error](err); ok {
		return IsStatementTimeout(err) || re.Status == http.StatusGatewayTimeout || re.Status == gatewayTimeout
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

func IsNetwork(err error) bool {
	if err == nil {
		return false
	}
	if re, ok := errors.AsType[*Error](err); ok {
		return re.Status == http.StatusBadGateway || re.Status == http.StatusServiceUnavailable || re.Status == http.StatusGatewayTimeout
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		callers := ue.Err == context.DeadlineExceeded //nolint:errorlint
		return !callers && !errors.Is(ue.Err, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	_, ok := errors.AsType[net.Error](err)
	return ok
}
