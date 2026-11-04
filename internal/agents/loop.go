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
	"leaflet/internal/remote"
	"leaflet/internal/web"
	"leaflet/locales"
)

// MaxRounds bounds the tool rounds of one run.
const MaxRounds = 12

// draftEvery spaces the page's updates as a call's blocks stream in;
// tests shorten it.
var draftEvery = 80 * time.Millisecond

// Request is one request to an agent, from any surface.
type Request struct {
	JobID          string
	PageID         string
	PageTitle      string
	Surface        string // inline | selection | sidebar | comment | workspace
	Instruction    string
	ProviderID     string
	Model          string
	AgentName      string
	SupportsTools  bool
	FocusBlockIDs  []string
	SelectionText  string
	ConversationID string
	CommentID      string
	// History holds earlier turns of the conversation.
	History []Turn
	// KeptToPage is a History whose runs read nothing beyond their page:
	// this run may then use the web (RunLocal).
	KeptToPage     bool
	AllowCrossPage bool
	// Web is a run that may search and read the web.
	Web bool
	// Thread is the comment thread a comment run answers in, its first
	// comment first.
	Thread []ThreadComment
	// OpenPage is the page open as a workspace run was asked for, which
	// its agent is told of; nil for none, or one not the user's.
	OpenPage *OpenPage
	// PublicPages are the pages the app has a public link open to, which a
	// cloud workspace run changes nothing of, as of a page shared.
	PublicPages []string
}

// OpenPage is the page open as a workspace run is asked for, as its agent
// is told of it.
type OpenPage struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Shared bool   `json:"shared,omitempty"`
}

// Turn is a message of an earlier turn of a conversation, the user's
// request or the agent's reply, as the model reads it.
type Turn struct {
	Role    string
	Content string
	// KeptToPage is a turn of a run that read nothing beyond its page,
	// and Guarded one whose URL guard was on: it read beyond its page or
	// the web, or followed turns that did.
	KeptToPage, Guarded bool
}

// ThreadComment is a comment of the thread a run answers in, as the model
// reads it.
type ThreadComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// JobRequest is the request as ai_jobs.request stores it.
func (r Request) JobRequest() map[string]any {
	m := map[string]any{"instruction": r.Instruction, "allow_cross_page": r.AllowCrossPage, "agent_name": r.AgentName, "allow_web": r.Web}
	if r.SelectionText != "" {
		m["selection_text"] = r.SelectionText
	}
	// The server finds the rest of it, as it finds a page's.
	if r.OpenPage != nil {
		m["open_page_id"] = r.OpenPage.ID
	}
	if len(r.PublicPages) > 0 {
		m["public_page_ids"] = r.PublicPages
	}
	return m
}

// Events of a run.
type Events struct {
	Status func(string)
	Op     func(op doc.Op, summary string)
	// Text is the model's answer so far, as it streams in: a round of
	// tool calls starts it again.
	Text func(string)
	// Revise changes the op at at, as blocks go in while the call that
	// writes them streams: delta is what it changes in the page. A nil
	// op takes it away, the last.
	Revise func(at int, op *doc.Op, summary string, delta []doc.Op)
	// Activity is what the run is doing while a call takes a while, as
	// searching the web, said in the app's language.
	Activity func(string)
}

// Result is how a run ended.
type Result struct {
	Reply string
	Edits []Edit
	Ops   []doc.Op
	// KeptToPage is a run that read nothing beyond its page.
	KeptToPage bool
	// Guarded is a run whose URL guard was on as it ended (Executor.
	// Guarded): its reply may carry what it read beyond its page or on
	// the web.
	Guarded bool
}

// Models that think aloud write their thinking in <think> tags, in any
// case: thinkRe finds a whole block of it, and openRe and closeRe a tag of
// one left open or closed alone.
var (
	thinkRe = regexp.MustCompile(`(?is)<think>.*?</think>`)
	openRe  = regexp.MustCompile(`(?i)<think>`)
	closeRe = regexp.MustCompile(`(?i)</think>`)
)

// withoutThinking is an answer with the model's thinking left out: each
// block of it; all before a closing tag that none opened, as models whose
// chat template opens the tag write it (DeepSeek-R1's distills); and all
// after an opening tag the answer ends without closing.
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

// shownText is an answer as far as it has come, as it can be shown: its
// thinking left out, finished or not.
func shownText(s string) string {
	s = withoutThinking(s)
	// The start of a tag still coming.
	for n := len("<think>") - 1; n > 0; n-- {
		if len(s) >= n && strings.EqualFold(s[len(s)-n:], "<think>"[:n]) {
			s = s[:len(s)-n]
			break
		}
	}
	return strings.TrimSpace(s)
}

