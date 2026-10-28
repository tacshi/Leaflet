package agents

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sign in with ChatGPT: the user's ChatGPT Plus or Pro plan pays for agent
// runs, through OpenAI's OAuth for open-source and personal apps
// (developers.openai.com/siwc). The app registers itself on first sign-in
// and gets a client ID of its own; tokens stay on this device.

const (
	chatgptIssuer   = "https://auth.openai.com"
	chatgptResource = "https://api.openai.com/v1"
	chatgptScopes   = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	chatgptPlan     = "chatgpt.tokens.use.direct"
	chatgptPort     = 1455
)

// ChatGPTAccount is what is kept of a sign-in, in the keychain.
type ChatGPTAccount struct {
	ClientID     string `json:"client_id"`
	Email        string `json:"email"`
	Subject      string `json:"sub"`
	RefreshToken string `json:"refresh_token"`
}

type chatgptDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

var (
	discoveryMu    sync.Mutex
	discoveryCache *chatgptDiscovery
)

func discover(ctx context.Context) (*chatgptDiscovery, error) {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	if discoveryCache != nil {
		return discoveryCache, nil
	}
	var d chatgptDiscovery
	if err := getSignInJSON(ctx, chatgptIssuer+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if d.Issuer != chatgptIssuer {
		return nil, errors.New("OpenAI's sign-in answered for another issuer")
	}
	for _, e := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI} {
		if !strings.HasPrefix(e, chatgptIssuer+"/") {
			return nil, errors.New("OpenAI's sign-in endpoints are not on its own site")
		}
	}
	if d.RevocationEndpoint != "" && !strings.HasPrefix(d.RevocationEndpoint, chatgptIssuer+"/") {
		d.RevocationEndpoint = ""
	}
	discoveryCache = &d
	return &d, nil
}

// errSignInUnreachable is a page where OpenAI's sign-in should answer
// with JSON: a block page for the region, or the network's own page when
// a proxy is not used.
func errSignInUnreachable(status string) error {
	return fmt.Errorf("Leaflet couldn't reach ChatGPT's sign-in (%s). Check your network or proxy, then try again.", status) //nolint:staticcheck // shown to people as a sentence.
}

// isJSON is application/json, or a type of it such as jwk-set+json.
func isJSON(res *http.Response) bool {
	t, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	return t == "application/json" || strings.HasPrefix(t, "application/") && strings.HasSuffix(t, "+json")
}

// signInHTTP is the client of OpenAI's sign-in. Its answers are small, so
// one that takes longer than this has stalled, and would otherwise keep a
// run waiting on a token.
var signInHTTP = &http.Client{Timeout: 30 * time.Second}

