package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentSetup(t *testing.T) {
	exe := "/Applications/Leaflet.app/Contents/MacOS/Leaflet"
	dir := "/Users/me/Library/Application Support/Leaflet"
	for _, cl := range externalClients {
		where, snippet := agentSetup(cl.id, exe, dir)
		if where == "" || snippet == "" {
			t.Errorf("%s: no setup", cl.id)
			continue
		}
		switch cl.id {
		case "claude":
			want := "claude mcp add -s user leaflet -e LEAFLET_DATA_DIR='/Users/me/Library/Application Support/Leaflet' -- " + exe + " mcp"
			if snippet != want {
				t.Errorf("claude:\n%s\nwant\n%s", snippet, want)
			}
		case "codex":
			for _, line := range []string{`[mcp_servers.leaflet]`, `command = "` + exe + `"`, `args = ["mcp"]`, `env = { LEAFLET_DATA_DIR = "` + dir + `" }`} {
				if !strings.Contains(snippet, line) {
					t.Errorf("codex has no %s:\n%s", line, snippet)
				}
			}
		default:
			var v map[string]map[string]struct {
				Type    string            `json:"type"`
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			}
			if err := json.Unmarshal([]byte(snippet), &v); err != nil {
				t.Errorf("%s: %v\n%s", cl.id, err, snippet)
				continue
			}
			key := "mcpServers"
			if cl.id == "vscode" {
				key = "servers"
			}
			s := v[key]["leaflet"]
			if s.Command != exe || len(s.Args) != 1 || s.Args[0] != "mcp" || s.Env["LEAFLET_DATA_DIR"] != dir || (cl.id == "vscode") != (s.Type == "stdio") {
				t.Errorf("%s: %+v", cl.id, s)
			}
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"/usr/bin/leaflet": "/usr/bin/leaflet", "/a b": "'/a b'", "it's": `'it'\''s'`, "": "''"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
