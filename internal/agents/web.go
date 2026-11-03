package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"leaflet/locales"
)

// Searching the web for a run, through its provider: each searches in its
// own way, as a request of its own whose answer comes back to the run as a
// tool's result, its links among them, which the URL guard then knows
// (checkURLs). The agent function searches the same ways
// (supabase/functions/_shared/search.ts).

// Source is a page a search found.
type Source struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Search is what a search of the web found: its sources, and what the
// model that searched said of them.
type Search struct {
	Answer  string
	Sources []Source
}

// Searcher is a model whose provider may search the web for it.
type Searcher interface {
	// CanSearch reports whether the provider searches the web.
	CanSearch() bool
	// SearchWeb searches the web for query, with model.
	SearchWeb(ctx context.Context, model, query string) (Search, error)
}

// maxSearchAnswer, maxSources and maxSnippet bound what a search gives
// the run; maxSearchBytes, the answer read from the provider.
const (
	maxSearchAnswer = 8000
	maxSources      = 20
	maxSnippet      = 300
	maxSearchBytes  = 8 << 20
)

// searchPrompt asks the model that searches for what it found.
func searchPrompt(query string) string {
	return "Search the web for: " + query + "\n\nToday is " + Today() +
		". Say briefly what the most relevant results say, each with its link. Answer from the results alone."
}

// searchKind is how a provider at baseURL searches the web: on OpenAI's
// and xAI's Responses API, Claude's Messages API, or the Chat Completions
// of OpenRouter or GLM, with their own search tools; "" when it does not.
func searchKind(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.openai.com", "api.x.ai":
		return "responses"
	case "api.anthropic.com":
		return "anthropic"
	case "openrouter.ai":
		return "openrouter"
	case "open.bigmodel.cn", "api.z.ai":
		return "glm"
	}
	return ""
}

// CanSearch reports whether the client's provider searches the web.
func (c *Client) CanSearch() bool { return searchKind(c.BaseURL) != "" }

// SearchWeb searches the web through the client's provider.
func (c *Client) SearchWeb(ctx context.Context, model, query string) (Search, error) {
	prompt := searchPrompt(query)
	user := []map[string]any{{"role": "user", "content": prompt}}
	switch kind := searchKind(c.BaseURL); kind {
	case "responses":
		body := map[string]any{"model": model, "input": user, "tools": []any{map[string]any{"type": "web_search"}}, "store": false}
		// xAI takes include's values of its own.
		if strings.Contains(c.BaseURL, "api.openai.com") {
			body["include"] = []string{"web_search_call.action.sources"}
		}
		var res struct {
			Output []any `json:"output"`
		}
		if err := c.post(ctx, "/responses", body, &res); err != nil {
			return Search{}, err
		}
		return responsesSearch(res.Output), nil
	case "anthropic":
		body := map[string]any{"model": model, "max_tokens": 4096, "messages": user,
			"tools": []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 3}}}
		var res struct {
			Content []any `json:"content"`
		}
		if err := c.post(ctx, "/messages", body, &res); err != nil {
			return Search{}, err
		}
		return anthropicSearch(res.Content), nil
	case "openrouter", "glm":
		tool := map[string]any{"type": "openrouter:web_search", "parameters": map[string]any{"max_results": 5}}
		if kind == "glm" {
			tool = map[string]any{"type": "web_search", "web_search": map[string]any{
				"enable": true, "search_engine": "search_std", "search_result": true, "count": 5}}
		}
		var res chatSearchAnswer
		if err := c.post(ctx, "/chat/completions", map[string]any{"model": model, "messages": user, "tools": []any{tool}}, &res); err != nil {
			return Search{}, err
		}
		return res.search(), nil
	}
	return Search{}, &APIError{Message: locales.AgentSearchUnavailable()}
}

// post sends a request of the client's own and reads its JSON answer into
// out. Claude's Messages API takes the key in x-api-key alone.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if path == "/messages" {
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSearchBytes))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return &APIError{Status: res.StatusCode, Message: errorMessage(res.Status, data), RetryAfter: retryAfter(res.Header)}
	}
	return json.Unmarshal(data, out)
}

// CanSearch reports that a ChatGPT plan searches the web, as its
// workspace lets it: one that does not answers the search with an error.
func (g *ChatGPT) CanSearch() bool { return true }

// SearchWeb searches the web on the ChatGPT plan, with the Responses API's
// web_search.
func (g *ChatGPT) SearchWeb(ctx context.Context, model, query string) (Search, error) {
	body := map[string]any{
		"model": model, "input": []any{map[string]any{"role": "user", "content": searchPrompt(query)}},
		"tools": []any{map[string]any{"type": "web_search"}}, "include": []string{"web_search_call.action.sources"},
		"store": false, "stream": true,
	}
	res, err := g.do(ctx, "POST", "/responses", body, "text/event-stream")
	if err != nil {
		return Search{}, err
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var items []any
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
		case "response.completed":
			resp, _ := ev["response"].(map[string]any)
			if out, _ := resp["output"].([]any); len(out) > 0 {
				items = out
			}
			return responsesSearch(items), nil
		case "response.failed", "error", "response.incomplete":
			msg := locales.AgentChatgptCouldNotAnswer()
			if resp, ok := ev["response"].(map[string]any); ok {
				if e, ok := resp["error"].(map[string]any); ok {
					if s, ok := e["message"].(string); ok && s != "" {
						msg = s
					}
				}
			}
			if s, ok := ev["message"].(string); ok && s != "" {
				msg = s
			}
			return Search{}, &APIError{Message: msg}
		}
	}
	if err := sc.Err(); err != nil {
		return Search{}, err
	}
	return Search{}, &APIError{Message: locales.AgentChatgptCutOff()}
}

