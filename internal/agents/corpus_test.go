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

// pageOp is a create_page op as both sides make it: the server's has the
// page its database made, and its blocks, which here go to CreateSubpage
// or CreatePage; inside parent, or at the top level in folder.
func pageOp(title, blocks, parent, folder any) map[string]any {
	if blocks == nil {
		blocks = []any{}
	}
	text := func(v any) any {
		if v == nil {
			return ""
		}
		return v
	}
	return map[string]any{"op": "create_page", "title": title, "blocks": blocks, "parent": text(parent), "folder": text(folder)}
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
		// The blocks of the pages made, in the order they were.
		var made [][]*doc.Node
		x := &Executor{Ctx: ToolContext{
			Content: tc.Before.Copy(), PageID: "page-1", PageTitle: "Plan", AgentName: "Claude", Request: tc.Request, ReadOnly: tc.ReadOnly,
			CreateSubpage: func(_ string, nodes []*doc.Node) (string, error) {
				made = append(made, nodes)
				return "page-2", nil
			},
			// The user has folder-1 alone, as on the server.
			CreatePage: func(_ string, nodes []*doc.Node, folder string) (string, error) {
				if folder != "" && folder != "folder-1" {
					return "", noFolder(folder)
				}
				made = append(made, nodes)
				return "page-3", nil
			},
		}}
		if tc.Cross {
			x.Ctx.ListPages = func() []map[string]any { return []map[string]any{} }
			x.Ctx.ReadOtherPage = func(id string) map[string]any {
				return map[string]any{"id": id, "title": "Other", "blocks": []any{}}
			}
			x.Ctx.ListFolders = func() []map[string]any { return []map[string]any{} }
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
		ours, _ := parity.JSON(t, x.Ops).([]any)
		for _, o := range ours {
			if m := o.(map[string]any); m["op"] == "create_page" {
				page := m["page"].(map[string]any)
				o = pageOp(page["title"], parity.JSON(t, made[0]), page["parent_id"], page["folder_id"])
				made = made[1:]
			}
			ops = append(ops, o)
		}
		for _, o := range tc.Ops {
			var v any = o
			if o["op"] == "create_page" {
				page, content := o["page"].(map[string]any), o["content"].(map[string]any)
				v = pageOp(page["title"], content["document"].(map[string]any)["children"], page["parent_id"], page["folder_id"])
			}
			theirOps = append(theirOps, v)
		}
		after := parity.JSON(t, x.Ctx.Content.Children)
		if after == nil {
			after = []any{}
		}
		mine := parity.Steady(known, results, ops, after)
		them := parity.Steady(known, theirs, theirOps, any(tc.After.Document.Children))
		for i, what := range []string{"results", "ops", "page"} {
			if got, want := parity.Plain(mine[i]), parity.Plain(them[i]); !reflect.DeepEqual(got, want) {
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

// corpusWorld is the user's pages as both sides' corpora have them
// (workspace_test.ts WORLD): a page with a subpage, one shared with a
// friend, one in their folder, one untitled, one others read by its public
// link; p-9 is someone else's.
var corpusWorld = []WorkspacePage{
	{ID: "p-1", Title: "Plan", Tags: []string{"plan"}},
	{ID: "p-2", Title: "Notes", ParentID: "p-1", Tags: []string{}},
	{ID: "p-3", Title: "Shared", Tags: []string{}, Shared: true, People: true},
	{ID: "p-4", Title: "Filed", FolderID: "f-1", Tags: []string{}},
	{ID: "p-5", Title: "Untitled", Tags: []string{}, Untitled: true},
	{ID: "p-6", Title: "Linked", Tags: []string{}, Shared: true},
}

// corpusWorkspace is the workspace's hooks over corpusWorld, which they
// leave as it is, as the server's corpus has them; made gets the blocks
// of the pages made.
func corpusWorkspace(reading bool, made *[][]*doc.Node) *Workspace {
	page := func(id string) (WorkspacePage, bool) {
		for _, p := range corpusWorld {
			if p.ID == id {
				return p, true
			}
		}
		return WorkspacePage{}, false
	}
	folders := func() []map[string]any { return []map[string]any{{"id": "f-1", "name": "Clippings"}} }
	return &Workspace{
		Reading: reading,
		Pages:   func() ([]WorkspacePage, error) { return corpusWorld, nil },
		Page:    page,
		ReadPage: func(id string) (map[string]any, error) {
			p, _ := page(id)
			return map[string]any{"page": map[string]any{"id": id, "title": p.Title}, "blocks": []any{}, "subpages": []any{}}, nil
		},
		Search: func(string) ([]map[string]any, error) {
			return []map[string]any{{"page_id": "p-1", "title": "Plan"}, {"page_id": "p-3", "title": "Shared", "snippet": "What they wrote"}}, nil
		},
		Folders: folders,
		CreatePage: func(_ string, nodes []*doc.Node, _, folder string) (doc.NewPage, error) {
			if folder != "" && folder != "f-1" {
				return doc.NewPage{}, noFolder(folder)
			}
			*made = append(*made, nodes)
			return doc.NewPage{ID: "page-new"}, nil
		},
		MovePage: func(id, parent, folder string) (from, to doc.PagePlace, err error) {
			if folder != "" && folder != "f-1" {
				return from, to, noFolder(folder)
			}
			if id == "p-1" && parent == "p-2" {
				return from, to, store.ErrMoveIntoSelf
			}
			p, _ := page(id)
			return doc.PagePlace{ParentID: p.ParentID, FolderID: p.FolderID}, doc.PagePlace{ParentID: parent, FolderID: folder}, nil
		},
		RenamePage: func(string, string) error { return nil },
		SetTags:    func(_ string, tags []string) ([]string, error) { return store.NormalizeTags(tags), nil },
		CreateFolder: func(name string) (doc.NewFolder, error) {
			return doc.NewFolder{ID: "f-new", SpaceID: "space-0", Name: name, SortKey: "a1"}, nil
		},
	}
}

// A workspace run's calls make the ops here that they make on the server,
// are taken and refused alike, and answer alike.
func TestCorpusWorkspaceCalls(t *testing.T) {
	var cases []struct {
		Reading bool   `json:"reading"`
		Request string `json:"request"`
		Calls   []struct {
			Tool   string   `json:"tool"`
			Args   string   `json:"args"`
			OK     bool     `json:"ok"`
			Result any      `json:"result"`
			Edits  []string `json:"edits"`
		} `json:"calls"`
		Ops []map[string]any `json:"ops"`
	}
	parity.Read(t, "workspace_calls.json", &cases)
	fail := parity.Check(t)
	for _, tc := range cases {
		var made [][]*doc.Node
		x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, AgentName: "Claude", Request: tc.Request,
			Workspace: corpusWorkspace(tc.Reading, &made)}}
		var results, theirs []any
		for _, c := range tc.Calls {
			edits := len(x.Edits)
			out := x.CallRaw(c.Tool, c.Args)
			if _, refused := out["error"]; refused == c.OK {
				fail("%s %s: refused %v (%v), on the server %v", c.Tool, c.Args, refused, out["error"], !c.OK)
				continue
			}
			var summaries []string
			for _, e := range x.Edits[edits:] {
				summaries = append(summaries, e.Summary)
			}
			if !slices.Equal(summaries, c.Edits) {
				fail("%s %s: edits %q, the server's %q", c.Tool, c.Args, summaries, c.Edits)
			}
			if c.OK {
				results, theirs = append(results, parity.JSON(t, out)), append(theirs, c.Result)
			}
		}
		var ops, theirOps []any
		mine, _ := parity.JSON(t, x.Ops).([]any)
		for _, o := range mine {
			m := o.(map[string]any)
			if m["op"] == "create_page" {
				page := m["page"].(map[string]any)
				o = pageOp(page["title"], parity.JSON(t, made[0]), page["parent_id"], page["folder_id"])
				made = made[1:]
			}
			ops = append(ops, workspaceOp(o))
		}
		for _, o := range tc.Ops {
			var v any = o
			if o["op"] == "create_page" {
				page, content := o["page"].(map[string]any), o["content"].(map[string]any)
				v = pageOp(page["title"], content["document"].(map[string]any)["children"], page["parent_id"], page["folder_id"])
			}
			theirOps = append(theirOps, workspaceOp(v))
		}
		ours := parity.Steady(map[string]bool{}, results, ops)
		them := parity.Steady(map[string]bool{}, theirs, theirOps)
		for i, what := range []string{"results", "ops"} {
			if got, want := parity.Plain(ours[i]), parity.Plain(them[i]); !reflect.DeepEqual(got, want) {
				calls, _ := json.Marshal(tc.Calls)
				fail("%s of %s\n%s\nthe server's\n%s", what, calls, parity.Show(got), parity.Show(want))
			}
		}
	}
}

// workspaceOp is a page op as both sides write it: set_page_tags with its
// tags though it has none, which the app's JSON leaves out.
func workspaceOp(o any) any {
	if m, ok := o.(map[string]any); ok && m["op"] == "set_page_tags" && m["tags"] == nil {
		m["tags"] = []any{}
	}
	return o
}

// Tags are tidied here as the server tidies them.
func TestCorpusTags(t *testing.T) {
	var cases []struct {
		Tags []string `json:"tags"`
		Kept []string `json:"kept"`
	}
	parity.Read(t, "tags.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := store.NormalizeTags(c.Tags); !slices.Equal(got, c.Kept) {
			fail("%q kept as %q, on the server %q", c.Tags, got, c.Kept)
		}
	}
}

// The user's turn of a workspace run is worded here as on the server.
func TestCorpusWorkspaceMessages(t *testing.T) {
	var cases []struct {
		Open        *OpenPage `json:"open"`
		Instruction string    `json:"instruction"`
		Message     string    `json:"message"`
	}
	parity.Read(t, "workspace_messages.json", &cases)
	fail := parity.Check(t)
	for _, c := range cases {
		if got := workspaceMessage(Request{Instruction: c.Instruction, OpenPage: c.Open}); got != c.Message {
			fail("%+v made\n%q\nthe server\n%q", c, got, c.Message)
		}
	}
}
