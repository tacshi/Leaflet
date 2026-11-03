package agents

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/parity"
	"leaflet/internal/store"
)

// The corpora the server's tests write (supabase/functions/_shared/
// corpus_test.ts) hold random cases with what the server's agent function
// makes of them: this side must make the same of each.

// subpageOp is a create_page op as both sides make it: the server's has
// the page its database made, and its blocks, which here go to
// CreateSubpage.
func subpageOp(title, blocks any) map[string]any {
	if blocks == nil {
		blocks = []any{}
	}
	return map[string]any{"op": "create_page", "title": title, "blocks": blocks}
}

// Tool calls make the ops here that they make on the server, are taken
// and refused alike, and leave the page as the server leaves it: a run is
// the same on a device as in the cloud.
func TestCorpusToolCalls(t *testing.T) {
	var cases []struct {
		Before   *doc.Content `json:"before"`
		Cross    bool         `json:"cross"`
		ReadOnly bool         `json:"readOnly"`
		Request  string       `json:"request"`
		Calls    []struct {
			Tool   string   `json:"tool"`
			Args   string   `json:"args"`
			OK     bool     `json:"ok"`
			Result any      `json:"result"`
			Edits  []string `json:"edits"`
		} `json:"calls"`
		Ops   []map[string]any `json:"ops"`
		After struct {
			Document struct {
				Children []any `json:"children"`
			} `json:"document"`
		} `json:"after"`
	}
	parity.Read(t, "calls.json", &cases)
	fail := parity.Check(t)
	for _, tc := range cases {
		known := map[string]bool{}
		for _, n := range tc.Before.All() {
			known[n.ID] = true
		}
		var subpages [][]*doc.Node
		x := &Executor{Ctx: ToolContext{
			Content: tc.Before.Copy(), PageID: "page-1", PageTitle: "Plan", AgentName: "Claude", Request: tc.Request, ReadOnly: tc.ReadOnly,
			CreateSubpage: func(_ string, nodes []*doc.Node) (string, error) {
				subpages = append(subpages, nodes)
				return "page-2", nil
			},
		}}
		if tc.Cross {
			x.Ctx.ListPages = func() []map[string]any { return []map[string]any{} }
			x.Ctx.ReadOtherPage = func(id string) map[string]any {
				return map[string]any{"id": id, "title": "Other", "blocks": []any{}}
			}
		}
		var results, theirs []any
		for _, c := range tc.Calls {
			edits := len(x.Edits)
			out := x.CallRaw(c.Tool, c.Args)
			if _, refused := out["error"]; refused == c.OK {
				fail("%s %s: refused %v (%v), on the server %v", c.Tool, c.Args, refused, out["error"], !c.OK)
			}
			var summaries []string
			for _, e := range x.Edits[edits:] {
				summaries = append(summaries, e.Summary)
			}
			if !slices.Equal(summaries, c.Edits) {
				fail("%s %s: edits %q, the server's %q", c.Tool, c.Args, summaries, c.Edits)
			}
			if c.Result == nil {
				continue
			}
			// What read_page gives besides the blocks is each side's own.
			if c.Tool == "read_page" {
				results = append(results, parity.JSON(t, out["blocks"]))
				theirs = append(theirs, c.Result.(map[string]any)["blocks"])
				continue
			}
			results = append(results, parity.JSON(t, out))
			theirs = append(theirs, c.Result)
		}
		var ops, theirOps []any
		made, _ := parity.JSON(t, x.Ops).([]any)
		for _, o := range made {
			if m := o.(map[string]any); m["op"] == "create_page" {
				o = subpageOp(m["page"].(map[string]any)["title"], parity.JSON(t, subpages[0]))
				subpages = subpages[1:]
			}
			ops = append(ops, o)
		}
		for _, o := range tc.Ops {
			var v any = o
			if o["op"] == "create_page" {
				page, content := o["page"].(map[string]any), o["content"].(map[string]any)
				v = subpageOp(page["title"], content["document"].(map[string]any)["children"])
			}
			theirOps = append(theirOps, v)
		}
		after := parity.JSON(t, x.Ctx.Content.Children)
		if after == nil {
			after = []any{}
		}
		ours := parity.Steady(known, results, ops, after)
		them := parity.Steady(known, theirs, theirOps, any(tc.After.Document.Children))
		for i, what := range []string{"results", "ops", "page"} {
			if got, want := parity.Plain(ours[i]), parity.Plain(them[i]); !reflect.DeepEqual(got, want) {
				calls, _ := json.Marshal(tc.Calls)
				fail("%s of %s\non %s\n%s\nthe server's\n%s", what, calls, parity.Show(tc.Before.Children), parity.Show(got), parity.Show(want))
			}
		}
	}
}

// Agents find the URLs in text where the server's find them.
func TestCorpusURLsInText(t *testing.T) {
	var cases []struct {
		Text string   `json:"text"`
		URLs []string `json:"urls"`
	}
	parity.Read(t, "texts.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := urlRe.FindAllString(c.Text, -1); !slices.Equal(got, c.URLs) {
			fail("%q: URLs %q, the server's %q", c.Text, got, c.URLs)
		}
	}
}

// The user's turn of a run is the server's to the character.
func TestCorpusUserMessages(t *testing.T) {
	var cases []struct {
		Turn struct {
			Instruction   string           `json:"instruction"`
			PageTitle     string           `json:"pageTitle"`
			PageViews     []map[string]any `json:"pageViews"`
			Focus         []map[string]any `json:"focus"`
			SelectionText string           `json:"selectionText"`
			Thread        []ThreadComment  `json:"thread"`
		} `json:"turn"`
		Message string `json:"message"`
	}
	parity.Read(t, "messages.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		req := Request{
			PageTitle: c.Turn.PageTitle, Instruction: c.Turn.Instruction, SelectionText: c.Turn.SelectionText,
			Thread: c.Turn.Thread, SupportsTools: true,
		}
		views := c.Turn.PageViews
		if views == nil {
			views = []map[string]any{}
		}
		if got := userMessage(req, views, c.Turn.Focus); got != c.Message {
			fail("%s made\n%q\nthe server\n%q", parity.Show(c.Turn), got, c.Message)
		}
	}
}

// A recording's transcript reads here as agents read it on the server.
func TestCorpusTranscripts(t *testing.T) {
	var cases []struct {
		Segments []store.Segment `json:"segments"`
		Text     string          `json:"text"`
	}
	parity.Read(t, "transcripts.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := TranscriptText(c.Segments); got != c.Text {
			fail("%+v read as\n%q\nthe server\n%q", c.Segments, got, c.Text)
		}
	}
}

// Ops take away a change made since they were made here where they do on
// the server, which keeps the page in history first.
func TestCorpusOverwrites(t *testing.T) {
	var cases []struct {
		Before     *doc.Content `json:"before"`
		Ops        []doc.Op     `json:"ops"`
		Overwrites bool         `json:"overwrites"`
	}
	parity.Read(t, "ops.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := overwrites(c.Before, c.Ops); got != c.Overwrites {
			fail("%s\non %s: overwrites %v, on the server %v", parity.Show(c.Ops), parity.Show(c.Before.Children), got, c.Overwrites)
		}
	}
}