// chatSearchAnswer is a search's answer from OpenRouter's or GLM's Chat
// Completions: GLM's results in web_search, OpenRouter's in the message's
// annotations.
type chatSearchAnswer struct {
	Choices []struct {
		Message struct {
			Content     any   `json:"content"`
			Annotations []any `json:"annotations"`
		} `json:"message"`
	} `json:"choices"`
	WebSearch []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Content string `json:"content"`
	} `json:"web_search"`
}

func (a chatSearchAnswer) search() Search {
	var s searchFound
	for _, w := range a.WebSearch {
		s.add(w.Title, w.Link, w.Content)
	}
	if len(a.Choices) > 0 {
		m := a.Choices[0].Message
		s.answer.WriteString(textOf(m.Content))
		s.citations(m.Annotations)
	}
	return s.search()
}

// searchFound gathers a search's answer and sources, each source once.
type searchFound struct {
	answer  strings.Builder
	sources []Source
	seen    map[string]int
}

func (s *searchFound) add(title, link, snippet string) {
	if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
		return
	}
	if s.seen == nil {
		s.seen = map[string]int{}
	}
	title, snippet = strings.Join(strings.Fields(title), " "), strings.Join(strings.Fields(snippet), " ")
	if i, ok := s.seen[link]; ok {
		if s.sources[i].Title == "" {
			s.sources[i].Title = title
		}
		if s.sources[i].Snippet == "" {
			s.sources[i].Snippet = clip(snippet, maxSnippet)
		}
		return
	}
	if len(s.sources) == maxSources {
		return
	}
	s.seen[link] = len(s.sources)
	s.sources = append(s.sources, Source{Title: title, URL: link, Snippet: clip(snippet, maxSnippet)})
}

// citations adds the sources of url_citation annotations, as OpenAI's and
// OpenRouter's give them: the citation's fields in it, or in url_citation.
func (s *searchFound) citations(annotations []any) {
	for _, a := range annotations {
		m, _ := a.(map[string]any)
		if m["type"] != "url_citation" {
			continue
		}
		if c, ok := m["url_citation"].(map[string]any); ok {
			m = c
		}
		link, _ := m["url"].(string)
		title, _ := m["title"].(string)
		snippet, _ := m["content"].(string)
		s.add(title, link, snippet)
	}
}

func (s *searchFound) search() Search {
	return Search{Answer: clip(strings.TrimSpace(s.answer.String()), maxSearchAnswer), Sources: s.sources}
}

// responsesSearch is what a Responses API search found: its answer's text
// and citations, then the sources its searches went through.
func responsesSearch(output []any) Search {
	var s searchFound
	var calls []map[string]any
	for _, it := range output {
		item, _ := it.(map[string]any)
		switch item["type"] {
		case "message":
			parts, _ := item["content"].([]any)
			for _, p := range parts {
				part, _ := p.(map[string]any)
				if part["type"] == "output_text" {
					t, _ := part["text"].(string)
					s.answer.WriteString(t)
					anns, _ := part["annotations"].([]any)
					s.citations(anns)
				}
			}
		case "web_search_call":
			calls = append(calls, item)
		}
	}
	for _, c := range calls {
		action, _ := c["action"].(map[string]any)
		sources, _ := action["sources"].([]any)
		for _, src := range sources {
			m, _ := src.(map[string]any)
			link, _ := m["url"].(string)
			title, _ := m["title"].(string)
			s.add(title, link, "")
		}
	}
	return s.search()
}

// anthropicSearch is what a search with Claude's web_search found: the
// answer's text and its citations, then the results.
func anthropicSearch(content []any) Search {
	var s searchFound
	var results []any
	for _, b := range content {
		block, _ := b.(map[string]any)
		switch block["type"] {
		case "text":
			t, _ := block["text"].(string)
			s.answer.WriteString(t)
			cites, _ := block["citations"].([]any)
			for _, c := range cites {
				m, _ := c.(map[string]any)
				link, _ := m["url"].(string)
				title, _ := m["title"].(string)
				cited, _ := m["cited_text"].(string)
				s.add(title, link, cited)
			}
		case "web_search_tool_result":
			if list, ok := block["content"].([]any); ok {
				results = append(results, list...)
			}
		}
	}
	for _, r := range results {
		m, _ := r.(map[string]any)
		link, _ := m["url"].(string)
		title, _ := m["title"].(string)
		s.add(title, link, "")
	}
	return s.search()
}

// clip cuts s to n characters, an ellipsis ending one it cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
