package agents

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/golden"
	"leaflet/internal/store"
)

type goldenCall struct {
	Tool   string   `json:"tool"`
	Args   string   `json:"args"`
	OK     bool     `json:"ok"`
	Result any      `json:"result"`
	Edits  []string `json:"edits"`
}

func summaries(edits []Edit) []string {
	out := []string{}
	for _, e := range edits {
		out = append(out, e.Summary)
	}
	return out
}

func goldenOps(t *testing.T, ops []doc.Op, made [][]*doc.Node) []any {
	t.Helper()
	out := []any{}
	all, _ := golden.JSON(t, ops).([]any)
	for _, o := range all {
		if m := o.(map[string]any); m["op"] == "create_page" {
			blocks := made[0]
			if made = made[1:]; blocks == nil {
				blocks = []*doc.Node{}
			}
			m["content"] = map[string]any{"document": map[string]any{"id": "root", "type": "page", "children": golden.JSON(t, blocks)}}
		}
		out = append(out, o)
	}
	return out
}

func comparedOps(ops []any) []any {
	text := func(v any) any {
		if v == nil {
			return ""
		}
		return v
	}
	out := []any{}
	for _, o := range ops {
		m, _ := o.(map[string]any)
		switch m["op"] {
		case "create_page":
			page, _ := m["page"].(map[string]any)
			content, _ := m["content"].(map[string]any)
			document, _ := content["document"].(map[string]any)
			blocks := document["children"]
			if blocks == nil {
				blocks = []any{}
			}
			o = map[string]any{"op": "create_page", "title": page["title"], "blocks": blocks, "parent": text(page["parent_id"]), "folder": text(page["folder_id"])}
		case "set_page_tags":
			if m["tags"] == nil {
				m = maps.Clone(m)
				m["tags"] = []any{}
				o = m
			}
		}
		out = append(out, o)
	}
	return out
}

