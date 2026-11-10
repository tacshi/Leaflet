package supabaseapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"leaflet/internal/oauth"
	"leaflet/locales"
)

var ClientID = "a587f79b-48d7-41c1-b5ce-cbf87d11a491"

var Auth = "https://leaflet-auth.capyvibe.app"

const Port = 51741

var Base = "https://api.supabase.com"

func tokenURL() string {
	if Auth == "" {
		return Base + "/v1/oauth/token"
	}
	return Auth + "/token"
}

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

var ErrSignedOut error = errSignedOut{}

type errSignedOut struct{}

func (errSignedOut) Error() string { return locales.SupabaseSignedOut() }

type failed struct {
	msg string
	err error
}

func (f *failed) Error() string { return f.msg }

func (f *failed) Unwrap() error { return f.err }

type Client struct {
	mu        sync.Mutex
	refresh   string
	access    string
	expires   time.Time
	renewing  sync.Mutex
	OnRefresh func(refresh string)
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

func Resume(refresh string) *Client { return &Client{refresh: refresh} }

func (c *Client) RefreshToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refresh
}

func SignIn(ctx context.Context, open func(url string)) (*Client, error) {
	authURL, state, verifier := BeginSignIn()
	lb, err := oauth.ListenFor("Supabase", Port, false, state, verifier)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	open(authURL)
	back, err := lb.Wait(ctx)
	if err != nil {
		return nil, err
	}
	return FinishSignIn(ctx, back, state, verifier)
}

func redirectURI() string {
	if Auth == "" {
		return fmt.Sprintf("http://127.0.0.1:%d/auth/callback", Port)
	}
	return Auth + "/auth/supabase"
}

