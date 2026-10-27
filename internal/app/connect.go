package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Connecting AI apps on this computer to `leaflet mcp` for the user, so
// nobody has to open a terminal or find a settings file: by the app's
// own command-line tool (Claude Code, Codex), its install link, which
// asks in the app (Cursor, VS Code), or by its settings file (Claude
// Desktop, and Codex without its tool).

// agentApp is an AI app Leaflet can connect.
type agentApp struct {
	// id is the app's choice in externalClients, where it has one.
	id, label string
	// bundles are the app's names in the Applications folder on macOS.
	bundles []string
	// cli is the app's command-line tool.
	cli string
}

var agentApps = []agentApp{
	{id: "claude", label: "Claude Code", cli: "claude"},
	{id: "claude-desktop", label: "Claude Desktop", bundles: []string{"Claude.app"}},
	{id: "codex", label: "Codex", bundles: []string{"Codex.app"}, cli: "codex"},
	{id: "cursor", label: "Cursor", bundles: []string{"Cursor.app"}},
	{id: "vscode", label: "VS Code", bundles: []string{"Visual Studio Code.app"}},
}

// agentEnv is where to look for the apps: the home folder, the user's
// configuration folder (os.UserConfigDir), and the folders of tools.
type agentEnv struct{ home, config, path string }

// currentAgentEnv is this computer's: its tools on the PATH of a login
// shell, or where their installers put them.
func currentAgentEnv() agentEnv {
	home, _ := os.UserHomeDir()
	config, _ := os.UserConfigDir()
	dirs := append(filepath.SplitList(loginPath()), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local"),
		"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".bun", "bin"))
	return agentEnv{home, config, strings.Join(dirs, string(filepath.ListSeparator))}
}

// serverEntry is what an app's settings say about the server named
// leaflet.
type serverEntry struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
}

// agentStatus is an app as found on this computer.
type agentStatus struct {
	app agentApp
	// cli is the path of the app's command-line tool, if found.
	cli       string
	installed bool
	// entry is the leaflet server in the app's settings, or nil.
	entry *serverEntry
	// seen is when `leaflet mcp` last served the app.
	seen time.Time
}

// current says whether the app's leaflet server is this copy of
// Leaflet, on the pages in dir.
func (s agentStatus) current(exe, dir string) bool {
	return s.entry != nil && s.entry.Command == exe && s.entry.Env["LEAFLET_DATA_DIR"] == dir
}

// detectAgents finds the apps on this computer, and what their settings
// and seen, the times of mcp.SeenPrefix by client name, say of Leaflet.
func detectAgents(env agentEnv, seen map[string]string) []agentStatus {
	var out []agentStatus
	for _, app := range agentApps {
		s := agentStatus{app: app}
		if app.cli != "" {
			s.cli = findCLI(app.cli, env)
		}
		s.installed = s.cli != "" || hasBundle(app.bundles, env.home)
		switch app.id {
		case "claude":
			// Connected only by its tool, which keeps its settings file busy.
		case "codex":
			s.installed = s.installed || exists(filepath.Dir(codexConfig(env)))
		default:
			path, _ := jsonConfig(app.id, env)
			s.installed = s.installed || exists(filepath.Dir(path))
		}
		s.entry, _ = readEntry(app.id, env)
		for name, v := range seen {
			if clientOf(name) != app.id {
				continue
			}
			if unix, err := strconv.ParseInt(v, 10, 64); err == nil && time.Unix(unix, 0).After(s.seen) {
				s.seen = time.Unix(unix, 0)
			}
		}
		out = append(out, s)
	}
	return out
}

// clientOf is the app of agentApps that calls itself name in MCP's
// clientInfo, or "".
func clientOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "cursor"):
		return "cursor"
	case strings.Contains(n, "claude-code"), strings.Contains(n, "claude code"):
		return "claude"
	case strings.Contains(n, "claude"):
		return "claude-desktop"
	case strings.Contains(n, "codex"):
		return "codex"
	case strings.Contains(n, "visual studio code"), strings.Contains(n, "vscode"):
		return "vscode"
	}
	return ""
}

