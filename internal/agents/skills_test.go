package agents

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/skills"
)

func toolNames(defs []map[string]any) []string {
	var out []string
	for _, d := range defs {
		out = append(out, d["function"].(map[string]any)["name"].(string))
	}
	return out
}

func TestReadSkill(t *testing.T) {
	if !slices.Contains(toolNames(ToolDefinitions(false, false)), "read_skill") ||
		!slices.Contains(toolNames(WorkspaceToolDefinitions(false)), "read_skill") {
		t.Error("an agent that writes has no read_skill")
	}
	enum := SkillToolDefinition(false)["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)["enum"]
	if !slices.Equal(enum.([]string), skills.Names(false)) {
		t.Errorf("read_skill offers %v in the app", enum)
	}
	if slices.Contains(toolNames(ToolDefinitions(true, true)), "read_skill") {
		t.Error("an agent that only reads has read_skill")
	}
	for _, ctx := range []ToolContext{{Content: doc.Empty()}, {Workspace: &Workspace{}}, {Content: doc.Empty(), ReadOnly: true}} {
		x := &Executor{Ctx: ctx}
		for _, s := range skills.All() {
			out := x.Call("read_skill", map[string]any{"name": s.Name})
			if s.External {
				if want := fmt.Sprintf("No skill %q. The skills are %s.", s.Name, strings.Join(skills.Names(false), ", ")); out["error"] != want {
					t.Errorf("read_skill %s in the app: %v, want %q", s.Name, out, want)
				}
				continue
			}
			if out["guide"] != s.Body || out["name"] != s.Name {
				t.Errorf("read_skill %s: %v", s.Name, out["error"])
			}
		}
		if out := x.Call("read_skill", map[string]any{"name": "nope"}); !strings.Contains(out["error"].(string), skills.Expressive) || strings.Contains(out["error"].(string), "saving-to-leaflet") {
			t.Errorf("read_skill nope: %v", out)
		}
		if len(x.Ops) != 0 {
			t.Errorf("read_skill changed the page: %v", x.Ops)
		}
	}
}

func TestPromptsOfferTheSkills(t *testing.T) {
	prompts := map[string]string{
		"page":      SystemPrompt("Agent", "sidebar", true, true, false, false, false, true),
		"workspace": WorkspacePrompt("Agent", true, true, false, false),
	}
	for where, p := range prompts {
		if !strings.Contains(p, "read the skill "+skills.Expressive+" with read_skill") {
			t.Errorf("the %s prompt does not ask for %s", where, skills.Expressive)
		}
		for _, s := range skills.All() {
			if strings.Contains(p, "  - "+s.Name+": ") == s.External {
				t.Errorf("the %s prompt lists %s: %v, for agents outside the app %v", where, s.Name, !s.External, s.External)
			}
		}
		if strings.Contains(p, "#fde68a") {
			t.Errorf("the %s prompt highlights with an opaque color", where)
		}
	}
	for where, p := range map[string]string{
		"read-only": SystemPrompt("Agent", "sidebar", true, true, true, false, false, true),
		"no tools":  SystemPrompt("Agent", "sidebar", true, false, false, false, false, true),
	} {
		if strings.Contains(p, "read_skill") {
			t.Errorf("the %s prompt offers read_skill", where)
		}
	}
}

// The guide sends charts through Markdown, so a fence that is no chart
// fails the write rather than leave its JSON on the page as code.
func TestMarkdownRefusesABrokenFence(t *testing.T) {
	broken := "Spend:\n\n```chart\n{\"labels\": [\"Mon\", \"Tue\"], \"series\": [{\"name\": \"¥\", \"values\": [42]}]}\n```\n"
	x := &Executor{Ctx: ToolContext{Content: doc.Empty(), PageID: "p", AgentName: "Agent"}}
	if out := x.Call("write_markdown", map[string]any{"position": map[string]any{"at": "end"}, "markdown": broken}); !strings.Contains(fmt.Sprint(out["error"]), "```chart fence") {
		t.Errorf("write_markdown: %v", out)
	}
	if len(x.Ops) != 0 {
		t.Errorf("a refused write changed the page: %v", x.Ops)
	}
	if _, _, err := PageNodes(map[string]any{"title": "Trip", "markdown": broken}, "Agent"); err == nil || !strings.Contains(err.Error(), "```chart fence") {
		t.Errorf("a page's markdown: %v", err)
	}
	// Streamed through Draft, a broken fence rolls back cleanly on FinishDraft.
	encoded, _ := json.Marshal(broken)
	draftRaw := `{"position":{"at":"end"},"markdown":` + string(encoded) + `}`
	x.Draft("write_markdown", draftRaw)
	if len(x.Ops) == 0 {
		t.Fatal("Draft did not write any blocks before FinishDraft")
	}
	if out, ok := x.FinishDraft("write_markdown", draftRaw); !ok || !strings.Contains(fmt.Sprint(out["error"]), "```chart fence") {
		t.Errorf("FinishDraft: ok=%v %v", ok, out)
	}
	if len(x.Ops) != 0 || !x.Ctx.Content.IsEmpty() {
		t.Errorf("a refused streamed draft left edits: ops=%d children=%d", len(x.Ops), len(x.Ctx.Content.Children))
	}
	if out := x.Call("write_markdown", map[string]any{"position": map[string]any{"at": "end"}, "markdown": strings.Replace(broken, "[42]", "[42, 31]", 1)}); out["error"] != nil {
		t.Errorf("a whole chart: %v", out["error"])
	}
}