func TestToolCalls(t *testing.T) {
	var cases []struct {
		Before   *doc.Content `json:"before"`
		Cross    bool         `json:"cross"`
		ReadOnly bool         `json:"readOnly"`
		Request  string       `json:"request"`
		Calls    []goldenCall `json:"calls"`
		Ops      []any        `json:"ops"`
		After    struct {
			Document struct {
				Children []any `json:"children"`
			} `json:"document"`
		} `json:"after"`
	}
	f := golden.Open(t, "calls_random.json")
	f.Decode(&cases)
	for i, tc := range cases {
		known := map[string]bool{}
		for _, n := range tc.Before.All() {
			known[n.ID] = true
		}
		var made [][]*doc.Node
		x := &Executor{Ctx: ToolContext{
			Content: tc.Before.Copy(), PageID: "page-1", PageTitle: "Plan", AgentName: "Claude", Request: tc.Request, ReadOnly: tc.ReadOnly,
			CreateSubpage: func(_ string, nodes []*doc.Node) (string, error) {
				made = append(made, nodes)
				return "page-2", nil
			},
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
		var results, wanted, answers []any
		var places [][]any
		for j, c := range tc.Calls {
			edits := len(x.Edits)
			out := x.CallRaw(c.Tool, c.Args)
			_, refused := out["error"]
			f.Check(!refused, i, "calls", j, "ok")
			f.Check(summaries(x.Edits[edits:]), i, "calls", j, "edits")
			if c.Result == nil {
				continue
			}
			if c.Tool == "read_page" {
				blocks, _ := c.Result.(map[string]any)
				results, wanted, answers = append(results, golden.JSON(t, out["blocks"])), append(wanted, blocks["blocks"]), append(answers, out["blocks"])
				places = append(places, []any{i, "calls", j, "result", "blocks"})
				continue
			}
			results, wanted, answers = append(results, golden.JSON(t, out)), append(wanted, c.Result), append(answers, out)
			places = append(places, []any{i, "calls", j, "result"})
		}
		ops := goldenOps(t, x.Ops, made)
		after := golden.JSON(t, x.Ctx.Content.Children)
		if after == nil {
			after = []any{}
		}
		mine := golden.Steady(known, results, comparedOps(ops), after)
		file := golden.Steady(known, wanted, comparedOps(tc.Ops), any(tc.After.Document.Children))
		for k := range results {
			if !reflect.DeepEqual(golden.Plain(mine[0].([]any)[k]), golden.Plain(file[0].([]any)[k])) {
				f.Mismatch(answers[k], places[k]...)
			}
		}
		if !reflect.DeepEqual(golden.Plain(mine[1]), golden.Plain(file[1])) {
			f.Mismatch(ops, i, "ops")
		}
		if !reflect.DeepEqual(golden.Plain(mine[2]), golden.Plain(file[2])) {
			f.Mismatch(x.Ctx.Content, i, "after")
		}
	}
}

func TestURLsInText(t *testing.T) {
	var cases []struct {
		Text string `json:"text"`
	}
	f := golden.Open(t, "texts_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(urlsIn(c.Text), i, "urls")
	}
}

func urlsIn(text string) []string {
	if got := urlRe.FindAllString(text, -1); got != nil {
		return got
	}
	return []string{}
}

func TestUserMessages(t *testing.T) {
	var cases []struct {
		Turn struct {
			Instruction   string           `json:"instruction"`
			PageTitle     string           `json:"pageTitle"`
			PageViews     []map[string]any `json:"pageViews"`
			Focus         []map[string]any `json:"focus"`
			SelectionText string           `json:"selectionText"`
			Thread        []ThreadComment  `json:"thread"`
		} `json:"turn"`
	}
	f := golden.Open(t, "messages_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		req := Request{
			PageTitle: c.Turn.PageTitle, Instruction: c.Turn.Instruction, SelectionText: c.Turn.SelectionText,
			Thread: c.Turn.Thread, SupportsTools: true,
		}
		views := c.Turn.PageViews
		if views == nil {
			views = []map[string]any{}
		}
		f.Check(userMessage(req, views, c.Turn.Focus), i, "message")
	}
}

func TestTranscriptsReadForAgents(t *testing.T) {
	var cases []struct {
		Segments []store.Segment `json:"segments"`
	}
	f := golden.Open(t, "transcripts_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(TranscriptText(c.Segments), i, "text")
	}
}

func TestOpsOverwrite(t *testing.T) {
	var cases []struct {
		Before *doc.Content `json:"before"`
		Ops    []doc.Op     `json:"ops"`
	}
	f := golden.OpenIn(t, "../doc", "ops_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(overwrites(c.Before, c.Ops), i, "overwrites")
	}
}

var goldenWorld = []WorkspacePage{
	{ID: "p-1", Title: "Plan", Tags: []string{"plan"}},
	{ID: "p-2", Title: "Notes", ParentID: "p-1", Tags: []string{}},
	{ID: "p-3", Title: "Shared", Tags: []string{}, Shared: true, People: true},
	{ID: "p-4", Title: "Filed", FolderID: "f-1", Tags: []string{}},
	{ID: "p-5", Title: "Untitled", Tags: []string{}, Untitled: true},
	{ID: "p-6", Title: "Linked", Tags: []string{}, Shared: true},
}

func goldenWorkspace(reading bool, made *[][]*doc.Node) *Workspace {
	page := func(id string) (WorkspacePage, bool) {
		for _, p := range goldenWorld {
			if p.ID == id {
				return p, true
			}
		}
		return WorkspacePage{}, false
	}
	folders := func() []map[string]any { return []map[string]any{{"id": "f-1", "name": "Clippings"}} }
	return &Workspace{
		Reading: reading,
		Pages:   func() ([]WorkspacePage, error) { return goldenWorld, nil },
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
		CreateFolder: func(name, parentID string) (doc.NewFolder, error) {
			return doc.NewFolder{ID: "f-new", SpaceID: "space-0", ParentFolderID: parentID, Name: name, SortKey: "a1"}, nil
		},
		MoveFolder: func(id, parentID string) (string, doc.FolderPlace, doc.FolderPlace, error) {
			return "Folder", doc.FolderPlace{SortKey: "a"}, doc.FolderPlace{ParentFolderID: parentID, SortKey: "b"}, nil
		},
	}
}

func TestWorkspaceCalls(t *testing.T) {
	var cases []struct {
		Reading bool         `json:"reading"`
		Request string       `json:"request"`
		Calls   []goldenCall `json:"calls"`
		Ops     []any        `json:"ops"`
	}
	f := golden.Open(t, "workspace_calls_random.json")
	f.Decode(&cases)
	for i, tc := range cases {
		var made [][]*doc.Node
		x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: WorkspaceID, AgentName: "Claude", Request: tc.Request,
			Workspace: goldenWorkspace(tc.Reading, &made)}}
		var results, wanted, answers []any
		var places [][]any
		for j, c := range tc.Calls {
			edits := len(x.Edits)
			out := x.CallRaw(c.Tool, c.Args)
			_, refused := out["error"]
			f.Check(!refused, i, "calls", j, "ok")
			f.Check(summaries(x.Edits[edits:]), i, "calls", j, "edits")
			if refused {
				continue
			}
			results, wanted, answers = append(results, golden.JSON(t, out)), append(wanted, c.Result), append(answers, out)
			places = append(places, []any{i, "calls", j, "result"})
		}
		ops := goldenOps(t, x.Ops, made)
		mine := golden.Steady(map[string]bool{}, results, comparedOps(ops))
		file := golden.Steady(map[string]bool{}, wanted, comparedOps(tc.Ops))
		for k := range results {
			if !reflect.DeepEqual(golden.Plain(mine[0].([]any)[k]), golden.Plain(file[0].([]any)[k])) {
				f.Mismatch(answers[k], places[k]...)
			}
		}
		if !reflect.DeepEqual(golden.Plain(mine[1]), golden.Plain(file[1])) {
			f.Mismatch(ops, i, "ops")
		}
	}
}

