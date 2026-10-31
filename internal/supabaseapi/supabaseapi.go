// Package supabaseapi is the Supabase Management API, signed in to with
// the person's own Supabase account: it finds, creates and sets up the
// project Leaflet syncs through.
package supabaseapi

import (
	"bytes"
	"context"
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
)

// ClientID is the OAuth app Leaflet signs in as, registered in a Supabase
// organization's settings (OAuth Apps). It is public: every sign-in link
// carries it.
var ClientID = "a587f79b-48d7-41c1-b5ce-cbf87d11a491"

// Auth is Leaflet's sign-in Worker (worker/), which holds the OAuth app's
// client secret so the app ships none. Its /auth/supabase is the OAuth
// app's redirect URL: Supabase takes only https ones, which a desktop app
// can't answer, so the page sends the browser on to Leaflet waiting on
// Port. Its /token exchanges codes and refresh tokens with the secret
// added. Tests set it to "", for the loopback and the API itself.
var Auth = "https://leaflet-auth.capyvibe.app"

// Port is the loopback port the redirect page sends the browser on to:
// http://127.0.0.1:51741/auth/callback.
const Port = 51741

// Base is the API's address; tests point it elsewhere.
var Base = "https://api.supabase.com"

// tokenURL is where codes and refresh tokens are exchanged.
func tokenURL() string {
	if Auth == "" {
		return Base + "/v1/oauth/token"
	}
	return Auth + "/token"
}

// Error is a refused request, with the API's message.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrSignedOut is a sign-in that ended: revoked, or its refresh token
// expired.
var ErrSignedOut = errors.New("Your Supabase sign-in ended. Sign in with Supabase again.") //nolint:staticcheck // shown to people as a sentence.

// Client is a signed-in connection to the Management API.
type Client struct {
	mu      sync.Mutex
	refresh string
	access  string
	expires time.Time
	// renewing is held while the access token renews, so the refresh
	// token, which rotates, is spent once.
	renewing sync.Mutex
	// OnRefresh keeps the refresh token as it rotates.
	OnRefresh func(refresh string)
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

// Resume returns a client for a sign-in kept from before.
func Resume(refresh string) *Client { return &Client{refresh: refresh} }

// RefreshToken is what to keep of the sign-in.
func (c *Client) RefreshToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refresh
}