// getSignInJSON reads one of the sign-in's JSON documents.
func getSignInJSON(ctx context.Context, u string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Accept", "application/json")
	res, err := signInHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || !isJSON(res) {
		return errSignInUnreachable(res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("ChatGPT sign-in failed: %w", err)
	}
	return nil
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{4,200}$`)

type chatgptTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func postToken(ctx context.Context, endpoint string, form url.Values) (*chatgptTokens, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := signInHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var t chatgptTokens
	data, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(data, &t); err != nil && res.StatusCode < 400 {
		if !isJSON(res) {
			return nil, errSignInUnreachable(res.Status)
		}
		return nil, fmt.Errorf("ChatGPT sign-in failed: %w", err)
	}
	if res.StatusCode >= 400 || t.Error != "" {
		msg := t.Description
		if msg == "" {
			msg = t.Error
		}
		if msg == "" {
			msg = res.Status
		}
		return &t, fmt.Errorf("ChatGPT sign-in failed: %s", msg)
	}
	return &t, nil
}

// SignInChatGPT signs in through the browser, which open opens. hostID
// names this device to OpenAI and stays the same; clientID is the one an
// earlier sign-in registered, or "".
func SignInChatGPT(ctx context.Context, hostID, clientID string, open func(url string)) (*ChatGPT, error) {
	d, err := discover(ctx)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", chatgptPort))
	if err != nil {
		// Only the port may differ.
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	host := fmt.Sprintf("127.0.0.1:%d", port)
	redirect := "http://" + host + "/auth/callback"
	state, nonce, verifier := randomString(32), randomString(32), randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"scope":                 {chatgptScopes},
		"resource":              {chatgptResource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"ext_agent_host_id":     {hostID},
	}
	if clientID != "" {
		q.Set("client_id", clientID)
	} else {
		q.Set("client_id", "dynamic_agent_client")
		q.Set("agent_name_hint", "Leaflet")
	}
	type callback struct {
		code, clientID string
		err            error
	}
	done := make(chan callback, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/auth/callback" || r.Host != host {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(v.Get("state")), []byte(state)) != 1 {
			http.Error(w, "This sign-in link is stale. Start again from Leaflet.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := v.Get("error"); e != "" {
			_, _ = fmt.Fprint(w, signInPage("Sign-in was cancelled", "You can close this tab."))
			msg := "Sign-in was cancelled."
			if e != "access_denied" {
				msg = "ChatGPT sign-in failed: " + e
			}
			done <- callback{err: errors.New(msg)}
			return
		}
		_, _ = fmt.Fprint(w, signInPage("Signed in to Leaflet", "You can close this tab and go back to Leaflet."))
		done <- callback{code: v.Get("code"), clientID: v.Get("client_id")}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	open(d.AuthorizationEndpoint + "?" + q.Encode())
	var cb callback
	select {
	case cb = <-done:
	case <-ctx.Done():
		return nil, errors.New("Sign-in timed out. Try again.") //nolint:staticcheck // shown to people as a sentence.
	}
	if cb.err != nil {
		return nil, cb.err
	}
	issued := cb.clientID
	if issued == "" {
		issued = clientID
	}
	switch {
	case issued == "" || issued == "dynamic_agent_client" || !clientIDRe.MatchString(issued):
		return nil, errors.New("ChatGPT did not register Leaflet. Try again.") //nolint:staticcheck // shown to people as a sentence.
	case clientID != "" && issued != clientID:
		return nil, errors.New("ChatGPT answered for another registration. Try again.") //nolint:staticcheck // shown to people as a sentence.
	}
	t, err := postToken(ctx, d.TokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {issued}, "code": {cb.code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {chatgptResource},
	})
	if err != nil {
		return nil, err
	}
	claims, err := verifyIDToken(ctx, d, t.IDToken, issued, nonce)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(" "+t.Scope+" ", " "+chatgptPlan+" ") {
		return nil, errors.New("ChatGPT didn't let Leaflet use your plan. Using a plan needs ChatGPT Plus or Pro.") //nolint:staticcheck // shown to people as a sentence.
	}
	email, _ := claims["email"].(string)
	sub, _ := claims["sub"].(string)
	g := &ChatGPT{
		acct:    ChatGPTAccount{ClientID: issued, Email: email, Subject: sub, RefreshToken: t.RefreshToken},
		access:  t.AccessToken,
		expires: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second),
	}
	return g, nil
}

func signInPage(title, body string) string {
	return `<!doctype html><meta charset="utf-8"><title>Leaflet</title><body style="font:16px -apple-system,system-ui,sans-serif;display:grid;place-items:center;height:90vh;color:#16181d"><div style="text-align:center"><h2 style="margin:0 0 8px">` +
		title + `</h2><p style="color:#565c67;margin:0">` + body + `</p></div></body>`
}

// verifyIDToken checks the ID token's signature against OpenAI's keys, and
// its issuer, audience, expiry and nonce.
func verifyIDToken(ctx context.Context, d *chatgptDiscovery, token, clientID, nonce string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("ChatGPT sent no identity token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "RS256" {
		return nil, errors.New("ChatGPT's identity token is not one Leaflet can check")
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getSignInJSON(ctx, d.JWKSURI, &jwks); err != nil {
		return nil, err
	}
	var key *rsa.PublicKey
	for _, k := range jwks.Keys {
		if k.Kid == header.Kid && k.Kty == "RSA" {
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 == nil && err2 == nil {
				key = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
			}
		}
	}
	if key == nil {
		return nil, errors.New("ChatGPT's identity token is signed with an unknown key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, errors.New("ChatGPT's identity token did not verify")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	audOK := false
	switch aud := claims["aud"].(type) {
	case string:
		audOK = aud == clientID
	case []any:
		for _, a := range aud {
			audOK = audOK || a == clientID
		}
	}
	exp, _ := claims["exp"].(float64)
	switch {
	case claims["iss"] != chatgptIssuer:
		return nil, errors.New("ChatGPT's identity token is from another issuer")
	case !audOK:
		return nil, errors.New("ChatGPT's identity token is for another app")
	case time.Unix(int64(exp), 0).Before(time.Now().Add(-time.Minute)):
		return nil, errors.New("ChatGPT's identity token has expired")
	case claims["nonce"] != nonce:
		return nil, errors.New("ChatGPT's identity token is for another sign-in")
	}
	return claims, nil
}

// ChatGPT runs agent requests on a ChatGPT plan through the Responses API.
type ChatGPT struct {
	// mu guards acct, access and expires, never across a request:
	// Settings reads the account on the main thread while a token renews.
	mu      sync.Mutex
	acct    ChatGPTAccount
	access  string
	expires time.Time
	// base is the API's address; tests point it elsewhere.
	base string
	// OnRefresh keeps the account as its refresh token rotates.
	OnRefresh func(ChatGPTAccount)
	// renewing is held while the access token renews, so calls that need
	// it at once wait for one renewal rather than each spend the refresh
	// token.
	renewing sync.Mutex
}

// chatgptHTTP is the client of the plan's API. Its answers stream, so
// they may take as long as a run, given they keep coming.
var chatgptHTTP = sync.OnceValue(func() *http.Client {
	return &http.Client{Transport: streamTransport(), CheckRedirect: noRedirect}
})

// NewChatGPT resumes a sign-in kept from before.
func NewChatGPT(acct ChatGPTAccount) *ChatGPT { return &ChatGPT{acct: acct} }

// Account returns what to keep of the sign-in.
func (g *ChatGPT) Account() ChatGPTAccount {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acct
}

// fresh is the access token while it has a minute or more left.
func (g *ChatGPT) fresh() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.access, g.access != "" && time.Now().Before(g.expires.Add(-time.Minute))
}

func (g *ChatGPT) token(ctx context.Context) (string, error) {
	if tok, ok := g.fresh(); ok {
		return tok, nil
	}
	g.renewing.Lock()
	defer g.renewing.Unlock()
	// Another call may have renewed it while this one waited.
	if tok, ok := g.fresh(); ok {
		return tok, nil
	}
	acct := g.Account()
	if acct.RefreshToken == "" {
		return "", errors.New("Sign in with ChatGPT again in Settings › Agent providers.") //nolint:staticcheck // shown to people as a sentence.
	}
	d, err := discover(ctx)
	if err != nil {
		return "", err
	}
	t, err := postToken(ctx, d.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {acct.ClientID},
		"refresh_token": {acct.RefreshToken}, "resource": {chatgptResource},
	})
	if err != nil {
		if t != nil && t.Error == "invalid_grant" {
			return "", errors.New("Your ChatGPT sign-in ended. Sign in again in Settings › Agent providers.") //nolint:staticcheck // shown to people as a sentence.
		}
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.access, g.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	if t.RefreshToken != "" {
		g.acct.RefreshToken = t.RefreshToken
		if g.OnRefresh != nil {
			go g.OnRefresh(g.acct)
		}
	}
	return g.access, nil
}

// SignOut revokes the sign-in.
func (g *ChatGPT) SignOut(ctx context.Context) {
	d, err := discover(ctx)
	if err != nil || d.RevocationEndpoint == "" {
		return
	}
	acct := g.Account()
	req, _ := http.NewRequestWithContext(ctx, "POST", d.RevocationEndpoint, strings.NewReader(url.Values{
		"token": {acct.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {acct.ClientID},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err := signInHTTP.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

func (g *ChatGPT) do(ctx context.Context, method, path string, body any, accept string) (*http.Response, error) {
	tok, err := g.token(ctx)
	if err != nil {
		return nil, err
	}
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	base := g.base
	if base == "" {
		base = chatgptResource
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := chatgptHTTP().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer func() { _ = res.Body.Close() }()
		data, _ := io.ReadAll(res.Body)
		msg := res.Status
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if e, ok := m["error"].(map[string]any); ok {
				if s, ok := e["message"].(string); ok && s != "" {
					msg = s
				}
			}
		}
		return nil, &APIError{Status: res.StatusCode, Message: msg}
	}
	return res, nil
}

// ListModels lists the models the plan can use.
func (g *ChatGPT) ListModels(ctx context.Context) ([]string, error) {
	res, err := g.do(ctx, "GET", "/models", nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range body.Models {
		if m.Visibility == "list" && strings.TrimSpace(m.Slug) != "" {
			out = append(out, m.Slug)
		}
	}
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// Chat sends a chat-completions conversation through the Responses API:
// system messages become instructions, tool calls function_call items.
func (g *ChatGPT) Chat(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (ChatResult, error) {
	var instructions []string
	var input []any
	for _, m := range messages {
		role, _ := m["role"].(string)
		content := textOf(m["content"])
		switch role {
		case "system":
			instructions = append(instructions, content)
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m["tool_call_id"], "output": content})
		case "assistant":
			if content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": content})
			}
			if calls, ok := m["tool_calls"].([]any); ok {
				for _, c := range calls {
					call, _ := c.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					input = append(input, map[string]any{"type": "function_call", "call_id": call["id"], "name": fn["name"], "arguments": fn["arguments"]})
				}
			}
		default:
			input = append(input, map[string]any{"role": "user", "content": content})
		}
	}
	body := map[string]any{"model": model, "input": input, "store": false, "stream": true}
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(tools) > 0 {
		var ts []any
		for _, t := range tools {
			fn, _ := t["function"].(map[string]any)
			ts = append(ts, map[string]any{"type": "function", "name": fn["name"], "description": fn["description"], "parameters": fn["parameters"]})
		}
		body["tools"] = ts
		body["tool_choice"] = "auto"
	}
	if maxTokens > 0 {
		body["max_output_tokens"] = max(maxTokens, 16)
	}
	res, err := g.do(ctx, "POST", "/responses", body, "text/event-stream")
	if err != nil {
		return ChatResult{}, err
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	// The final event may come with an empty output, the items having
	// streamed before it, so they are kept as they arrive.
	var items []any
	var deltas strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "response.output_item.done":
			if item, ok := ev["item"].(map[string]any); ok {
				items = append(items, item)
			}
		case "response.output_text.delta":
			if d, ok := ev["delta"].(string); ok {
				deltas.WriteString(d)
			}
		case "response.completed":
			resp, _ := ev["response"].(map[string]any)
			out := parseResponse(resp)
			if out.Content == "" && len(out.ToolCalls) == 0 {
				out = parseResponse(map[string]any{"output": items})
				if out.Content == "" {
					out.Content = deltas.String()
				}
			}
			return out, nil
		case "response.failed", "error":
			msg := "ChatGPT could not answer"
			if resp, ok := ev["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if s, ok := e["message"].(string); ok {
						msg = s
					}
				}
			}
			if s, ok := ev["message"].(string); ok && s != "" {
				msg = s
			}
			return ChatResult{}, &APIError{Message: msg}
		case "response.incomplete":
			return ChatResult{}, &APIError{Message: "ChatGPT stopped before finishing; your plan's limit may have been reached."}
		}
	}
	if err := sc.Err(); err != nil {
		return ChatResult{}, err
	}
	return ChatResult{}, &APIError{Message: "ChatGPT's answer was cut off"}
}

func parseResponse(resp map[string]any) ChatResult {
	var out ChatResult
	items, _ := resp["output"].([]any)
	var text strings.Builder
	for _, it := range items {
		item, _ := it.(map[string]any)
		switch item["type"] {
		case "message":
			parts, _ := item["content"].([]any)
			for _, p := range parts {
				part, _ := p.(map[string]any)
				if part["type"] == "output_text" {
					s, _ := part["text"].(string)
					text.WriteString(s)
				}
			}
		case "function_call":
			id, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: name, Arguments: args})
		}
	}
	out.Content = text.String()
	return out
}

// Model is a chat model the agent loop can run on.
type Model interface {
	Chat(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (ChatResult, error)
}

// TestModel checks a model answers and whether it calls tools.
func TestModel(ctx context.Context, m Model, model string, models []string) TestResult {
	res, err := m.Chat(ctx, model, []map[string]any{{
		"role": "user", "content": `Call the ping tool with {"value": "ok"}. Do not reply with text.`,
	}}, []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "ping", "description": "Health check.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}},
		},
	}}, 64)
	if err != nil {
		return TestResult{Message: err.Error(), Models: models}
	}
	tools := false
	for _, tc := range res.ToolCalls {
		tools = tools || tc.Name == "ping"
	}
	msg := "Connected, but this model did not call tools: it will be chat-only."
	if tools {
		msg = "Connected · tool calling works"
	}
	return TestResult{OK: true, SupportsTools: tools, Message: msg, Models: models}
}
