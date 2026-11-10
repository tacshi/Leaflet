package agents

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"leaflet/internal/doc"
	"leaflet/internal/web"
	"leaflet/locales"
)

const MaxRounds = 12

var draftEvery = 80 * time.Millisecond

type Request struct {
	JobID          string
	PageID         string
	PageTitle      string
	Surface        string
	Instruction    string
	ProviderID     string
	Model          string
	AgentName      string
	SupportsTools  bool
	FocusBlockIDs  []string
	SelectionText  string
	ConversationID string
	CommentID      string
	History        []Turn
	KeptToPage     bool
	ReadOthers     bool
	AllowCrossPage bool
	Web            bool
	Thread         []ThreadComment
	OpenPage       *OpenPage
}

type OpenPage struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Shared bool   `json:"shared,omitempty"`
}

type Turn struct {
	Role                string
	Content             string
	KeptToPage, Guarded bool
}

type ThreadComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

const (
	maxRequestText = 60_000
	maxAgentName   = 1_000
)

func (r Request) JobRequest() map[string]any {
	m := map[string]any{"instruction": clip(r.Instruction, maxRequestText), "allow_cross_page": r.AllowCrossPage,
		"agent_name": clip(r.AgentName, maxAgentName), "allow_web": r.Web}
	if r.SelectionText != "" {
		m["selection_text"] = clip(r.SelectionText, maxRequestText)
	}
	if r.OpenPage != nil {
		m["open_page_id"] = r.OpenPage.ID
	}
	return m
}

type Events struct {
	Status   func(string)
	Op       func(op doc.Op, summary string)
	Text     func(string)
	Revise   func(at int, op *doc.Op, summary string, delta []doc.Op)
	Activity func(string)
}

type Result struct {
	Reply      string
	Edits      []Edit
	Ops        []doc.Op
	KeptToPage bool
	Guarded    bool
	ReadOthers bool
}

var (
	thinkRe = regexp.MustCompile(`(?is)<think>.*?</think>`)
	openRe  = regexp.MustCompile(`(?i)<think>`)
	closeRe = regexp.MustCompile(`(?i)</think>`)
)

func withoutThinking(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	if at := closeRe.FindAllStringIndex(s, -1); len(at) > 0 {
		s = s[at[len(at)-1][1]:]
	}
	if at := openRe.FindStringIndex(s); at != nil {
		s = s[:at[0]]
	}
	return strings.TrimSpace(s)
}

func shownText(s string) string {
	s = withoutThinking(s)
	for n := len("<think>") - 1; n > 0; n-- {
		if len(s) >= n && strings.EqualFold(s[len(s)-n:], "<think>"[:n]) {
			s = s[:len(s)-n]
			break
		}
	}
	return strings.TrimSpace(s)
}