func TestWorkspaceMessages(t *testing.T) {
	var cases []struct {
		Open        *OpenPage `json:"open"`
		Instruction string    `json:"instruction"`
	}
	f := golden.Open(t, "workspace_messages_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(workspaceMessage(Request{Instruction: c.Instruction, OpenPage: c.Open}), i, "message")
	}
}

func TestCutsAndThinking(t *testing.T) {
	var cases []struct {
		Pad    int    `json:"pad"`
		End    string `json:"end"`
		Answer string `json:"answer"`
	}
	f := golden.Open(t, "cuts_random.json")
	f.Decode(&cases)
	for i, c := range cases {
		pad := strings.Repeat("x", c.Pad)
		f.Check(strings.TrimPrefix(cutResult(pad+c.End), pad), i, "cut")
		f.Check(withoutThinking(c.Answer), i, "reply")
	}
}

func TestURLsAndPageTitles(t *testing.T) {
	var cases struct {
		URLs []struct {
			Text string `json:"text"`
		} `json:"urls"`
		Titles []struct {
			Title string  `json:"title"`
			Made  *string `json:"made"`
		} `json:"titles"`
	}
	f := golden.Open(t, "agent.json")
	f.Decode(&cases)
	if len(cases.URLs) == 0 || len(cases.Titles) == 0 {
		t.Fatal("the golden file has no cases")
	}
	for i, c := range cases.URLs {
		f.Check(urlsIn(c.Text), "urls", i, "urls")
	}
	for i, c := range cases.Titles {
		made := map[string]*string{}
		for _, tool := range []string{"create_subpage", "create_page"} {
			x := executor(page())
			x.Ctx.CreateSubpage = func(title string, _ []*doc.Node) (string, error) {
				made[tool] = &title
				return "page-2", nil
			}
			x.Ctx.CreatePage = func(title string, _ []*doc.Node, _ string) (string, error) {
				made[tool] = &title
				return "page-3", nil
			}
			x.Call(tool, map[string]any{"title": c.Title})
		}
		sub, own := made["create_subpage"], made["create_page"]
		if (sub == nil) != (own == nil) || sub != nil && *sub != *own {
			t.Errorf("%q: a subpage titled %s, a page of its own %s", c.Title, golden.Show(sub), golden.Show(own))
		}
		if (sub == nil) != (c.Made == nil) || sub != nil && *sub != *c.Made {
			f.Mismatch(sub, "titles", i, "made")
		}
	}
}

func contract(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		names := true
		for i, e := range x {
			out[i] = contract(e)
			_, ok := out[i].(string)
			names = names && ok
		}
		if names {
			slices.SortFunc(out, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k != "description" {
				out[k] = contract(e)
			}
		}
		return out
	}
	return v
}

func checkSchemas(t *testing.T, f *golden.File, key string, defs []map[string]any, want map[string]any) {
	t.Helper()
	tools, _ := golden.JSON(t, defs).([]any)
	for _, d := range tools {
		fn, _ := d.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		f.Check(contract(fn["parameters"]), key, name)
	}
	if len(tools) != len(want) {
		t.Errorf("%d tools offered, the golden file has %d under %s", len(tools), len(want), key)
	}
}

