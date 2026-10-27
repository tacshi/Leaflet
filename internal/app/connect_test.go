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
	if err := os.WriteFile(path, []byte("{ // a comment\n}"), 0o644); err != nil {
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

	link := installLink("vscode", testExe, testDir)
	q, ok := strings.CutPrefix(link, "vscode:mcp/install?")
	if !ok || strings.ContainsAny(q, " +") {
		t.Fatalf("vscode: %s", link)
	}
	js, err := url.PathUnescape(q)
	var vs server
	if err != nil || json.Unmarshal([]byte(js), &vs) != nil || vs.Name != "leaflet" || vs.Type != "stdio" {
		t.Fatalf("vscode %q: %v", js, err)
	}
	check("vscode", vs)

	if installLink("claude", testExe, testDir) != "" {
		t.Error("claude has a link")
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
		"cursor-vscode": "cursor", "Visual Studio Code": "vscode", "Visual Studio Code - Insiders": "vscode", "goose": "",
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