func RunLocal(ctx context.Context, client Model, req Request, tc ToolContext, ev Events) (Result, error) {
	tc.Request = req.Instruction
	pagesBefore := len(req.History) > 0 && !req.KeptToPage
	crossPage := tc.ListPages != nil || tc.ReadOtherPage != nil || tc.Workspace != nil && tc.Workspace.Reading
	webBefore := slices.ContainsFunc(req.History, func(t Turn) bool { return t.Guarded })
	if req.Web && req.SupportsTools && !pagesBefore {
		if tc.ReadWeb == nil {
			tc.ReadWeb = func(link string) (web.Page, error) { return web.Read(ctx, link) }
		}
		if s, ok := client.(Searcher); ok && tc.SearchWeb == nil && s.CanSearch() {
			tc.SearchWeb = func(q string) (Search, error) { return s.SearchWeb(ctx, req.Model, q) }
		}
	} else {
		tc.ReadWeb, tc.SearchWeb = nil, nil
	}
	if tc.Captions == nil {
		tc.Captions = func(v doc.Video) (web.Captions, error) { return web.ReadCaptions(ctx, v) }
	}
	exec := &Executor{Ctx: tc, OnOp: ev.Op, OnRevise: ev.Revise, armed: pagesBefore, readOthers: req.ReadOthers}
	if webBefore {
		exec.saw()
	}
	exec.begin()
	held := pagesBefore && crossPage
	result := func(reply string) Result {
		return Result{Reply: reply, Edits: exec.Edits, Ops: exec.Ops, KeptToPage: !exec.readPages && !held, Guarded: exec.Guarded(), ReadOthers: exec.readOthers}
	}
	if ev.Status != nil {
		ev.Status("running")
	}
	views := tc.Views(tc.Content)
	var focus []map[string]any
	for _, v := range views {
		for _, id := range req.FocusBlockIDs {
			if v["id"] == id {
				focus = append(focus, v)
			}
		}
	}
	system, user := SystemPrompt(req.AgentName, req.Surface, tc.ListPages != nil, req.SupportsTools, tc.ReadOnly, tc.ReadWeb != nil, tc.SearchWeb != nil, tc.CreatePage != nil), userMessage(req, views, focus)
	if tc.Workspace != nil {
		system, user = WorkspacePrompt(req.AgentName, tc.Workspace.Reading, req.SupportsTools, tc.ReadWeb != nil, tc.SearchWeb != nil), workspaceMessage(req)
	}
	messages := []map[string]any{{"role": "system", "content": system}}
	for _, t := range req.History {
		if pagesBefore && !crossPage && !t.KeptToPage {
			continue
		}
		messages = append(messages, map[string]any{"role": t.Role, "content": t.Content})
	}
	messages = append(messages, map[string]any{"role": "user", "content": user})
	var tools []map[string]any
	if req.SupportsTools {
		tools = ToolDefinitions(tc.ListPages != nil, tc.ReadOnly)
		if tc.CreatePage == nil {
			tools = slices.DeleteFunc(tools, func(d map[string]any) bool { return d["function"].(map[string]any)["name"] == "create_page" })
		}
		if tc.Workspace != nil {
			tools = WorkspaceToolDefinitions(tc.Workspace.Reading)
		}
		if tc.ReadWeb != nil {
			tools = append(tools, WebToolDefinitions(tc.SearchWeb != nil)...)
		}
	}
	sm, streams := client.(StreamingModel)
	streams = streams && (ev.Text != nil || ev.Revise != nil)
	answer := func() (res ChatResult, drafted map[int]madeCall, err error) {
		drafted = map[int]madeCall{}
		if !streams {
			res, err = client.Chat(ctx, req.Model, messages, tools, 0)
			return res, drafted, err
		}
		current, name, args, drafting := -1, "", "", true
		var shown time.Time
		on := &Streamed{Text: ev.Text, Call: func(i int, n, a string) {
			if i != current {
				if current >= 0 {
					out, ok := exec.FinishDraft(name, args)
					if ok {
						drafted[current] = madeCall{out, exec.Refused}
					}
					drafting = drafting && ok
				}
				current, name = i, n
				drafting = drafting && drafts[n] && i < maxRoundCalls && !exec.endsHere()
			}
			args = a
			if drafting && time.Since(shown) >= draftEvery {
				shown = time.Now()
				exec.Draft(name, a)
			}
		}}
		res, err = sm.ChatStream(ctx, req.Model, messages, tools, 0, on)
		switch {
		case err != nil && !errors.Is(ctx.Err(), context.Canceled),
			err == nil && res.FinishReason == "length":
			exec.DropDraft()
		case err == nil && current >= 0 && current < len(res.ToolCalls):
			if out, ok := exec.FinishDraft(name, res.ToolCalls[current].Arguments); ok {
				drafted[current] = madeCall{out, exec.Refused}
			}
		}
		return res, drafted, err
	}
	for round := 0; round < MaxRounds; round++ {
		made := len(exec.Ops)
		res, drafted, err := answer()
		if wait, ok := retryable(err); ok && len(exec.Ops) == made {
			log.Printf("agent model: %v; asking again in %v", err, wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
				res, drafted, err = answer()
			}
		}
		if err != nil {
			return result(""), err
		}
		cut := res.FinishReason == "length"
		if len(res.ToolCalls) == 0 {
			if req.Surface == "comment" && exec.endsHere() {
				return result(""), errLinkedStop
			}
			reply := withoutThinking(res.Content)
			if cut {
				reply = strings.TrimSpace(reply + "\n\n" + locales.AgentReplyCutOff())
			}
			return result(reply), nil
		}
		messages = append(messages, res.AssistantMessage())
		for i, call := range res.ToolCalls {
			if err := ctx.Err(); err != nil {
				return result(""), err
			}
			made, ok := drafted[i]
			switch {
			case ok:
			case cut && i == len(res.ToolCalls)-1:
				made = madeCall{map[string]any{"error": errCallCutOff}, errCallCutOff}
			case i >= maxRoundCalls:
				tooMany := fmt.Sprintf(errTooManyCalls, maxRoundCalls)
				made = madeCall{map[string]any{"error": tooMany}, tooMany}
			default:
				if a := activity(call.Name, call.Arguments); a != "" && ev.Activity != nil {
					ev.Activity(a)
				}
				made = madeCall{exec.CallRaw(call.Name, call.Arguments), exec.Refused}
			}
			if made.refused != "" {
				log.Printf("agent tool %s refused: %s", call.Name, made.refused)
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": cutResult(resultJSON(made.out))})
			if exec.ended {
				return result(""), errLinkedStop
			}
		}
	}
	return result(locales.AgentStoppedAfterRounds(MaxRounds)), nil
}

type madeCall struct {
	out     map[string]any
	refused string
}

const toolResultLimit = 60_000

func cutResult(s string) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= toolResultLimit {
		return s
	}
	return string(utf16.Decode(units[:toolResultLimit])) + "…(truncated)"
}