// RunLocal runs the loop on this device against a local model, so agents
// on them work offline. The tools edit ctx.Content.
func RunLocal(ctx context.Context, client Model, req Request, tc ToolContext, ev Events) (Result, error) {
	tc.Request = req.Instruction
	// Earlier turns of the conversation may hold what a run read on
	// another page: this one has no web, and what it writes keeps to the
	// URLs the user gave, as after reading one (checkURLs). Where it may no
	// longer read other pages, as the page is shared now, the turns of
	// those runs are left out: what they read would be written where
	// others read it.
	pagesBefore := len(req.History) > 0 && !req.KeptToPage
	crossPage := tc.ListPages != nil || tc.ReadOtherPage != nil || tc.Workspace != nil && tc.Workspace.Reading
	// Earlier turns that read the web may hold what they read there: the
	// run keeps to the URLs it may write after reading the web from its
	// start, as one that read it itself does, and has found none yet.
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
	// A video's captions are read from its site, whose link is on the
	// page, with or without the web.
	if tc.Captions == nil {
		tc.Captions = func(v doc.Video) (web.Captions, error) { return web.ReadCaptions(ctx, v) }
	}
	exec := &Executor{Ctx: tc, OnOp: ev.Op, OnRevise: ev.Revise, armed: pagesBefore}
	if webBefore {
		exec.saw()
	}
	exec.begin()
	result := func(reply string) Result {
		return Result{Reply: reply, Edits: exec.Edits, Ops: exec.Ops, KeptToPage: !exec.readPages, Guarded: exec.Guarded()}
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
		// No page of the user's own from a page someone else writes or
		// reads (ToolContext.CreatePage).
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
	// answer asks the model for the round's answer. The round's calls that
	// write blocks go in the page as they stream, while the calls before
	// them did too, so they still run in turn: drafted are those that did.
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
				// The call before has all come.
				if current >= 0 {
					out, ok := exec.FinishDraft(name, args)
					if ok {
						drafted[current] = madeCall{out, exec.Refused}
					}
					drafting = drafting && ok
				}
				current, name = i, n
				// A call past the most an answer makes does not run.
				drafting = drafting && drafts[n] && i < maxRoundCalls
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
			// A call cut off is no call: what it put in goes, a call the
			// run's deadline or the model's length limit cut off too. One
			// the user stopped stays, as edits made before do.
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
		// A model that was busy or failing is asked once more, after a
		// while, when its answer put nothing in the page: the calls that
		// did would come again.
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
		// The model stopped at the most it may write at once: its answer
		// says so, and its last call, cut off, is not made.
		cut := res.FinishReason == "length"
		if len(res.ToolCalls) == 0 {
			reply := withoutThinking(res.Content)
			if cut {
				reply = strings.TrimSpace(reply + "\n\n" + locales.AgentReplyCutOff())
			}
			return result(reply), nil
		}
		messages = append(messages, res.AssistantMessage())
		for i, call := range res.ToolCalls {
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
				// Why, in the code's own words: the call's arguments, and
				// what a refusal says of them, a URL the run may not write
				// say, stay out of the log.
				log.Printf("agent tool %s refused: %s", call.Name, made.refused)
			}
			raw, _ := json.Marshal(made.out)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": cutResult(string(raw))})
		}
	}
	return result(locales.AgentStoppedAfterRounds(MaxRounds)), nil
}

// madeCall is a tool call made: its result, and why it was refused, ""
// when it was not (Executor.Refused).
type madeCall struct {
	out     map[string]any
	refused string
}

// toolResultLimit is the longest tool result a model reads, in UTF-16
// units of its JSON, as on the server: a page of embeds, or many pages
// listed, would be more than a model reads at once.
const toolResultLimit = 60_000

// cutResult is a tool's result as the model reads it, cut off past
// toolResultLimit as the server cuts it.
func cutResult(s string) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= toolResultLimit {
		return s
	}
	return string(utf16.Decode(units[:toolResultLimit])) + "…(truncated)"
}

// activity says what a call that takes a while does, for the reply to
// show while it works: "" for a call that does not.
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

// inlinePageLimit is the longest page, in UTF-16 units of its blocks as
// JSON, the request carries, as on the server; a longer one the model
// reads with read_page.
const inlinePageLimit = 40_000

