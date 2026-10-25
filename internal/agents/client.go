// Package agents runs agents on pages: an OpenAI-compatible client, the
// provider presets, the system prompt, the page tools every surface uses,
// the loop for local models and the client of the server's loop for cloud
// models (docs/protocol.md §2–4).
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// APIError is a failed model request.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// ToolCall is a tool the model asked to call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // the raw JSON arguments
}

// ChatResult is the model's answer.
type ChatResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

// AssistantMessage is the answer as a message of the conversation.
func (r ChatResult) AssistantMessage() map[string]any {
	m := map[string]any{"role": "assistant", "content": r.Content}
	if len(r.ToolCalls) > 0 {
		var calls []any
		for _, c := range r.ToolCalls {
			calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Arguments}})
		}
		m["tool_calls"] = calls
	}
	return m
}

// Client talks to any OpenAI-compatible API: OpenAI, Claude's compatibility
// layer, xAI, OpenRouter, DeepSeek, GLM, Ollama, LM Studio, llama.cpp, vLLM.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient returns a client for an endpoint.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{BaseURL: NormalizeBaseURL(baseURL), APIKey: apiKey, HTTP: &http.Client{Timeout: 3 * time.Minute}}
}

// NormalizeBaseURL trims slashes and a pasted /chat/completions or /models.
func NormalizeBaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, s := range []string{"/chat/completions", "/models"} {
		u = strings.TrimSuffix(u, s)
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		if strings.Contains(c.BaseURL, "anthropic.com") {
			req.Header.Set("x-api-key", c.APIKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		msg := res.Status
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			switch e := m["error"].(type) {
			case map[string]any:
				if s, ok := e["message"].(string); ok {
					msg = s
				}
			case string:
				msg = e
			}
		} else if body := strings.TrimSpace(string(data)); body != "" && len(body) < 200 && !strings.HasPrefix(body, "<") {
			msg = body
		}
		return nil, &APIError{Status: res.StatusCode, Message: msg}
	}
	return data, nil
}

// ListModels reads the endpoint's /models.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := c.do(ctx, "GET", "/models", nil)
	if err != nil {
		return nil, err
	}
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	var list []any
	switch b := body.(type) {
	case map[string]any:
		if l, ok := b["data"].([]any); ok {
			list = l
		} else if l, ok := b["models"].([]any); ok {
			list = l
		}
	case []any:
		list = b
	}
	var ids []string
	for _, m := range list {
		switch x := m.(type) {
		case map[string]any:
			if id, ok := x["id"].(string); ok {
				ids = append(ids, id)
			} else if name, ok := x["name"].(string); ok {
				ids = append(ids, name)
			}
		case string:
			ids = append(ids, x)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Chat sends a conversation, with tools when given.
func (c *Client) Chat(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (ChatResult, error) {
	body := map[string]any{"model": model, "messages": messages, "stream": false}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	data, err := c.do(ctx, "POST", "/chat/completions", body)
	if err != nil {
		return ChatResult{}, err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments any    `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return ChatResult{}, err
	}
	if len(resp.Choices) == 0 {
		return ChatResult{}, &APIError{Message: "The model returned no choices."}
	}
	ch := resp.Choices[0]
	out := ChatResult{Content: textOf(ch.Message.Content), FinishReason: ch.FinishReason}
	for i, tc := range ch.Message.ToolCalls {
		args := ""
		switch a := tc.Function.Arguments.(type) {
		case string:
			args = a
		case nil:
			args = "{}"
		default:
			raw, _ := json.Marshal(a)
			args = string(raw)
		}
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Arguments: args})
	}
	return out, nil
}

func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, p := range x {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// TestResult is what a connection test found.
type TestResult struct {
	OK            bool
	SupportsTools bool
	Message       string
	Models        []string
}

// Test checks the endpoint answers and whether the model calls tools.
func (c *Client) Test(ctx context.Context, model string) TestResult {
	models, listErr := c.ListModels(ctx)
	if model == "" {
		if listErr != nil {
			return TestResult{Message: fmt.Sprintf("Could not reach %s: %v", c.BaseURL, listErr)}
		}
		if len(models) == 0 {
			return TestResult{Message: "Choose a model to test with"}
		}
		model = models[0]
	}
	res, err := c.Chat(ctx, model, []map[string]any{{
		"role": "user", "content": `Call the ping tool with {"value": "ok"}. Do not reply with text.`,
	}}, []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "ping", "description": "Health check.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}},
		},
	}}, 64)
	if err != nil {
		if _, ok := err.(*APIError); ok {
			return TestResult{Message: err.Error(), Models: models}
		}
		return TestResult{Message: fmt.Sprintf("Could not reach %s: %v", c.BaseURL, err), Models: models}
	}
	tools := false
	for _, tc := range res.ToolCalls {
		tools = tools || tc.Name == "ping"
	}
	msg := "Connected, but this model did not call tools: it will be chat-only."
	if tools {
		switch len(models) {
		case 0:
			msg = "Connected · tool calling works"
		case 1:
			msg = "Connected · 1 model available"
		default:
			msg = fmt.Sprintf("Connected · %d models available", len(models))
		}
	}
	return TestResult{OK: true, SupportsTools: tools, Message: msg, Models: models}
}
