package app

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testExe = "/Applications/Leaflet.app/Contents/MacOS/Leaflet"
	testDir = "/Users/me/Library/Application Support/Leaflet"
)

func testEnv(t *testing.T) agentEnv {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	root := t.TempDir()
	return agentEnv{home: filepath.Join(root, "home"), config: filepath.Join(root, "config")}
}

func TestCodexEntry(t *testing.T) {
	// As Codex's own tool writes it, among other servers.
	own := `model = "o3"

[mcp_servers.other]
command = "other"

[mcp_servers.leaflet]
command = "/App/Le af.app/x"
args = ["mcp"]

[mcp_servers.leaflet.env]
LEAFLET_DATA_DIR = "/a \"b\""

[profiles.fast]
model = "o4-mini"
`
	e := codexEntry(own)
	if e == nil || e.Command != "/App/Le af.app/x" || e.Env["LEAFLET_DATA_DIR"] != `/a "b"` {
		t.Errorf("own: %+v", e)
	}
	got := withoutCodexEntry(own)
	for _, keep := range []string{`model = "o3"`, "[mcp_servers.other]", "[profiles.fast]", `model = "o4-mini"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %s:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "leaflet") || codexEntry(got) != nil {
		t.Errorf("kept leaflet:\n%s", got)
	}
	// As agentSetup writes it.
	_, snippet := agentSetup("codex", testExe, testDir)
	if e := codexEntry(snippet); e == nil || e.Command != testExe || e.Env["LEAFLET_DATA_DIR"] != testDir {
		t.Errorf("snippet: %+v", e)
	}
	if codexEntry(`[mcp_servers.leafletish]`+"\ncommand = \"x\"") != nil {
		t.Error("read another server as leaflet")
	}
}

func TestConnectCodexWithoutCLI(t *testing.T) {
	env := testEnv(t)
	path := codexConfig(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model = \"o3\"\n\n[mcp_servers.leaflet]\ncommand = \"/old\"\n\n[mcp_servers.leaflet.env]\nLEAFLET_DATA_DIR = \"/old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := connectAgent("codex", "", env, testExe, testDir); err != nil {
		t.Fatal(err)
	}
	s := agentStatus{}
	s.entry, _ = readEntry("codex", env)
	raw, _ := os.ReadFile(path)
	if !s.current(testExe, testDir) || strings.Count(string(raw), "[mcp_servers.leaflet") != 1 || !strings.HasPrefix(string(raw), "model = \"o3\"\n\n[mcp_servers.leaflet]") {
		t.Errorf("connected:\n%s", raw)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := disconnectAgent("codex", "", env); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "model = \"o3\"\n" {
		t.Errorf("disconnected:\n%q", raw)
	}
}

func TestConnectClaudeDesktop(t *testing.T) {
	env := testEnv(t)
	path, _ := jsonConfig("claude-desktop", env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"globalShortcut": "Alt+Space", "mcpServers": {"other": {"command": "x", "args": ["a&b"]}}}`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectAgent("claude-desktop", "", env, testExe, testDir); err != nil {
		t.Fatal(err)
	}
	var got struct {
		GlobalShortcut string                     `json:"globalShortcut"`
		MCPServers     map[string]json.RawMessage `json:"mcpServers"`
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	e, _ := readEntry("claude-desktop", env)
	if got.GlobalShortcut != "Alt+Space" || string(got.MCPServers["other"]) == "" || !strings.Contains(string(raw), "a&b") || !(agentStatus{entry: e}).current(testExe, testDir) {
		t.Errorf("connected:\n%s", raw)
	}
	if err := disconnectAgent("claude-desktop", "", env); err != nil {
		t.Fatal(err)
	}
	if e, _ := readEntry("claude-desktop", env); e != nil {
		t.Errorf("still there: %+v", e)
	}
	// A file that is not JSON is left alone.
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectAgent("claude-desktop", "", env, testExe, testDir); err == nil {
		t.Error("wrote over a file it could not read")
	}
}

func TestInstallLink(t *testing.T) {
	type server struct {
		Name, Type, Command string
		Args                []string
		Env                 map[string]string
	}
	check := func(id string, s server) {
		if s.Command != testExe || len(s.Args) != 1 || s.Args[0] != "mcp" || s.Env["LEAFLET_DATA_DIR"] != testDir {
			t.Errorf("%s: %+v", id, s)
		}
	}
	u, err := url.Parse(installLink("cursor", testExe, testDir))
	if err != nil || u.Scheme != "cursor" || u.Query().Get("name") != "leaflet" {
		t.Fatalf("cursor: %v %v", u, err)
	}
	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("config"))
	var cs server
	if err != nil || json.Unmarshal(raw, &cs) != nil {
		t.Fatalf("cursor config %q: %v", raw, err)
	}
	check("cursor", cs)

	for _, id := range []string{"claude", "opencode", "vscode"} {
		if installLink(id, testExe, testDir) != "" {
			t.Errorf("%s has a link", id)
		}
	}
}

func TestDetectAgents(t *testing.T) {
	env := testEnv(t)
	cursor, _ := jsonConfig("cursor", env)
	if err := os.MkdirAll(filepath.Dir(cursor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cursor, []byte(`{"mcpServers": {"leaflet": {"command": "/old/Leaflet", "env": {"LEAFLET_DATA_DIR": "`+testDir+`"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	found := detectAgents(env, map[string]string{"cursor-vscode": "1700000000", "claude-code": "1600000000", "claude-ai": "x"})
	by := map[string]agentStatus{}
	for _, s := range found {
		by[s.app.id] = s
	}
	if c := by["cursor"]; !c.installed || c.entry == nil || c.current(testExe, testDir) || c.seen.Unix() != 1700000000 {
		t.Errorf("cursor: %+v", c)
	}
	if c := by["codex"]; c.entry != nil {
		t.Errorf("codex: %+v", c)
	}
	if c := by["claude-desktop"]; !c.seen.IsZero() {
		t.Errorf("claude desktop seen at %v", c.seen)
	}
}

func TestClientOf(t *testing.T) {
	for name, want := range map[string]string{
		"claude-code": "claude", "claude-ai": "claude-desktop", "codex-mcp-client": "codex", "Codex": "codex",
		"cursor-vscode": "cursor", "opencode": "opencode", "OpenCode": "opencode", "Visual Studio Code": "", "goose": "",
	} {
		if got := clientOf(name); got != want {
			t.Errorf("clientOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFindCLI(t *testing.T) {
	env := testEnv(t)
	for _, v := range []string{"2.1.99", "2.1.308", "2.1.304"} {
		p := filepath.Join(env.config, "Claude", "claude-code", v, "abc", "claude.app", "Contents", "MacOS", "claude")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := findCLI("claude", env); versionOf(got) != "2.1.308" {
		t.Errorf("bundled: %s", got)
	}
	bin := filepath.Join(env.home, "bin")
	env.path = bin
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findCLI("claude", env); got != filepath.Join(bin, "claude") {
		t.Errorf("on PATH: %s", got)
	}
}

func TestAgentOrder(t *testing.T) {
	var got []string
	for _, a := range agentApps {
		got = append(got, a.label)
	}
	if want := "ChatGPT, Claude Desktop, Codex, Claude Code, OpenCode, Cursor"; strings.Join(got, ", ") != want {
		t.Errorf("order %s, want %s", strings.Join(got, ", "), want)
	}
}

func TestConnectOpenCodeWithoutCLI(t *testing.T) {
	for name, before := range map[string]string{
		// Its second version's settings, as `opencode mcp add` writes them.
		"v2": `{"$schema": "https://opencode.ai/config.json", "mcp": {"servers": {"rea": {"type": "local", "command": ["npx", "rea"]}}}}`,
		// Its first's, with servers right under mcp.
		"v1":   `{"mcp": {"rea": {"type": "local", "command": ["npx", "rea"], "enabled": true}}}`,
		"none": ``,
	} {
		env := testEnv(t)
		t.Setenv("XDG_CONFIG_HOME", "")
		path := openCodeConfig(env)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := connectAgent("opencode", "", env, testExe, testDir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := os.ReadFile(path)
		keys := []string{"mcp", "servers"}
		if name == "v1" {
			keys = []string{"mcp"}
		}
		servers, err := nested(raw, keys)
		if err != nil || servers["leaflet"] == nil || (name != "none" && servers["rea"] == nil) {
			t.Errorf("%s: connected under %v:\n%s", name, keys, raw)
		}
		var e struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
		}
		_ = json.Unmarshal(servers["leaflet"], &e)
		got, _ := readEntry("opencode", env)
		if e.Type != "local" || len(e.Command) != 2 || e.Command[1] != "mcp" || !(agentStatus{entry: got}).current(testExe, testDir) {
			t.Errorf("%s: entry %+v, read %+v", name, e, got)
		}
		if err := disconnectAgent("opencode", "", env); err != nil {
			t.Fatal(err)
		}
		raw, _ = os.ReadFile(path)
		if got, _ := readEntry("opencode", env); got != nil || (name != "none" && !strings.Contains(string(raw), `"rea"`)) {
			t.Errorf("%s: disconnected:\n%s", name, raw)
		}
	}
}

func TestEditServersKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name, before string
		keys         []string
		want         string
	}{{
		name: "opencode.jsonc",
		before: `{
  // my settings
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      /* a server */
      "rea": { "type": "local", "command": ["npx", "rea"], },
    },
  },
}
`,
		keys: []string{"mcp", "servers"},
		want: `{
  // my settings
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      /* a server */
      "rea": { "type": "local", "command": ["npx", "rea"], },
      "leaflet": {
        "command": "/x"
      },
    },
  },
}
`,
	}, {
		name:   "claude_desktop_config.json",
		before: "{\n\t\"zShortcut\": \"Alt+Space\",\n\t\"aMenu\": true\n}\n",
		keys:   []string{"mcpServers"},
		want:   "{\n\t\"zShortcut\": \"Alt+Space\",\n\t\"aMenu\": true,\n\t\"mcpServers\": {\n\t\t\"leaflet\": {\n\t\t\t\"command\": \"/x\"\n\t\t}\n\t}\n}\n",
	}, {
		name: "new.json",
		keys: []string{"mcp", "servers"},
		want: "{\n  \"mcp\": {\n    \"servers\": {\n      \"leaflet\": {\n        \"command\": \"/x\"\n      }\n    }\n  }\n}\n",
	}} {
		path := filepath.Join(dir, c.name)
		if c.before != "" {
			if err := os.WriteFile(path, []byte(c.before), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := editServers(path, c.keys, map[string]string{"command": "/x"}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != c.want {
			t.Errorf("%s: set:\n%s\nwant\n%s", c.name, got, c.want)
		}
		// Set again: replaced where it is.
		if err := editServers(path, c.keys, map[string]string{"command": "/x"}); err != nil {
			t.Fatal(err)
		}
		if again, _ := os.ReadFile(path); string(again) != c.want {
			t.Errorf("%s: set again:\n%s", c.name, again)
		}
		if err := editServers(path, c.keys, nil); err != nil {
			t.Fatal(err)
		}
		got, _ = os.ReadFile(path)
		if servers, err := nested(got, c.keys); err != nil || servers["leaflet"] != nil {
			t.Errorf("%s: removed: %v\n%s", c.name, err, got)
		}
		if c.before != "" && strings.Contains(c.before, "rea") && string(got) != c.before {
			t.Errorf("%s: removed, not as before:\n%s", c.name, got)
		}
	}
}

func TestChatGPTSharesCodex(t *testing.T) {
	env := testEnv(t)
	if err := connectAgent("codex", "", env, testExe, testDir); err != nil {
		t.Fatal(err)
	}
	found := detectAgents(env, map[string]string{"codex-mcp-client": "1700000000"})
	by := map[string]agentStatus{}
	for _, s := range found {
		by[s.app.id] = s
	}
	for _, id := range []string{"chatgpt", "codex"} {
		if s := by[id]; !s.current(testExe, testDir) || s.seen.Unix() != 1700000000 {
			t.Errorf("%s: %+v", id, s)
		}
	}
}