func BeginSignIn() (authURL, state, verifier string) {
	state, verifier = oauth.NewState(), oauth.NewVerifier()
	authURL = Base + "/v1/oauth/authorize?" + url.Values{
		"client_id":             {ClientID},
		"redirect_uri":          {redirectURI()},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {oauth.Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}.Encode()
	return authURL, state, verifier
}

func FinishSignIn(ctx context.Context, answer url.Values, state, verifier string) (*Client, error) {
	if subtle.ConstantTimeCompare([]byte(answer.Get("state")), []byte(state)) != 1 {
		return nil, errors.New(locales.SupabaseSignInStale())
	}
	if e := answer.Get("error"); e != "" {
		if e == "access_denied" {
			return nil, errors.New(locales.SupabaseSignInCancelled())
		}
		return nil, errors.New(locales.SupabaseSignInFailed(e))
	}
	c := &Client{}
	err := c.exchange(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {answer.Get("code")},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI()},
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) exchange(ctx context.Context, form url.Values) error {
	form.Set("client_id", ClientID)
	req, _ := http.NewRequestWithContext(ctx, "POST", tokenURL(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := send(httpClient, req, &t); err != nil {
		var e *Error
		if errors.As(err, &e) && form.Get("grant_type") == "refresh_token" && e.Status == http.StatusBadRequest {
			return ErrSignedOut
		}
		return &failed{locales.SupabaseSignInFailed(err.Error()), err}
	}
	if t.AccessToken == "" {
		return errors.New(locales.SupabaseNoSignIn())
	}
	c.mu.Lock()
	c.access, c.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	if t.RefreshToken != "" {
		c.refresh = t.RefreshToken
	}
	refresh := c.refresh
	c.mu.Unlock()
	if t.RefreshToken != "" && c.OnRefresh != nil {
		c.OnRefresh(refresh)
	}
	return nil
}

func (c *Client) fresh() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.access, c.access != "" && time.Now().Before(c.expires.Add(-time.Minute))
}

func (c *Client) token(ctx context.Context) (string, error) {
	if tok, ok := c.fresh(); ok {
		return tok, nil
	}
	c.renewing.Lock()
	defer c.renewing.Unlock()
	if tok, ok := c.fresh(); ok {
		return tok, nil
	}
	refresh := c.RefreshToken()
	if refresh == "" {
		return "", ErrSignedOut
	}
	if err := c.exchange(context.WithoutCancel(ctx), url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); err != nil {
		return "", err
	}
	tok, _ := c.fresh()
	return tok, nil
}

func send(hc *http.Client, req *http.Request, out any) error {
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return &Error{Status: res.StatusCode, Message: message(data, res.Status)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func message(data []byte, fallback string) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		for _, k := range []string{"message", "error_description", "msg", "error"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return fallback
}

func (c *Client) call(ctx context.Context, method, path string, body any, ctype string, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case *bytes.Buffer:
		r = b
	default:
		raw, _ := json.Marshal(b)
		r, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, Base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return send(httpClient, req, out)
}

type Organization struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type Project struct {
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	OrganizationSlug string `json:"organization_slug"`
	Region           string `json:"region"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

func (p Project) Healthy() bool { return p.Status == "ACTIVE_HEALTHY" }

func (p Project) URL() string { return "https://" + p.Ref + ".supabase.co" }

type Member struct {
	Name  string `json:"user_name"`
	Email string `json:"email"`
	Role  string `json:"role_name"`
}

func (c *Client) Members(ctx context.Context, org string) ([]Member, error) {
	var out []Member
	return out, c.call(ctx, "GET", "/v1/organizations/"+url.PathEscape(org)+"/members", nil, "", &out)
}

func (c *Client) Organizations(ctx context.Context) ([]Organization, error) {
	var out []Organization
	return out, c.call(ctx, "GET", "/v1/organizations", nil, "", &out)
}

func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	return out, c.call(ctx, "GET", "/v1/projects", nil, "", &out)
}

func (c *Client) Project(ctx context.Context, ref string) (Project, error) {
	var p Project
	return p, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref), nil, "", &p)
}

func (c *Client) CreateProject(ctx context.Context, name, org, regionGroup string) (Project, error) {
	var p Project
	err := c.call(ctx, "POST", "/v1/projects", map[string]any{
		"name": name, "organization_slug": org, "db_pass": oauth.RandomString(24),
		"region_selection": map[string]string{"type": "smartGroup", "code": regionGroup},
	}, "", &p)
	return p, err
}

func (c *Client) Restore(ctx context.Context, ref string) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/restore", nil, "", nil)
}

type APIKey struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	APIKey string `json:"api_key"`
}

func (c *Client) APIKeys(ctx context.Context, ref string) ([]APIKey, error) {
	var out []APIKey
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/api-keys?reveal=true", nil, "", &out)
}

type Migration struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

func (c *Client) Migrations(ctx context.Context, ref string) ([]Migration, error) {
	var out []Migration
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/database/migrations", nil, "", &out)
}

func (c *Client) Query(ctx context.Context, ref, sql string) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/database/query", map[string]any{"query": sql}, "", nil)
}

type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c *Client) Secrets(ctx context.Context, ref string) ([]Secret, error) {
	var out []Secret
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/secrets", nil, "", &out)
}

func (c *Client) SetSecrets(ctx context.Context, ref string, secrets []Secret) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/secrets", secrets, "", nil)
}

func (c *Client) DeleteSecrets(ctx context.Context, ref string, names []string) error {
	return c.call(ctx, "DELETE", "/v1/projects/"+url.PathEscape(ref)+"/secrets", names, "", nil)
}

type File struct {
	Path string
	Data []byte
}

func (c *Client) DeployFunction(ctx context.Context, ref, slug, entrypoint string, verifyJWT bool, files []File) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	meta, _ := json.Marshal(map[string]any{"name": slug, "entrypoint_path": entrypoint, "verify_jwt": verifyJWT})
	if err := w.WriteField("metadata", string(meta)); err != nil {
		return err
	}
	for _, f := range files {
		part, err := w.CreateFormFile("file", f.Path)
		if err != nil {
			return err
		}
		if _, err := part.Write(f.Data); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/functions/deploy?slug="+url.QueryEscape(slug), &body, w.FormDataContentType(), nil)
}

func (c *Client) SetUploadLimit(ctx context.Context, ref string, limit int64) error {
	return c.call(ctx, "PATCH", "/v1/projects/"+url.PathEscape(ref)+"/config/storage", map[string]any{"fileSizeLimit": limit}, "", nil)
}

type SignUps struct {
	Disabled    bool `json:"disable_signup"`
	AutoConfirm bool `json:"mailer_autoconfirm"`
}

func (c *Client) SignUps(ctx context.Context, ref string) (SignUps, error) {
	var s SignUps
	return s, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/config/auth", nil, "", &s)
}

func (c *Client) SetSignUps(ctx context.Context, ref string, s SignUps) error {
	return c.call(ctx, "PATCH", "/v1/projects/"+url.PathEscape(ref)+"/config/auth", s, "", nil)
}
