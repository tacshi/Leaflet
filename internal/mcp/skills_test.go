package mcp

import (
	"strings"
	"testing"

	"leaflet/internal/agents"
	"leaflet/internal/skills"
)

func TestSkills(t *testing.T) {
	s := NewServer(newStore(t), "0.1.0")
	got := session(t, s, initialize, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "read_skill", map[string]any{"name": skills.Expressive}),
		call(4, "read_skill", map[string]any{"name": "saving-to-leaflet"}),
		call(5, "read_skill", map[string]any{"name": "nope"}))
	inst, _ := got[1]["result"].(map[string]any)["instructions"].(string)
	for _, sk := range skills.All() {
		if !strings.Contains(inst, sk.Name) {
			t.Errorf("instructions leave out the skill %s", sk.Name)
		}
	}
	if !strings.Contains(inst, "offer once") || !strings.Contains(inst, "save it only on a yes") {
		t.Errorf("instructions do not have agents offer to save what is worth keeping: %q", inst)
	}
	if strings.Contains(inst, "#fde68a") {
		t.Error("instructions highlight with an opaque color")
	}
	var tool map[string]any
	for _, x := range got[2]["result"].(map[string]any)["tools"].([]any) {
		if m := x.(map[string]any); m["name"] == "read_skill" {
			tool = m
		}
	}
	if tool == nil {
		t.Fatal("no read_skill")
	}
	if a := tool["annotations"].(map[string]any); a["readOnlyHint"] != true || a["openWorldHint"] != false {
		t.Errorf("read_skill annotations %v", a)
	}
	if desc, _ := tool["description"].(string); !strings.Contains(desc, skills.Index(true)) {
		t.Errorf("read_skill description %q", desc)
	}
	out, failed := result(t, got[3])
	if want, _ := skills.Get(skills.Expressive); failed || out["guide"] != want.Body {
		t.Errorf("read_skill: %v", out)
	}
	if out, failed := result(t, got[4]); failed || out["name"] != "saving-to-leaflet" {
		t.Errorf("read_skill saving-to-leaflet: %v", out)
	}
	if out, failed := result(t, got[5]); !failed || !strings.Contains(out["error"].(string), "No skill") || !strings.Contains(out["error"].(string), "saving-to-leaflet") {
		t.Errorf("read_skill nope: %v", out)
	}
}

// A skill the app's agents read names a tool only they lack, or the leaflet
// command, as for agents outside the app: they would call a tool they do not
// have.
func TestAppSkillsNameTheirTools(t *testing.T) {
	inApp := map[string]bool{}
	for _, defs := range [][]map[string]any{agents.ToolDefinitions(true, false), agents.WorkspaceToolDefinitions(true), agents.WebToolDefinitions(true), agents.MapToolDefinitions()} {
		for _, d := range defs {
			inApp[d["function"].(map[string]any)["name"].(string)] = true
		}
	}
	var outside []string
	for name := range toolNames() {
		if !inApp[name] {
			outside = append(outside, name)
		}
	}
	for _, sk := range skills.All() {
		if sk.External {
			continue
		}
		for line := range strings.Lines(sk.Body) {
			named := strings.Contains(line, "leaflet command")
			for _, name := range outside {
				named = named || strings.Contains(line, name)
			}
			if named && !strings.Contains(line, "outside the app") {
				t.Errorf("%s names what only agents outside the app have, without saying so: %s", sk.Name, line)
			}
		}
	}
}
