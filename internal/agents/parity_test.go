package agents

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/doc"
)

// Agents find URLs and title subpages here as the server's agent function
// does (supabase/functions/_shared/testdata/agent.json, which its tests
// write).
func TestAgentsAsTheServerHasThem(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		URLs []struct {
			Text string   `json:"text"`
			URLs []string `json:"urls"`
		} `json:"urls"`
		Titles []struct {
			Title string  `json:"title"`
			Made  *string `json:"made"`
		} `json:"titles"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.URLs) == 0 || len(f.Titles) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, tc := range f.URLs {
		if got := urlRe.FindAllString(tc.Text, -1); !slices.Equal(got, tc.URLs) {
			t.Errorf("%q: URLs %q, want %q", tc.Text, got, tc.URLs)
		}
	}
	// A page of its own takes its title as a subpage does.
	for _, tool := range []string{"create_subpage", "create_page"} {
		for _, tc := range f.Titles {
			var made *string
			x := executor(page())
			x.Ctx.CreateSubpage = func(title string, _ []*doc.Node) (string, error) {
				made = &title
				return "page-2", nil
			}
			x.Ctx.CreatePage = func(title string, _ []*doc.Node, _ string) (string, error) {
				made = &title
				return "page-3", nil
			}
			x.Call(tool, map[string]any{"title": tc.Title})
			if (made == nil) != (tc.Made == nil) || made != nil && *made != *tc.Made {
				t.Errorf("%s %q made %v, want %v", tool, tc.Title, made, tc.Made)
			}
		}
	}
}

// contract is a schema as both sides check it: its words left out, its
// lists of names sorted, as the server's test writes it.
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

// The tools offer the model what the server's offer it, and take and
// refuse the same calls (supabase/functions/_shared/testdata/tools.json,
// which its tests write): a call that works on a cloud run works here.
func TestToolsAsTheServerHasThem(t *testing.T) {
	raw, err := os.ReadFile("../../supabase/functions/_shared/testdata/tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Schemas   map[string]any `json:"schemas"`
		Workspace map[string]any `json:"workspace"`
		Calls     []struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
			OK   bool   `json:"ok"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Schemas) == 0 || len(f.Calls) == 0 {
		t.Fatal("the fixture has no cases")
	}
	defs, _ := json.Marshal(ToolDefinitions(true, false))
	var ours []struct {
		Function struct {
			Name       string `json:"name"`
			Parameters any    `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(defs, &ours); err != nil {
		t.Fatal(err)
	}
	for _, d := range ours {
		want, ok := f.Schemas[d.Function.Name]
		if !ok {
			t.Errorf("%s: the server has no such tool", d.Function.Name)
			continue
		}
		got, _ := json.Marshal(contract(d.Function.Parameters))
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s takes\n%s\nthe server's\n%s", d.Function.Name, got, w)
		}
	}
	if len(ours) != len(f.Schemas) {
		t.Errorf("%d tools here, %d on the server", len(ours), len(f.Schemas))
	}
	// The workspace agent's too.
	defs, _ = json.Marshal(WorkspaceToolDefinitions(true))
	ours = nil
	if err := json.Unmarshal(defs, &ours); err != nil {
		t.Fatal(err)
	}
	for _, d := range ours {
		got, _ := json.Marshal(contract(d.Function.Parameters))
		w, _ := json.Marshal(f.Workspace[d.Function.Name])
		if string(got) != string(w) {
			t.Errorf("workspace %s takes\n%s\nthe server's\n%s", d.Function.Name, got, w)
		}
	}
	if len(ours) != len(f.Workspace) {
		t.Errorf("%d workspace tools here, %d on the server", len(ours), len(f.Workspace))
	}
	for _, c := range f.Calls {
		x := executor(page())
		x.Ctx.CreateSubpage = func(string, []*doc.Node) (string, error) { return "page-2", nil }
		// The user has folder-1 alone, as on the server.
		x.Ctx.CreatePage = func(_ string, _ []*doc.Node, folder string) (string, error) {
			if folder != "" && folder != "folder-1" {
				return "", noFolder(folder)
			}
			return "page-3", nil
		}
		out := x.CallRaw(c.Tool, c.Args)
		if _, refused := out["error"]; refused == c.OK {
			t.Errorf("%s %s: refused %v (%v), the server's ok %v", c.Tool, c.Args, refused, out["error"], c.OK)
		}
	}
}

// A value is counted as the server counts it (json_costs.json): the pages
// list_pages lists are cut where they would pass the most a model reads at
// once, on either side at the same page.
func TestJSONCostAsTheServer(t *testing.T) {
	var cases []struct {
		Value any `json:"value"`
		Cost  int `json:"cost"`
	}
	data, err := os.ReadFile("../../supabase/functions/_shared/testdata/json_costs.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if got := jsonCost(c.Value); got != c.Cost {
			t.Errorf("%#v costs %d here, %d on the server", c.Value, got, c.Cost)
		}
	}
}