// userMessage is the user's turn of a run: what others may have written,
// the page's title, its blocks, the focus, the selection and the comment
// thread, as JSON on one line each, and last the request, the user's own
// words, as the server's runs have it (prompt.ts userMessage). None of the
// rest can break out into a line of its own, a "Request:" of its own say,
// nor, in the system prompt, be taken as the app's. A model that cannot
// read the page with a tool gets it whatever its length.
func userMessage(req Request, views, focus []map[string]any) string {
	parts := []string{"Page title: " + asData(req.PageTitle)}
	if page := asData(views); len(utf16.Encode([]rune(page))) <= inlinePageLimit || !req.SupportsTools {
		parts = append(parts, "Current page blocks (JSON):\n"+page)
	} else {
		parts = append(parts, "The page is long; call read_page to see all of it.")
	}
	if len(focus) > 0 {
		parts = append(parts, "Focus blocks (JSON):\n"+asData(focus))
	}
	if req.SelectionText != "" {
		parts = append(parts, "Selected text (JSON):\n"+asData(req.SelectionText))
	}
	// An "@agent answer Sam's question above" needs Sam's question.
	if len(req.Thread) > 0 {
		parts = append(parts, "Comment thread (JSON):\n"+asData(req.Thread))
	}
	parts = append(parts, "Request:\n"+req.Instruction)
	return strings.Join(parts, "\n\n")
}

// workspaceMessage is the user's turn of a workspace run: the page open as
// they asked, as data, and the request, as the server's runs have it
// (prompt.ts userMessage).
func workspaceMessage(req Request) string {
	var parts []string
	if req.OpenPage != nil {
		parts = append(parts, "Open page (JSON): "+asData(req.OpenPage))
	}
	return strings.Join(append(parts, "Request:\n"+req.Instruction), "\n\n")
}

// asData is v as JSON on one line, as the server's asData has it: the line
// separators JSON leaves as they are, which models read as breaks, are
// escaped too.
func asData(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "\u0085", `\u0085`)
}

// maxRoundCalls bounds the tool calls of one answer that run: those past
// it are refused, for the model to make in its next.
const maxRoundCalls = 50

// errTooManyCalls is what the model is told of a call past maxRoundCalls.
const errTooManyCalls = "This call was not made: one answer makes at most %d calls. Make it again in your next."

// retryWait is how long a run waits to ask a model that was busy or
// failing again, when it did not say; maxRetryWait is the longest it
// waits, past which it does not ask again. Tests shorten them.
var retryWait, maxRetryWait = 2 * time.Second, 30 * time.Second

// retryable reports whether a model's failure is worth asking again for,
// once, and after how long: the model was busy (429) or failing (5xx),
// and asked for no longer a wait than maxRetryWait.
func retryable(err error) (time.Duration, bool) {
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusTooManyRequests && api.Status < http.StatusInternalServerError {
		return 0, false
	}
	wait := cmp.Or(api.RetryAfter, retryWait)
	return wait, wait <= maxRetryWait
}

// errCallCutOff is what the model is told of a call its length limit cut
// off, for it to make it again in parts rather than the same way.
const errCallCutOff = "Your answer reached your output length limit, which cut this call off: it was not made. Make it again in smaller calls, a few blocks each."

// Streamer is the server's client as RunCloud uses it: *remote.Client.
type Streamer interface {
	InvokeStream(ctx context.Context, name string, body map[string]any, fn func(map[string]any)) error
	SelectOne(ctx context.Context, table string, q remote.Query) (map[string]any, error)
}

// errCutOff is a run whose stream ended before the server said how, said
// in the app's language.
var errCutOff error = errStreamCutOff{}

type errStreamCutOff struct{}

func (errStreamCutOff) Error() string { return locales.AgentConnectionEnded() }

