package agents

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"leaflet/internal/oauth"
	"leaflet/locales"
)

const (
	chatgptIssuer   = "https://auth.openai.com"
	chatgptResource = "https://api.openai.com/v1"
	chatgptScopes   = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	chatgptPlan     = "chatgpt.tokens.use.direct"
	chatgptPort     = 1455
)

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

func errSignInUnreachable(status string) error {
	return errors.New(locales.AgentChatgptUnreachable(status))
}

func isJSON(res *http.Response) bool {
	t, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	return t == "application/json" || strings.HasPrefix(t, "application/") && strings.HasSuffix(t, "+json")
}

var signInHTTP = &http.Client{Timeout: 30 * time.Second}

func getSignInJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
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
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
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

func SignInChatGPT(ctx context.Context, hostID, clientID string, open func(url string)) (*ChatGPT, error) {
	d, err := discover(ctx)
	if err != nil {
		return nil, err
	}
	lb, err := oauth.Listen("ChatGPT", chatgptPort, true)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	nonce := oauth.RandomString(32)
	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {lb.Redirect},
		"scope":                 {chatgptScopes},
		"resource":              {chatgptResource},
		"state":                 {lb.State},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {lb.Challenge()},
		"ext_agent_host_id":     {hostID},
	}
	if clientID != "" {
		q.Set("client_id", clientID)
	} else {
		q.Set("client_id", "dynamic_agent_client")
		q.Set("agent_name_hint", "Leaflet")
	}
	open(d.AuthorizationEndpoint + "?" + q.Encode())
	back, err := lb.Wait(ctx)
	if err != nil {
		return nil, err
	}
	issued := back.Get("client_id")
	if issued == "" {
		issued = clientID
	}
	switch {
	case issued == "" || issued == "dynamic_agent_client" || !clientIDRe.MatchString(issued):
		return nil, errors.New(locales.AgentChatgptNotRegistered())
	case clientID != "" && issued != clientID:
		return nil, errors.New(locales.AgentChatgptOtherRegistration())
	}
	t, err := postToken(ctx, d.TokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {issued}, "code": {back.Get("code")},
		"code_verifier": {lb.Verifier}, "redirect_uri": {lb.Redirect}, "resource": {chatgptResource},
	})
	if err != nil {
		return nil, err
	}
	claims, err := verifyIDToken(ctx, d, t.IDToken, issued, nonce)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(" "+t.Scope+" ", " "+chatgptPlan+" ") {
		return nil, errors.New(locales.AgentChatgptNoPlan())
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
			if exp := new(big.Int).SetBytes(e); err1 == nil && err2 == nil && exp.IsInt64() {
				if v := exp.Int64(); v > 0 && v <= math.MaxInt32 {
					key = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(v)}
				}
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

type ChatGPT struct {
	mu        sync.Mutex
	acct      ChatGPTAccount
	access    string
	expires   time.Time
	base      string
	OnRefresh func(ChatGPTAccount)
	renewing  sync.Mutex
}

var chatgptHTTP = sync.OnceValue(func() *http.Client {
	return &http.Client{Transport: streamTransport(), CheckRedirect: noRedirect}
})

func NewChatGPT(acct ChatGPTAccount) *ChatGPT { return &ChatGPT{acct: acct} }

func (g *ChatGPT) Account() ChatGPTAccount {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acct
}

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
	if tok, ok := g.fresh(); ok {
		return tok, nil
	}
	acct := g.Account()
	if acct.RefreshToken == "" {
		return "", errors.New(locales.AgentChatgptSignInAgain())
	}
	d, err := discover(ctx)
	if err != nil {
		return "", err
	}
	t, err := postToken(context.WithoutCancel(ctx), d.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {acct.ClientID},
		"refresh_token": {acct.RefreshToken}, "resource": {chatgptResource},
	})
	if err != nil {
		if t != nil && t.Error == "invalid_grant" {
			return "", errors.New(locales.AgentChatgptSignInEnded())
		}
		return "", err
	}
	g.mu.Lock()
	g.access, g.expires = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	if t.RefreshToken != "" {
		g.acct.RefreshToken = t.RefreshToken
		acct = g.acct
	}
	g.mu.Unlock()
	if t.RefreshToken != "" && g.OnRefresh != nil {
		g.OnRefresh(acct)
	}
	return t.AccessToken, nil
}

func (g *ChatGPT) drop(tok string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access == tok {
		g.access = ""
	}
}

func (g *ChatGPT) SignOut(ctx context.Context) {
	d, err := discover(ctx)
	if err != nil || d.RevocationEndpoint == "" {
		return
	}
	acct := g.Account()
	req, err := http.NewRequestWithContext(ctx, "POST", d.RevocationEndpoint, strings.NewReader(url.Values{
		"token": {acct.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {acct.ClientID},
	}.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err := signInHTTP.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

func (g *ChatGPT) do(ctx context.Context, method, path string, body any, accept string) (*http.Response, error) {
	return g.doOnce(ctx, method, path, body, accept, true)
}

func (g *ChatGPT) doOnce(ctx context.Context, method, path string, body any, accept string, again bool) (*http.Response, error) {
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
	if res.StatusCode == http.StatusUnauthorized && again {
		_ = res.Body.Close()
		g.drop(tok)
		return g.doOnce(ctx, method, path, body, accept, false)
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
		return nil, &APIError{Status: res.StatusCode, Message: msg, RetryAfter: retryAfter(res.Header)}
	}
	return res, nil
}

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

func (g *ChatGPT) Chat(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (ChatResult, error) {
	return g.ChatStream(ctx, model, messages, tools, maxTokens, nil)
}

func (g *ChatGPT) ChatStream(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int, on *Streamed) (ChatResult, error) {
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
	var items []any
	var deltas strings.Builder
	type streaming struct {
		i    int
		name string
		args strings.Builder
	}
	calls := map[float64]*streaming{}
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
				on.text(deltas.String())
			}
		case "response.output_item.added":
			item, _ := ev["item"].(map[string]any)
			if item["type"] == "function_call" {
				at, _ := ev["output_index"].(float64)
				name, _ := item["name"].(string)
				calls[at] = &streaming{i: len(calls), name: name}
			}
		case "response.function_call_arguments.delta":
			at, _ := ev["output_index"].(float64)
			if c := calls[at]; c != nil {
				d, _ := ev["delta"].(string)
				c.args.WriteString(d)
				on.call(c.i, c.name, c.args.String())
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
			msg := locales.AgentChatgptCouldNotAnswer()
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
			return ChatResult{}, &APIError{Message: incompleteMessage(ev)}
		}
	}
	if err := sc.Err(); err != nil {
		return ChatResult{}, err
	}
	return ChatResult{}, &APIError{Message: locales.AgentChatgptCutOff()}
}

func incompleteMessage(ev map[string]any) string {
	resp, _ := ev["response"].(map[string]any)
	details, _ := resp["incomplete_details"].(map[string]any)
	switch details["reason"] {
	case "max_output_tokens":
		return locales.AgentChatgptStoppedAtLimit()
	case "content_filter":
		return locales.AgentChatgptStoppedFiltered()
	}
	return locales.AgentChatgptStoppedPlan()
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

type Model interface {
	Chat(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (ChatResult, error)
}

type StreamingModel interface {
	ChatStream(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int, on *Streamed) (ChatResult, error)
}
