// Package agents runs agents on pages: an OpenAI-compatible client, the
// provider presets, the system prompt, the page tools every surface uses,
// the loop for local models and the client of the server's loop for cloud
// models (docs/protocol.md §2–4).
package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/agents/endpoint"
	"leaflet/locales"
)

// APIError is a failed model request.
type APIError struct {
	Status  int
	Message string
	// RetryAfter is how long the server asked to be left before trying
	// again, 0 when it did not say.
	RetryAfter time.Duration
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
	// Stream is the client of streamed answers, HTTP when nil.
	Stream *http.Client
}

// NewClient returns a client for an endpoint.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: endpoint.NormalizeBaseURL(baseURL), APIKey: apiKey,
		HTTP:   &http.Client{Transport: answerTransport(), CheckRedirect: noRedirect},
		Stream: &http.Client{Transport: streamedAnswerTransport(), CheckRedirect: noRedirect},
	}
}

// Model requests have no overall timeout: a long answer may take most of
// a run, whose context bounds it. A server that stops answering is given
// up on instead.
const (
	// answerTimeout bounds the wait for an answer that is not streamed,
	// whose headers come only once the model has finished: a run's length.
	answerTimeout = 10 * time.Minute
	// streamTimeout bounds the wait for a streamed answer to start.
	streamTimeout = 2 * time.Minute
)

// idleTimeout is how long an answer may go without sending more of
// itself; tests shorten it.
var idleTimeout = 2 * time.Minute

// streamIdleTimeout is idleTimeout for a streamed answer, ChatGPT's: its
// Responses API sends nothing while the model reasons, which can take
// minutes before the first word.
var streamIdleTimeout = 10 * time.Minute

// errIdle is an answer that stopped coming.
var errIdle = errors.New("the model stopped answering")

// The transports are made on first use, after main gives
// http.DefaultTransport the system's proxy (netproxy), so they keep it.
var (
	answerTransport = sync.OnceValue(func() http.RoundTripper { return modelTransport(answerTimeout, &idleTimeout) })
	streamTransport = sync.OnceValue(func() http.RoundTripper { return modelTransport(streamTimeout, &streamIdleTimeout) })
	// streamedAnswerTransport is an OpenAI-compatible server's streamed
	// answer: a local server may load the model before it starts, and a
	// reasoning model may send nothing while it thinks.
	streamedAnswerTransport = sync.OnceValue(func() http.RoundTripper { return modelTransport(answerTimeout, &streamIdleTimeout) })
)

// modelTransport is http.DefaultTransport giving up on a server that sends
// no headers within header, or no more of its answer within *idle.
func modelTransport(header time.Duration, idle *time.Duration) http.RoundTripper {
	base := http.DefaultTransport
	if t, ok := base.(*http.Transport); ok {
		t = t.Clone()
		t.ResponseHeaderTimeout = header
		base = t
	}
	return idleTransport{base, idle}
}

type idleTransport struct {
	base http.RoundTripper
	idle *time.Duration
}

func (t idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	res, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	b := &idleBody{ReadCloser: res.Body, ctx: ctx, cancel: cancel, idle: *t.idle}
	b.timer = time.AfterFunc(b.idle, func() { cancel(errIdle) })
	res.Body = b
	return res, nil
}

// idleBody is an answer whose request ends when it stops coming.
type idleBody struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	idle   time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && errors.Is(context.Cause(b.ctx), errIdle) {
		err = errIdle
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}

