package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"leaflet/internal/doc"
)

// MaxRounds bounds the tool rounds of one run.
const MaxRounds = 12

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
	// Text is reply text streamed by the server.
	Text func(string)
}

// Result is how a run ended.
type Result struct {
	Reply string
	Edits []Edit
	Ops   []doc.Op
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// RunLocal runs the loop on this device against a local model, so agents
// on them work offline. The tools edit ctx.Content.
func RunLocal(ctx context.Context, client Model, req Request, tc ToolContext, ev Events) (Result, error) {
	tc.Request = req.Instruction
	exec := &Executor{Ctx: tc, OnOp: ev.Op}
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
	for round := 0; round < MaxRounds; round++ {
		res, err := client.Chat(ctx, req.Model, messages, tools, 0)
		if err != nil {
			return Result{Edits: exec.Edits, Ops: exec.Ops}, err
		}
		if len(res.ToolCalls) == 0 {
			reply := strings.TrimSpace(thinkRe.ReplaceAllString(res.Content, ""))
			return Result{Reply: reply, Edits: exec.Edits, Ops: exec.Ops}, nil
		}
		messages = append(messages, res.AssistantMessage())
		for _, call := range res.ToolCalls {
			out := exec.CallRaw(call.Name, call.Arguments)
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
			if s, ok := e["delta"].(string); ok && ev.Text != nil {
				ev.Text(s)
			}
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
	// A stream that ends without done was cut off, by the network or the
	// server, and is no success.
	if !done {
		if err == nil {
			err = errCutOff
		}
		return res, err
	}
	return res, nil
}