func activity(name, raw string) string {
	var args struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	_ = json.Unmarshal([]byte(raw), &args)
	switch name {
	case "web_search":
		return locales.AgentSearchingWeb(short(strings.TrimSpace(args.Query)))
	case "read_web_page":
		if u, err := url.Parse(strings.TrimSpace(args.URL)); err == nil && u.Hostname() != "" {
			return locales.AgentReadingWebPage(strings.TrimPrefix(u.Hostname(), "www."))
		}
		return locales.AgentReadingWeb()
	case "read_video_transcript":
		return locales.AgentReadingVideo()
	}
	return ""
}

const inlinePageLimit = 40_000

func userMessage(req Request, views, focus []map[string]any) string {
	parts := []string{"Page title: " + asData(req.PageTitle)}
	if page := asData(views); len(utf16.Encode([]rune(page))) <= inlinePageLimit || !req.SupportsTools {
		parts = append(parts, "Current page blocks (JSON):\n"+page)
	} else {
		parts = append(parts, "The page is long: call read_page to read it, in parts.")
	}
	if len(focus) > 0 {
		parts = append(parts, "Focus blocks (JSON):\n"+asData(focus))
	}
	if req.SelectionText != "" {
		parts = append(parts, "Selected text (JSON):\n"+asData(clip(req.SelectionText, maxRequestText)))
	}
	if len(req.Thread) > 0 {
		parts = append(parts, "Comment thread (JSON):\n"+asData(req.Thread))
	}
	parts = append(parts, "Request:\n"+req.Instruction)
	return strings.Join(parts, "\n\n")
}

func workspaceMessage(req Request) string {
	var parts []string
	if req.OpenPage != nil {
		parts = append(parts, "Open page (JSON): "+asData(req.OpenPage))
	}
	return strings.Join(append(parts, "Request:\n"+req.Instruction), "\n\n")
}

func asData(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "\u0085", `\u0085`)
}

const maxRoundCalls = 50

const errTooManyCalls = "This call was not made: one answer makes at most %d calls. Make it again in your next."

var retryWait, maxRetryWait = 2 * time.Second, 30 * time.Second

func retryable(err error) (time.Duration, bool) {
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusTooManyRequests && api.Status < http.StatusInternalServerError {
		return 0, false
	}
	wait := cmp.Or(api.RetryAfter, retryWait)
	return wait, wait <= maxRetryWait
}

const errCallCutOff = "Your answer reached your output length limit, which cut this call off: it was not made. Make it again in smaller calls, a few blocks each."