// noRedirect refuses to follow a redirect: the request carries the API
// key, which would go on to wherever the redirect points.
func noRedirect(*http.Request, []*http.Request) error {
	return errors.New("redirected")
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	res, err := c.send(ctx, c.HTTP, method, path, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return io.ReadAll(res.Body)
}

// send makes a request, its answer an error when it failed.
func (c *Client) send(ctx context.Context, hc *http.Client, method, path string, body any) (*http.Response, error) {
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
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 400 {
		return res, nil
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return nil, &APIError{Status: res.StatusCode, Message: errorMessage(res.Status, data), RetryAfter: retryAfter(res.Header)}
}

// retryAfter is the wait a busy server's Retry-After asks for, in seconds
// or until a time; 0 when it asks none.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if s, err := strconv.Atoi(v); err == nil {
		return max(0, time.Duration(s)*time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(t))
	}
	return 0
}

// errorMessage is what a failed answer says went wrong, its status if it
// says nothing readable.
func errorMessage(status string, data []byte) string {
	msg := status
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
	return msg
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
	return c.ChatStream(ctx, model, messages, tools, maxTokens, nil)
}

// Streamed is told an answer as it streams.
type Streamed struct {
	// Text gets the answer's text so far.
	Text func(text string)
	// Call gets the i-th tool call so far: its arguments as far as they
	// came, the calls before it whole.
	Call func(i int, name, args string)
}

func (s *Streamed) text(t string) {
	if s != nil && s.Text != nil {
		s.Text(t)
	}
}

func (s *Streamed) call(i int, name, args string) {
	if s != nil && s.Call != nil {
		s.Call(i, name, args)
	}
}

// ChatStream is Chat with the answer streamed to on as it comes. A nil on
// asks for the answer whole.
func (c *Client) ChatStream(ctx context.Context, model string, messages []map[string]any, tools []map[string]any, maxTokens int, on *Streamed) (ChatResult, error) {
	body := map[string]any{"model": model, "messages": messages, "stream": on != nil}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	hc := c.HTTP
	if on != nil && c.Stream != nil {
		hc = c.Stream
	}
	res, err := c.send(ctx, hc, "POST", "/chat/completions", body)
	if err != nil {
		return ChatResult{}, err
	}
	defer func() { _ = res.Body.Close() }()
	// A server that does not stream answers whole, as JSON.
	if on != nil && strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return readStream(res.Body, on)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return ChatResult{}, err
	}
	var resp struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return ChatResult{}, err
	}
	if len(resp.Choices) == 0 {
		return ChatResult{}, &APIError{Message: locales.AgentModelNoChoices()}
	}
	ch := resp.Choices[0]
	out := ChatResult{Content: textOf(ch.Message.Content), FinishReason: ch.FinishReason}
	for i, tc := range ch.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, tc.call(i))
	}
	if out.Content != "" {
		on.text(out.Content)
	}
	return out, nil
}