// connectAgent adds this copy of Leaflet, exe on the pages in dir, to
// an app's settings by its tool cli or its settings file; Cursor and VS
// Code are connected by their installLink instead.
func connectAgent(id, cli string, env agentEnv, exe, dir string) error {
	switch id {
	case "claude":
		if cli == "" {
			return errors.New("claude: command not found")
		}
		// It will not add a server it has, so the old one goes first.
		_ = runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
		return runCLI(cli, env, "mcp", "add", "-s", "user", "leaflet", "-e", "LEAFLET_DATA_DIR="+dir, "--", exe, "mcp")
	case "codex":
		if cli != "" {
			return runCLI(cli, env, "mcp", "add", "leaflet", "--env", "LEAFLET_DATA_DIR="+dir, "--", exe, "mcp")
		}
		_, snippet := agentSetup("codex", exe, dir)
		return editCodex(codexConfig(env), func(text string) string {
			text = strings.TrimRight(withoutCodexEntry(text), "\n")
			if text != "" {
				text += "\n\n"
			}
			return text + snippet + "\n"
		})
	}
	path, key := jsonConfig(id, env)
	return editServers(path, key, func(servers map[string]json.RawMessage) {
		servers["leaflet"] = mustJSON(map[string]any{"command": exe, "args": []string{"mcp"}, "env": map[string]string{"LEAFLET_DATA_DIR": dir}})
	})
}

// disconnectAgent removes Leaflet from an app's settings.
func disconnectAgent(id, cli string, env agentEnv) error {
	switch id {
	case "claude":
		return runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
	case "codex":
		if cli != "" {
			return runCLI(cli, env, "mcp", "remove", "leaflet")
		}
		return editCodex(codexConfig(env), withoutCodexEntry)
	}
	path, key := jsonConfig(id, env)
	return editServers(path, key, func(servers map[string]json.RawMessage) { delete(servers, "leaflet") })
}

// installLink is the link that has Cursor or VS Code ask to add this
// copy of Leaflet, or "" for the other apps.
func installLink(id, exe, dir string) string {
	server := map[string]any{"command": exe, "args": []string{"mcp"}, "env": map[string]string{"LEAFLET_DATA_DIR": dir}}
	switch id {
	case "cursor":
		config := base64.StdEncoding.EncodeToString(mustJSON(server))
		return "cursor://anysphere.cursor-deeplink/mcp/install?name=leaflet&config=" + url.QueryEscape(config)
	case "vscode":
		server["name"], server["type"] = "leaflet", "stdio"
		return "vscode:mcp/install?" + strings.ReplaceAll(url.QueryEscape(string(mustJSON(server))), "+", "%20")
	}
	return ""
}

// readEntry is the leaflet server in an app's settings, or nil.
func readEntry(id string, env agentEnv) (*serverEntry, error) {
	if id == "codex" {
		raw, err := os.ReadFile(codexConfig(env))
		if err != nil {
			return nil, ignoreMissing(err)
		}
		return codexEntry(string(raw)), nil
	}
	path, key := jsonConfig(id, env)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ignoreMissing(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var servers map[string]*serverEntry
	if r, ok := top[key]; ok {
		if err := json.Unmarshal(r, &servers); err != nil {
			return nil, err
		}
	}
	return servers["leaflet"], nil
}

// jsonConfig is an app's JSON settings file, and the key of its servers.
func jsonConfig(id string, env agentEnv) (path, key string) {
	switch id {
	case "claude":
		return filepath.Join(env.home, ".claude.json"), "mcpServers"
	case "claude-desktop":
		return filepath.Join(env.config, "Claude", "claude_desktop_config.json"), "mcpServers"
	case "cursor":
		return filepath.Join(env.home, ".cursor", "mcp.json"), "mcpServers"
	case "vscode":
		return filepath.Join(env.config, "Code", "User", "mcp.json"), "servers"
	}
	return "", ""
}

// editServers changes the servers in a JSON settings file, keeping the
// rest of it.
func editServers(path, key string, fn func(servers map[string]json.RawMessage)) error {
	top := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &top); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	servers := map[string]json.RawMessage{}
	if r, ok := top[key]; ok {
		if err := json.Unmarshal(r, &servers); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	fn(servers)
	top[key] = mustJSON(servers)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return err
	}
	return writeAtomic(path, b.Bytes())
}

// codexConfig is Codex's settings file.
func codexConfig(env agentEnv) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "config.toml")
	}
	return filepath.Join(env.home, ".codex", "config.toml")
}

func editCodex(path string, fn func(string) string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeAtomic(path, []byte(fn(string(raw))))
}

// codexTable is the TOML table a line heads, or "" for another line.
func codexTable(line string) string {
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(l, "[") {
		return ""
	}
	if i := strings.Index(l, "#"); i >= 0 {
		l = strings.TrimSpace(l[:i])
	}
	return strings.TrimSpace(strings.Trim(l, "[]"))
}