func TestToolSchemasAndCalls(t *testing.T) {
	var cases struct {
		Schemas   map[string]any `json:"schemas"`
		Workspace map[string]any `json:"workspace"`
		Calls     []struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
		} `json:"calls"`
	}
	f := golden.Open(t, "tools.json")
	f.Decode(&cases)
	if len(cases.Schemas) == 0 || len(cases.Workspace) == 0 || len(cases.Calls) == 0 {
		t.Fatal("the golden file has no cases")
	}
	checkSchemas(t, f, "schemas", ToolDefinitions(true, false), cases.Schemas)
	checkSchemas(t, f, "workspace", WorkspaceToolDefinitions(true), cases.Workspace)
	for i, c := range cases.Calls {
		x := executor(page())
		x.Ctx.CreateSubpage = func(string, []*doc.Node) (string, error) { return "page-2", nil }
		x.Ctx.CreatePage = func(_ string, _ []*doc.Node, folder string) (string, error) {
			if folder != "" && folder != "folder-1" {
				return "", noFolder(folder)
			}
			return "page-3", nil
		}
		_, refused := x.CallRaw(c.Tool, c.Args)["error"]
		f.Check(!refused, "calls", i, "ok")
	}
}

func TestJSONCost(t *testing.T) {
	var cases []struct {
		Value any `json:"value"`
	}
	f := golden.Open(t, "json_costs.json")
	f.Decode(&cases)
	for i, c := range cases {
		f.Check(jsonCost(c.Value), i, "cost")
	}
}

func TestSearchAnswersRead(t *testing.T) {
	var cases struct {
		Responses struct {
			Output []any  `json:"output"`
			Search Search `json:"search"`
		} `json:"responses"`
		Anthropic struct {
			Content []any  `json:"content"`
			Search  Search `json:"search"`
		} `json:"anthropic"`
		Chat struct {
			Data   chatSearchAnswer `json:"data"`
			Search Search           `json:"search"`
		} `json:"chat"`
	}
	f := golden.Open(t, "search.json")
	f.Decode(&cases)
	for name, c := range map[string][2]Search{
		"responses": {responsesSearch(cases.Responses.Output), cases.Responses.Search},
		"anthropic": {anthropicSearch(cases.Anthropic.Content), cases.Anthropic.Search},
		"chat":      {cases.Chat.Data.search(), cases.Chat.Search},
	} {
		if len(c[1].Sources) == 0 {
			t.Errorf("the golden file has no sources for %s", name)
		}
		if !reflect.DeepEqual(c[0], c[1]) {
			f.Mismatch(struct {
				Answer  string   `json:"answer"`
				Sources []Source `json:"sources"`
			}{c[0].Answer, append([]Source{}, c[0].Sources...)}, name, "search")
		}
	}
}

func TestWebToolSchemasAndCalls(t *testing.T) {
	var schemas struct {
		Web map[string]any `json:"web"`
	}
	tools := golden.Open(t, "tools.json")
	tools.Decode(&schemas)
	if len(schemas.Web) != 2 {
		t.Fatalf("the golden file has %d web tools", len(schemas.Web))
	}
	checkSchemas(t, tools, "web", WebToolDefinitions(true), schemas.Web)
	if defs := WebToolDefinitions(false); len(defs) != 1 || defs[0]["function"].(map[string]any)["name"] != "read_web_page" {
		t.Errorf("without a search %v", defs)
	}

	var cases struct {
		Request string `json:"request"`
		Search  Search `json:"search"`
		Seqs    []struct {
			Armed bool `json:"armed"`
			Calls []struct {
				Tool string `json:"tool"`
				Args string `json:"args"`
			} `json:"calls"`
		} `json:"sequences"`
	}
	f := golden.Open(t, "web_calls.json")
	f.Decode(&cases)
	if len(cases.Seqs) == 0 {
		t.Fatal("the golden file has no cases")
	}
	for i, seq := range cases.Seqs {
		x := webExecutor(cases.Request, cases.Search)
		x.armed = seq.Armed
		for j, c := range seq.Calls {
			_, refused := x.CallRaw(c.Tool, c.Args)["error"]
			f.Check(!refused, "sequences", i, "calls", j, "ok")
		}
		f.Check(!x.readPages, "sequences", i, "kept_to_page")
	}
}
