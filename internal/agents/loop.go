package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
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
	Surface        string // inline | selection | sidebar | comment
	Instruction    string
	ProviderID     string
	Model          string
	AgentName      string
	SupportsTools  bool
	FocusBlockIDs  []string
	SelectionText  string
	ConversationID string
	CommentID      string
	// History holds earlier turns of the conversation: {role, content}.
	History        []map[string]string
	AllowCrossPage bool
}

// JobRequest is the request as ai_jobs.request stores it.
func (r Request) JobRequest() map[string]any {
	m := map[string]any{"instruction": r.Instruction, "allow_cross_page": r.AllowCrossPage, "agent_name": r.AgentName}
	if r.SelectionText != "" {
		m["selection_text"] = r.SelectionText
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
}

// Result is how a run ended.
type Result struct {
	Reply string
	Edits []Edit
	Ops   []doc.Op
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// shownText is an answer as far as it has come, as it can be shown: its
// thinking left out, finished or not.
func shownText(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	if i := strings.Index(s, "<think>"); i >= 0 {
		s = s[:i]
	}
	// The start of a tag still coming.
	for n := len("<think>") - 1; n > 0; n-- {
		if strings.HasSuffix(s, "<think>"[:n]) {
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
	exec := &Executor{Ctx: tc, OnOp: ev.Op, OnRevise: ev.Revise}
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
	var parts []string
	if note := FocusNote(focus, req.SelectionText); note != "" {
		parts = append(parts, note)
	}
	if !req.SupportsTools {
		raw, _ := json.Marshal(views)
		parts = append(parts, "Page content:\n"+string(raw))
	}
	parts = append(parts, req.Instruction)
	messages := []map[string]any{{"role": "system", "content": SystemPrompt(req.AgentName, req.PageTitle, req.Surface, tc.ListPages != nil, req.SupportsTools, tc.ReadOnly)}}
	for _, t := range req.History {
		messages = append(messages, map[string]any{"role": t["role"], "content": t["content"]})
	}
	messages = append(messages, map[string]any{"role": "user", "content": strings.Join(parts, "\n\n")})
	var tools []map[string]any
	if req.SupportsTools {
		tools = ToolDefinitions(tc.ListPages != nil, tc.ReadOnly)
	}
	sm, streams := client.(StreamingModel)
	streams = streams && (ev.Text != nil || ev.Revise != nil)
	for round := 0; round < MaxRounds; round++ {
		var res ChatResult
		var err error
		// The round's calls that write blocks go in the page as they
		// stream, while the calls before them did too, so they still
		// run in turn: drafted are the results of those that did.
		drafted := map[int]map[string]any{}
		if streams {
			current, name, args, drafting := -1, "", "", true
			var shown time.Time
			on := &Streamed{Text: ev.Text, Call: func(i int, n, a string) {
				if i != current {
					// The call before has all come.
					if current >= 0 {
						out, ok := exec.FinishDraft(name, args)
						if ok {
							drafted[current] = out
						}
						drafting = drafting && ok
					}
					current, name = i, n
					drafting = drafting && drafts[n]
				}
				args = a
				if drafting && time.Since(shown) >= draftEvery {
					shown = time.Now()
					exec.Draft(name, a)
				}
			}}
			res, err = sm.ChatStream(ctx, req.Model, messages, tools, 0, on)
			switch {
			case err != nil && !errors.Is(ctx.Err(), context.Canceled):
				// A call cut off is no call: what it put in goes, a call
				// the run's deadline cut off too. One the user stopped
				// stays, as edits made before do.
				exec.DropDraft()
			case err == nil && current >= 0 && current < len(res.ToolCalls):
				if out, ok := exec.FinishDraft(name, res.ToolCalls[current].Arguments); ok {
					drafted[current] = out
				}
			}
		} else {
			res, err = client.Chat(ctx, req.Model, messages, tools, 0)
		}
		if err != nil {
			return Result{Edits: exec.Edits, Ops: exec.Ops}, err
		}
		if len(res.ToolCalls) == 0 {
			reply := strings.TrimSpace(thinkRe.ReplaceAllString(res.Content, ""))
			return Result{Reply: reply, Edits: exec.Edits, Ops: exec.Ops}, nil
		}
		messages = append(messages, res.AssistantMessage())
		for i, call := range res.ToolCalls {
			out, ok := drafted[i]
			if !ok {
				out = exec.CallRaw(call.Name, call.Arguments)
			}
			if e, ok := out["error"].(string); ok {
				// Where it was asked to write, not what: the page's text
				// stays out of the log.
				var args struct {
					Position json.RawMessage `json:"position"`
				}
				_ = json.Unmarshal([]byte(call.Arguments), &args)
				log.Printf("agent tool %s refused: %s (position %s)", call.Name, e, args.Position)
			}
			raw, _ := json.Marshal(out)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": string(raw)})
		}
	}
	return Result{
		Reply: fmt.Sprintf("I stopped after %d rounds of edits. Ask me to continue if there is more to do.", MaxRounds),
		Edits: exec.Edits, Ops: exec.Ops,
	}, nil
}

// Streamer is the server's client as RunCloud uses it: *remote.Client.
type Streamer interface {
	InvokeStream(ctx context.Context, name string, body map[string]any, fn func(map[string]any)) error
	SelectOne(ctx context.Context, table string, q remote.Query) (map[string]any, error)
}

// errCutOff is a run whose stream ended before the server said how.
var errCutOff = errors.New("The connection to the agent ended before it finished.") //nolint:staticcheck // shown to people as a sentence.

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
			summary, _ := e["summary"].(string)
			if summary == "" {
				summary = "Edited the page"
			}
			res.Ops = append(res.Ops, op)
			res.Edits = append(res.Edits, Edit{Summary: summary, BlockIDs: OpBlockIDs(op)})
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
		case "done":
			res.Reply, _ = e["reply"].(string)
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
		Edits []Edit   `json:"edits"`
	} `json:"result"`
}

// result is how a run whose stream was cut off ended, by its job: the
// server's ops and edits are all of the run's, those streamed before the
// cut among them.
func (j cloudJob) result(streamed Result) (Result, error) {
	res := streamed
	if len(j.Result.Ops) >= len(res.Ops) {
		res.Ops, res.Edits = j.Result.Ops, j.Result.Edits
	}
	if j.Status == "failed" {
		msg := j.Error
		if msg == "" {
			msg = errCutOff.Error()
		}
		return res, errors.New(msg)
	}
	res.Reply = j.Result.Reply
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