// chatMessage is a message of an answer, or a streamed piece of one.
type chatMessage struct {
	Content   any            `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatToolCall struct {
	// Index places a streamed piece of a call among the answer's calls.
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

func (tc chatToolCall) arguments() string {
	switch a := tc.Function.Arguments.(type) {
	case string:
		return a
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(a)
		return string(raw)
	}
}

// call is the i-th call of an answer.
func (tc chatToolCall) call(i int) ToolCall {
	args := tc.arguments()
	if args == "" {
		args = "{}"
	}
	id := tc.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", i)
	}
	return ToolCall{ID: id, Name: tc.Function.Name, Arguments: args}
}

// readStream reads a streamed answer: server-sent events, each a piece of
// the message, its calls coming in pieces by index.
func readStream(r io.Reader, on *Streamed) (ChatResult, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var out ChatResult
	var text strings.Builder
	var calls []*chatToolCall
	var args []*strings.Builder
	// slot numbers the calls by a server's indices in the order they
	// began, as the answer's ToolCalls are: the indices may start past 0
	// or leave gaps.
	slot := map[int]int{}
	done := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			done = true
			break
		}
		var ev struct {
			Choices []struct {
				Delta        chatMessage `json:"delta"`
				FinishReason *string     `json:"finish_reason"`
			} `json:"choices"`
			Error any `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Error != nil {
			return ChatResult{}, &APIError{Message: errorMessage(locales.AgentModelCouldNotAnswer(), []byte(data))}
		}
		if len(ev.Choices) == 0 {
			continue
		}
		ch := ev.Choices[0]
		if s := textOf(ch.Delta.Content); s != "" {
			text.WriteString(s)
			on.text(text.String())
		}
		for _, tc := range ch.Delta.ToolCalls {
			// A call without an index is a new one, unless it names one
			// that came before.
			i := -1
			if tc.Index != nil {
				// An index a server gives again with another ID starts
				// another call: merged, the two would be one, of the
				// second's name and both's arguments run together.
				if j, ok := slot[*tc.Index]; ok && (tc.ID == "" || calls[j].ID == "" || calls[j].ID == tc.ID) {
					i = j
				}
			} else if tc.ID != "" {
				for j, c := range calls {
					if c.ID == tc.ID {
						i = j
					}
				}
			}
			if i < 0 {
				i = len(calls)
				calls = append(calls, &chatToolCall{})
				args = append(args, &strings.Builder{})
				if tc.Index != nil {
					slot[*tc.Index] = i
				}
			}
			if tc.ID != "" {
				calls[i].ID = tc.ID
			}
			if tc.Function.Name != "" {
				calls[i].Function.Name = tc.Function.Name
			}
			args[i].WriteString(tc.arguments())
			on.call(i, calls[i].Function.Name, args[i].String())
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			out.FinishReason = *ch.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return ChatResult{}, err
	}
	if !done && out.FinishReason == "" {
		return ChatResult{}, &APIError{Message: locales.AgentModelCutOff()}
	}
	out.Content = text.String()
	for i, c := range calls {
		c.Function.Arguments = args[i].String()
		out.ToolCalls = append(out.ToolCalls, c.call(i))
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

// Test checks the endpoint answers and whether the model calls tools: the
// one given, else the first the endpoint lists.
func (c *Client) Test(ctx context.Context, model string) TestResult {
	models, listErr := c.ListModels(ctx)
	if model == "" {
		if listErr != nil {
			return TestResult{Message: locales.AgentTestUnreachable(c.BaseURL, listErr.Error())}
		}
		if len(models) == 0 {
			return TestResult{Message: locales.AgentTestChooseModel()}
		}
		model = models[0]
	}
	return testModel(ctx, c, model, models, c.BaseURL)
}

// TestModel checks a model answers and whether it calls tools; models are
// those its provider lists, nil when it lists none.
func TestModel(ctx context.Context, m Model, model string, models []string) TestResult {
	return testModel(ctx, m, model, models, "")
}

// testModel is TestModel of a model at the endpoint at, which a failure
// other than the model's own answer says could not be reached; "" says
// the failure as it is.
func testModel(ctx context.Context, m Model, model string, models []string, at string) TestResult {
	res, err := m.Chat(ctx, model, []map[string]any{{
		"role": "user", "content": `Call the ping tool with {"value": "ok"}. Do not reply with text.`,
	}}, []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "ping", "description": "Health check.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}},
		},
		// No cap on the answer: a model that thinks first spends a small
		// one thinking, and would seem not to call tools, and reasoning
		// models refuse max_tokens. The server's test sets none either.
	}}, 0)
	if err != nil {
		var api *APIError
		if errors.As(err, &api) || at == "" {
			return TestResult{Message: err.Error(), Models: models}
		}
		return TestResult{Message: locales.AgentTestUnreachable(at, err.Error()), Models: models}
	}
	tools := false
	for _, tc := range res.ToolCalls {
		tools = tools || tc.Name == "ping"
	}
	msg := locales.AgentTestChatOnly()
	switch {
	case tools && len(models) == 0:
		msg = locales.AgentTestToolsWork()
	case tools:
		msg = locales.AgentTestModels(len(models))
	}
	return TestResult{OK: true, SupportsTools: tools, Message: msg, Models: models}
}