func isLeafletTable(table string) bool {
	return table == "mcp_servers.leaflet" || strings.HasPrefix(table, "mcp_servers.leaflet.")
}

var (
	codexCommand = regexp.MustCompile(`^\s*command\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
	codexDataDir = regexp.MustCompile(`LEAFLET_DATA_DIR\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
)

// codexEntry reads the leaflet server of a Codex config.toml, as its
// own tool writes it (an [mcp_servers.leaflet.env] table) or as
// agentSetup does (an inline env).
func codexEntry(text string) *serverEntry {
	var e *serverEntry
	in := false
	for _, line := range strings.Split(text, "\n") {
		if t := codexTable(line); t != "" {
			in = isLeafletTable(t)
			if in && e == nil {
				e = &serverEntry{Env: map[string]string{}}
			}
			continue
		}
		if !in {
			continue
		}
		if m := codexCommand.FindStringSubmatch(line); m != nil {
			e.Command = tomlString(m[1])
		}
		if m := codexDataDir.FindStringSubmatch(line); m != nil {
			e.Env["LEAFLET_DATA_DIR"] = tomlString(m[1])
		}
	}
	return e
}

// withoutCodexEntry is a Codex config.toml without the leaflet server.
func withoutCodexEntry(text string) string {
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		if t := codexTable(line); t != "" {
			in = isLeafletTable(t)
		}
		if !in {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// tomlString is the value of a TOML string, basic or literal; a basic
// string's escapes are JSON's.
func tomlString(s string) string {
	if strings.HasPrefix(s, "'") {
		return strings.Trim(s, "'")
	}
	var v string
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

// writeAtomic replaces a file whole, so an app reading it never finds it
// half written, and keeps its permissions.
func writeAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// findCLI is the path of an app's command-line tool: in the env's
// folders of tools, or the one the app keeps (Claude Desktop's Claude
// Code, newest first).
func findCLI(name string, env agentEnv) string {
	for _, d := range filepath.SplitList(env.path) {
		if p := filepath.Join(d, name); isExecutable(p) {
			return p
		}
	}
	var bundled []string
	switch name {
	case "claude":
		bundled, _ = filepath.Glob(filepath.Join(env.config, "Claude", "claude-code", "*", "*", "claude.app", "Contents", "MacOS", "claude"))
		slices.SortFunc(bundled, func(a, b string) int { return compareVersions(versionOf(b), versionOf(a)) })
	case "codex":
		bundled = []string{"/Applications/Codex.app/Contents/Resources/codex"}
	}
	for _, p := range bundled {
		if isExecutable(p) {
			return p
		}
	}
	return ""
}

// versionOf is the version folder of a Claude Desktop's Claude Code.
func versionOf(p string) string {
	rel := strings.Split(filepath.ToSlash(p), "/claude-code/")
	if len(rel) < 2 {
		return ""
	}
	return strings.Split(rel[1], "/")[0]
}

// compareVersions orders dotted versions by their numbers.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// hasBundle says whether one of bundles is in an Applications folder.
func hasBundle(bundles []string, home string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, b := range bundles {
		if exists(filepath.Join("/Applications", b)) || exists(filepath.Join(home, "Applications", b)) {
			return true
		}
	}
	return false
}

// runCLI runs an app's tool, with the PATH it finds its own tools on
// (Claude Code may be a Node script), and says what went wrong in its
// own words.
func runCLI(cli string, env agentEnv, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = append(os.Environ(), "PATH="+strings.Join([]string{filepath.Dir(cli), env.path}, string(filepath.ListSeparator)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// loginPath is the PATH of the user's login shell, which an app opened
// from the Finder does not get; it is read once.
var loginPath = sync.OnceValue(func() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("PATH")
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Interactive too, as version managers set PATH in .zshrc; the
	// marker keeps what the shell prints as it starts out of it.
	out, err := exec.CommandContext(ctx, sh, "-lic", `printf '\n__LEAFLET_PATH__%s\n' "$PATH"`).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if p, ok := strings.CutPrefix(line, "__LEAFLET_PATH__"); ok {
				return p
			}
		}
	}
	return os.Getenv("PATH")
})

// translocated says whether macOS runs this copy from where it cannot
// stay: a disk image, or a random folder it moved a download to. An
// app's settings would lose it.
func translocated(exe string) bool {
	return strings.Contains(exe, "/AppTranslocation/") || strings.HasPrefix(exe, "/Volumes/")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func mustJSON(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