// RunCloud runs the loop on the server, whose Vault keeps the model's key.
// The job row must exist (docs/protocol.md §4); ops come as the server
// applies them.
func RunCloud(ctx context.Context, be Streamer, jobID string, ev Events) (Result, error) {
	var res Result
	var failure error
	done := false
	// The answer of the server's round of the model, as far as it came.
	var text strings.Builder
	round := -1.0
	err := be.InvokeStream(ctx, "agent", map[string]any{"job_id": jobID}, func(e map[string]any) {
		switch e["type"] {
		case "status":
			if ev.Status != nil {
				ev.Status(fmt.Sprint(e["status"]))
			}
		case "op":
			raw, _ := json.Marshal(e["op"])
			var op doc.Op
			if json.Unmarshal(raw, &op) != nil {
				return
			}
			// A page made keeps its content itself: the run's chat, kept
			// here, keeps none of it, for Undo takes the page away by its ID.
			op.Content = nil
			// Said from the op, in the app's language, as a local run's
			// edit, not as the server words it.
			tool, _ := e["tool"].(string)
			edit := opEdit(op, tool)
			summary := edit.Summary
			res.Ops = append(res.Ops, op)
			res.Edits = append(res.Edits, edit)
			if ev.Op != nil {
				ev.Op(op, summary)
			}
		case "text":
			s, ok := e["delta"].(string)
			if !ok || ev.Text == nil {
				return
			}
			if r, _ := e["round"].(float64); r != round {
				text.Reset()
				round = r
			}
			text.WriteString(s)
			ev.Text(text.String())
		case "call":
			raw, _ := json.Marshal(e["args"])
			tool, _ := e["tool"].(string)
			if a := activity(tool, string(raw)); a != "" && ev.Activity != nil {
				ev.Activity(a)
			}
			// The server names the calls that read the web: a run that
			// made one may have read it.
			res.Guarded = true
		case "done":
			res.Reply, _ = e["reply"].(string)
			res.KeptToPage, _ = e["kept_to_page"].(bool)
			guarded, _ := e["guarded"].(bool)
			res.Guarded = res.Guarded || guarded || !res.KeptToPage
			done = true
		case "error":
			failure = fmt.Errorf("%v", e["message"])
		}
	})
	if failure != nil {
		return res, failure
	}
	if done {
		return res, nil
	}
	// A stream that ends without done was cut off, and is no success as
	// it is. The server goes on with a run whose caller went, and saves
	// it: one cut off by the network, or by going quiet, ends as the job
	// the server keeps says, with the edits it made after the cut.
	if err == nil || remote.IsNetwork(err) {
		if job, ok := awaitJob(ctx, be, jobID); ok {
			return job.result(res)
		}
	}
	if err == nil {
		err = errCutOff
	}
	return res, err
}

// jobPollEvery spaces the reads of a job whose stream was cut off;
// jobWait is how long a running one is waited for, longer than the
// server lets a run go on, and jobQueuedWait how long one the server
// has not taken yet, as when the request never reached it. Tests
// shorten them.
var jobPollEvery, jobWait, jobQueuedWait = 2 * time.Second, 8 * time.Minute, 30 * time.Second

// cloudJob is a cloud run's ai_jobs row, as far as RunCloud reads it.
type cloudJob struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Result struct {
		Reply string   `json:"reply"`
		Ops   []doc.Op `json:"ops"`
		// KeptToPage is a run that read nothing beyond its page, and
		// Guarded one whose URL guard was on.
		KeptToPage bool `json:"kept_to_page"`
		Guarded    bool `json:"guarded"`
		// An edit for each op, naming the call that made it.
		Edits []struct {
			Tool string `json:"tool"`
		} `json:"edits"`
	} `json:"result"`
}

// result is how a run whose stream was cut off ended, by its job: the
// server's ops are all of the run's, those streamed before the cut among
// them, its edits said from them as streamed ones are.
func (j cloudJob) result(streamed Result) (Result, error) {
	res := streamed
	if len(j.Result.Ops) >= len(res.Ops) {
		res.Ops, res.Edits = j.Result.Ops, nil
		for i, op := range j.Result.Ops {
			// As a streamed op, without the content of a page it made.
			res.Ops[i].Content = nil
			tool := ""
			// One edit an op, as servers that name the tool make them.
			if len(j.Result.Edits) == len(j.Result.Ops) {
				tool = j.Result.Edits[i].Tool
			}
			res.Edits = append(res.Edits, opEdit(op, tool))
		}
	}
	if j.Status == "failed" {
		msg := j.Error
		if msg == "" {
			msg = errCutOff.Error()
		}
		return res, errors.New(msg)
	}
	res.Reply, res.KeptToPage = j.Result.Reply, j.Result.KeptToPage
	// The calls streamed before the cut say the run's guard was on too.
	res.Guarded = streamed.Guarded || j.Result.Guarded || !res.KeptToPage
	return res, nil
}

// awaitJob reads a cloud run's job until the server has ended it, and
// reports whether it did: one still queued after jobQueuedWait never
// started, and one still running after jobWait is given up on.
func awaitJob(ctx context.Context, be Streamer, jobID string) (cloudJob, bool) {
	start := time.Now()
	for {
		row, err := be.SelectOne(ctx, "ai_jobs", remote.Query{Select: "status, error, result", Filters: []string{remote.Eq("id", jobID)}})
		if err == nil && row == nil {
			return cloudJob{}, false // no such job: nothing to wait for
		}
		if err == nil {
			var job cloudJob
			raw, _ := json.Marshal(row)
			if json.Unmarshal(raw, &job) == nil {
				switch job.Status {
				case "done", "failed", "reverted":
					return job, true
				case "queued":
					if time.Since(start) >= jobQueuedWait {
						return cloudJob{}, false
					}
				}
			}
		}
		if time.Since(start) >= jobWait {
			return cloudJob{}, false
		}
		select {
		case <-ctx.Done():
			return cloudJob{}, false
		case <-time.After(jobPollEvery):
		}
	}
}