// SignIn signs in through the browser, which open opens.
func SignIn(ctx context.Context, open func(url string)) (*Client, error) {
	id := ClientID
	lb, err := oauth.Listen("Supabase", Port, false)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	redirect := lb.Redirect
	if Auth != "" {
		redirect = Auth + "/auth/supabase"
	}
	open(Base + "/v1/oauth/authorize?" + url.Values{
		"client_id":             {id},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"state":                 {lb.State},
		"code_challenge":        {lb.Challenge()},
		"code_challenge_method": {"S256"},
	}.Encode())
	back, err := lb.Wait(ctx)
	if err != nil {
		return nil, err
	}
	c := &Client{}
	err = c.exchange(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {back.Get("code")},
		"code_verifier": {lb.Verifier}, "redirect_uri": {redirect},
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// exchange posts form to the token endpoint and keeps the tokens.
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
		// Only a refresh token refused is a sign-in that ended: OAuth
		// answers 400 for it (invalid_grant). A refused client, as when
		// the Worker's secret is wrong, is 401 or 403, and keeps the
		// sign-in for when it is put right.
		var e *Error
		if errors.As(err, &e) && form.Get("grant_type") == "refresh_token" && e.Status == http.StatusBadRequest {
			return ErrSignedOut
		}
		return fmt.Errorf("Supabase sign-in failed: %w", err) //nolint:staticcheck // shown to people as a sentence.
	}
	if t.AccessToken == "" {
		return errors.New("Supabase did not sign you in.") //nolint:staticcheck // shown to people as a sentence.
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
	// Not the caller's context: one stopped while the refresh token
	// rotates would lose the new one.
	if err := c.exchange(context.WithoutCancel(ctx), url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); err != nil {
		return "", err
	}
	tok, _ := c.fresh()
	return tok, nil
}

// send makes req and decodes a JSON answer into out, when out is not nil.
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

// call makes a request of the API with a JSON body, or a body of the
// given content type when body is a *bytes.Buffer and ctype is set.
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

// Organization is one the account belongs to.
type Organization struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Project is a Supabase project.
type Project struct {
	Ref              string `json:"ref"`
	Name             string `json:"name"`
	OrganizationSlug string `json:"organization_slug"`
	Region           string `json:"region"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

// Healthy reports whether the project is up.
func (p Project) Healthy() bool { return p.Status == "ACTIVE_HEALTHY" }

// URL is the project's API address.
func (p Project) URL() string { return "https://" + p.Ref + ".supabase.co" }

// Member is a member of an organization.
type Member struct {
	Name  string `json:"user_name"`
	Email string `json:"email"`
	Role  string `json:"role_name"`
}

// Members lists an organization's members. The API tells no account its
// own email, so this is where it is found.
func (c *Client) Members(ctx context.Context, org string) ([]Member, error) {
	var out []Member
	return out, c.call(ctx, "GET", "/v1/organizations/"+url.PathEscape(org)+"/members", nil, "", &out)
}

// Organizations lists the account's organizations.
func (c *Client) Organizations(ctx context.Context) ([]Organization, error) {
	var out []Organization
	return out, c.call(ctx, "GET", "/v1/organizations", nil, "", &out)
}

// Projects lists the account's projects.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	return out, c.call(ctx, "GET", "/v1/projects", nil, "", &out)
}

// Project returns one project.
func (c *Client) Project(ctx context.Context, ref string) (Project, error) {
	var p Project
	return p, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref), nil, "", &p)
}

// CreateProject creates a project in an organization, in a region group
// ("americas", "emea" or "apac"), with a database password that is not
// kept.
func (c *Client) CreateProject(ctx context.Context, name, org, regionGroup string) (Project, error) {
	var p Project
	err := c.call(ctx, "POST", "/v1/projects", map[string]any{
		"name": name, "organization_slug": org, "db_pass": oauth.RandomString(24),
		"region_selection": map[string]string{"type": "smartGroup", "code": regionGroup},
	}, "", &p)
	return p, err
}

// Restore starts a paused project again.
func (c *Client) Restore(ctx context.Context, ref string) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/restore", nil, "", nil)
}

// APIKey is one of a project's keys.
type APIKey struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	APIKey string `json:"api_key"`
}

// APIKeys returns a project's keys, revealed.
func (c *Client) APIKeys(ctx context.Context, ref string) ([]APIKey, error) {
	var out []APIKey
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/api-keys?reveal=true", nil, "", &out)
}

// Migration is one applied to a project's database.
type Migration struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

// Migrations lists the migrations applied to a project.
func (c *Client) Migrations(ctx context.Context, ref string) ([]Migration, error) {
	var out []Migration
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/database/migrations", nil, "", &out)
}

// Query runs SQL on a project's database.
func (c *Client) Query(ctx context.Context, ref, sql string) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/database/query", map[string]any{"query": sql}, "", nil)
}

// Secret is one of a project's Edge Function secrets.
type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Secrets lists a project's secrets; the values are digests.
func (c *Client) Secrets(ctx context.Context, ref string) ([]Secret, error) {
	var out []Secret
	return out, c.call(ctx, "GET", "/v1/projects/"+url.PathEscape(ref)+"/secrets", nil, "", &out)
}

// SetSecrets sets a project's secrets.
func (c *Client) SetSecrets(ctx context.Context, ref string, secrets []Secret) error {
	return c.call(ctx, "POST", "/v1/projects/"+url.PathEscape(ref)+"/secrets", secrets, "", nil)
}

// DeleteSecrets deletes a project's secrets by name.
func (c *Client) DeleteSecrets(ctx context.Context, ref string, names []string) error {
	return c.call(ctx, "DELETE", "/v1/projects/"+url.PathEscape(ref)+"/secrets", names, "", nil)
}

// File is a source file of a function, at its path from the project's
// root, such as supabase/functions/agent/index.ts.
type File struct {
	Path string
	Data []byte
}

// DeployFunction deploys a function from its source files.
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

// SetUploadLimit sets a project's largest upload, in bytes.
func (c *Client) SetUploadLimit(ctx context.Context, ref string, limit int64) error {
	return c.call(ctx, "PATCH", "/v1/projects/"+url.PathEscape(ref)+"/config/storage", map[string]any{"fileSizeLimit": limit}, "", nil)
}
