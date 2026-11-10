package supabaseapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
)

func fakeAPI(t *testing.T, routes http.HandlerFunc) *httptest.Server {
	t.Helper()
	var challenge string
	issued := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/authorize":
			challenge = r.URL.Query().Get("code_challenge")
		case "/v1/oauth/token", "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "leaflet-app" {
				http.Error(w, `{"message":"unknown client"}`, http.StatusUnauthorized)
				return
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
					http.Error(w, `{"message":"bad code"}`, http.StatusBadRequest)
					return
				}
			case "refresh_token":
				if r.Form.Get("refresh_token") != fmt.Sprintf("refresh-%d", issued) {
					http.Error(w, `{"message":"revoked"}`, http.StatusBadRequest)
					return
				}
			}
			issued++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("access-%d", issued), "refresh_token": fmt.Sprintf("refresh-%d", issued),
				"expires_in": 3600, "token_type": "Bearer",
			})
		default:
			routes(w, r)
		}
	}))
	old, auth, id := Base, Auth, ClientID
	Base, Auth, ClientID = srv.URL, "", "leaflet-app"
	t.Cleanup(func() { Base, Auth, ClientID = old, auth, id; srv.Close() })
	return srv
}

func browser(t *testing.T, ctx context.Context) func(string) {
	return func(link string) {
		u, err := url.Parse(link)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		back := q.Get("redirect_uri")
		if Auth != "" {
			if back != Auth+"/auth/supabase" {
				t.Errorf("redirect %s", back)
			}
			back = fmt.Sprintf("http://127.0.0.1:%d/auth/callback", Port)
		}
		for _, get := range []string{link, back + "?code=the-code&state=" + url.QueryEscape(q.Get("state"))} {
			req, _ := http.NewRequestWithContext(ctx, "GET", get, nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			_ = res.Body.Close()
		}
	}
}

func TestSignInWithPKCE(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-1" {
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `[{"user_name":"me","email":"me@example.com","role_name":"Owner"}]`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := SignIn(ctx, browser(t, ctx))
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken() != "refresh-1" {
		t.Errorf("kept %q", c.RefreshToken())
	}
	m, err := c.Members(ctx, "me")
	if err != nil || len(m) != 1 || m[0].Email != "me@example.com" {
		t.Errorf("members %+v, %v", m, err)
	}
}

func TestRefreshRotatesAndEnds(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	ctx := context.Background()
	c := Resume("refresh-0")
	var kept []string
	c.OnRefresh = func(r string) { kept = append(kept, r) }
	if _, err := c.Organizations(ctx); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0] != "refresh-1" {
		t.Errorf("kept %v", kept)
	}
	ended := Resume("revoked")
	if _, err := ended.Organizations(ctx); !errors.Is(err, ErrSignedOut) {
		t.Errorf("a revoked sign-in: %v", err)
	}
}

func TestRefusedClientKeepsTheSignIn(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {})
	ClientID = "other"
	c := Resume("refresh-0")
	if _, err := c.Organizations(context.Background()); err == nil || errors.Is(err, ErrSignedOut) {
		t.Errorf("a refused client: %v", err)
	}
	if c.RefreshToken() != "refresh-0" {
		t.Errorf("kept %q", c.RefreshToken())
	}
}

func TestDeployFunctionSendsSources(t *testing.T) {
	var meta map[string]any
	var names []string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/abc/functions/deploy" || r.URL.Query().Get("slug") != "account" {
			http.NotFound(w, r)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		for part, err := mr.NextPart(); err == nil; part, err = mr.NextPart() {
			_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			switch params["name"] {
			case "metadata":
				data, _ := io.ReadAll(part)
				_ = json.Unmarshal(data, &meta)
			case "file":
				names = append(names, params["filename"])
			}
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	})
	c := Resume("refresh-0")
	err := c.DeployFunction(context.Background(), "abc", "account", "supabase/functions/account/index.ts", true, []File{
		{Path: "supabase/functions/account/index.ts", Data: []byte("x")},
		{Path: "supabase/functions/_shared/cors.ts", Data: []byte("y")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta["entrypoint_path"] != "supabase/functions/account/index.ts" || meta["verify_jwt"] != true {
		t.Errorf("metadata %v", meta)
	}
	if strings.Join(names, ",") != "supabase/functions/account/index.ts,supabase/functions/_shared/cors.ts" {
		t.Errorf("files %v", names)
	}
}

func TestSignInThroughTheWorker(t *testing.T) {
	var form url.Values
	srv := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	Auth = srv.URL
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			form = r.PostForm
			if _, _, ok := r.BasicAuth(); ok {
				t.Error("the app sent a secret")
			}
		}
		inner.ServeHTTP(w, r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := SignIn(ctx, browser(t, ctx))
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken() != "refresh-1" || form.Get("redirect_uri") != srv.URL+"/auth/supabase" {
		t.Errorf("kept %q, sent %v", c.RefreshToken(), form)
	}
}

func TestBeginAndFinishSignIn(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	link, state, verifier := BeginSignIn()
	req, _ := http.NewRequestWithContext(ctx, "GET", link, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if _, err := FinishSignIn(ctx, url.Values{"code": {"the-code"}, "state": {"another"}}, state, verifier); err == nil {
		t.Error("a stale answer signed in")
	}
	if _, err := FinishSignIn(ctx, url.Values{"error": {"access_denied"}, "state": {state}}, state, verifier); err == nil {
		t.Error("a cancelled sign-in signed in")
	}
	c, err := FinishSignIn(ctx, url.Values{"code": {"the-code"}, "state": {state}}, state, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken() != "refresh-1" {
		t.Errorf("kept %q", c.RefreshToken())
	}
}

func TestSignInFailsInTheAppsLanguage(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	_, err := FinishSignIn(context.Background(), url.Values{"state": {"s"}, "error": {"access_denied"}}, "s", "v")
	if err == nil || err.Error() != "登录已取消。" {
		t.Errorf("cancelled: %v", err)
	}
	if !errors.Is(fmt.Errorf("account: %w", ErrSignedOut), ErrSignedOut) || ErrSignedOut.Error() != "你的 Supabase 登录已失效。请重新使用 Supabase 登录。" {
		t.Errorf("signed out: %v", ErrSignedOut)
	}
}
