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
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tailscale/hujson"
)

// Connecting AI apps on this computer to `leaflet mcp` for the user, so
// nobody has to open a terminal or find a settings file: by the app's
// own command-line tool (Claude Code, OpenCode), its install link, which
// asks in the app (Cursor), or by its settings file (Claude Desktop,
// Codex and ChatGPT, and OpenCode without its tool).

// agentApp is an AI app Leaflet can connect.
type agentApp struct {
	// id is the app's choice in externalClients, where it has one.
	id, label string
	// bundles are the app's names in the Applications folder on macOS.
	bundles []string
	// windows are the folders, in %LOCALAPPDATA%, the app installs to on
	// Windows: its Microsoft Store package's, or its installer's.
	windows []string
	// cli is the app's command-line tool.
	cli string
	// settings is the app whose settings it reads, if another's.
	settings string
}

// settingsID is the app whose settings the app reads: its own, or, for
// ChatGPT, Codex's.
func (a agentApp) settingsID() string {
	if a.settings != "" {
		return a.settings
	}
	return a.id
}

// agentApps are in the order Settings lists them: the apps for chatting
// first, then those for code.
var agentApps = []agentApp{
	// The ChatGPT app that runs Codex inside, and reads Codex's settings;
	// an older ChatGPT runs no local servers.
	{id: "chatgpt", label: "ChatGPT", bundles: []string{"ChatGPT.app/Contents/Resources/codex-cli"}, cli: "codex", settings: "codex"},
	{id: "claude-desktop", label: "Claude Desktop", bundles: []string{"Claude.app"}, windows: []string{`Packages\Claude_pzs8sxrjxfjjc`, "AnthropicClaude"}},
	{id: "codex", label: "Codex", bundles: []string{"Codex.app"}, windows: []string{`Packages\OpenAI.Codex_2p2nqsd0c76g0`}, cli: "codex"},
	{id: "claude", label: "Claude Code", cli: "claude"},
	{id: "opencode", label: "OpenCode", bundles: []string{"OpenCode.app"}, cli: "opencode"},
	{id: "cursor", label: "Cursor", bundles: []string{"Cursor.app"}, windows: []string{`Programs\cursor`}},
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
		"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".opencode", "bin"))
	if runtime.GOOS == "windows" {
		// Where npm puts the tools it installs, which PATH may lack.
		dirs = append(dirs, filepath.Join(config, "npm"))
	}
	return agentEnv{home, config, strings.Join(dirs, string(filepath.ListSeparator))}
}

// serverEntry is what an app's settings say about the server named
// leaflet: how it starts, and whether it is turned off.
type serverEntry struct {
	Command string
	Args    []string
	Env     map[string]string
	Off     bool
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

// current says whether the app's leaflet server starts this copy of
// Leaflet's server on the pages in dir. Anything else, another copy or a
// server that would not start or is turned off, is for Update to mend.
func (s agentStatus) current(exe, dir string) bool {
	e := s.entry
	return e != nil && !e.Off && e.Command == exe && slices.Equal(e.Args, []string{"mcp"}) && e.Env["LEAFLET_DATA_DIR"] == dir
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
		s.installed = s.cli != "" || hasApp(app, env.home)
		switch app.id {
		case "claude":
			// Connected only by its tool, which keeps its settings file busy.
		case "chatgpt":
			// Its tool is Codex's, which it may keep inside.
			s.installed = hasApp(app, env.home)
		case "codex":
			// Codex of its own: not the one inside ChatGPT, which has a row.
			s.installed = s.cli != "" && !strings.Contains(s.cli, "/ChatGPT.app/") || hasApp(app, env.home)
		default:
			path, _ := jsonConfig(app.id, env)
			s.installed = s.installed || exists(filepath.Dir(path))
		}
		s.entry, _ = readEntry(app.settingsID(), env)
		// ChatGPT's Codex and Codex call themselves the same; what one has
		// used, both have.
		for name, v := range seen {
			if clientOf(name) != app.settingsID() {
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
	case strings.Contains(n, "opencode"):
		return "opencode"
	}
	return ""
}

// connectAgent adds this copy of Leaflet, exe on the pages in dir, to
// an app's settings by its tool cli or its settings file; Cursor is
// connected by its installLink instead.
func connectAgent(id, cli string, env agentEnv, exe, dir string) error {
	switch id {
	case "claude":
		if cli == "" {
			return errors.New("claude: command not found")
		}
		// It will not add a server it has, so the old one goes first, and
		// comes back as it was if the new one could not be added.
		old := claudeEntry(env)
		_ = runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
		err := runCLI(cli, env, "mcp", "add", "-s", "user", "leaflet", "-e", "LEAFLET_DATA_DIR="+dir, "--", exe, "mcp")
		if err != nil && old != nil {
			if rerr := runCLI(cli, env, "mcp", "add-json", "-s", "user", "leaflet", string(old)); rerr != nil {
				return fmt.Errorf("%w; putting the old entry back: %w", err, rerr)
			}
		}
		return err
	case "codex":
		// Not by its tool, which writes the whole file anew: other tables
		// reordered, numbers and empty lists respelled, comments lost.
		_, snippet := agentSetup("codex", exe, dir)
		want := &serverEntry{Command: exe, Args: []string{"mcp"}, Env: map[string]string{"LEAFLET_DATA_DIR": dir}}
		return editCodex(codexConfig(env), want, func(text string) (string, error) {
			text, err := withoutCodexEntry(text)
			if text = strings.TrimRight(text, "\n"); text != "" {
				text += "\n\n"
			}
			return text + snippet + "\n", err
		})
	case "opencode":
		if cli != "" {
			return runCLI(cli, env, "mcp", "add", "--global", "leaflet", "--env", "LEAFLET_DATA_DIR="+dir, "--", exe, "mcp")
		}
		path, keys := jsonConfig(id, env)
		return editServers(path, keys, map[string]any{"type": "local", "command": []string{exe, "mcp"}, "environment": map[string]string{"LEAFLET_DATA_DIR": dir}})
	}
	path, keys := jsonConfig(id, env)
	return editServers(path, keys, map[string]any{"command": exe, "args": []string{"mcp"}, "env": map[string]string{"LEAFLET_DATA_DIR": dir}})
}

// claudeEntry is the leaflet server in Claude Code's user settings, as
// it has it, or nil.
func claudeEntry(env agentEnv) json.RawMessage {
	path, keys := jsonConfig("claude", env)
	raw, err := os.ReadFile(path) // #nosec G304 -- Claude Code's own settings file.
	if err != nil {
		return nil
	}
	servers, err := nested(raw, keys)
	if err != nil {
		return nil
	}
	return servers["leaflet"]
}

// disconnectAgent removes Leaflet from an app's settings.
func disconnectAgent(id, cli string, env agentEnv) error {
	switch id {
	case "claude":
		return runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
	case "codex":
		return editCodex(codexConfig(env), nil, withoutCodexEntry)
	}
	// OpenCode's tool adds servers but does not remove them.
	path, keys := jsonConfig(id, env)
	return editServers(path, keys, nil)
}

// installLink is the link that has Cursor ask to add this copy of
// Leaflet, or "" for the other apps.
func installLink(id, exe, dir string) string {
	if id != "cursor" {
		return ""
	}
	server := map[string]any{"command": exe, "args": []string{"mcp"}, "env": map[string]string{"LEAFLET_DATA_DIR": dir}}
	config := base64.StdEncoding.EncodeToString(mustJSON(server))
	return "cursor://anysphere.cursor-deeplink/mcp/install?name=leaflet&config=" + url.QueryEscape(config)
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
	path, keys := jsonConfig(id, env)
	raw, err := os.ReadFile(path) // #nosec G304 -- an AI app's own settings file, at the place it keeps it.
	if err != nil {
		return nil, ignoreMissing(err)
	}
	servers, err := nested(raw, keys)
	if err != nil || servers["leaflet"] == nil {
		return nil, err
	}
	var e struct {
		Command     json.RawMessage   `json:"command"`
		Args        []string          `json:"args"`
		Env         map[string]string `json:"env"`
		Environment map[string]string `json:"environment"`
		Disabled    bool              `json:"disabled"`
		Enabled     *bool             `json:"enabled"`
	}
	if err := json.Unmarshal(servers["leaflet"], &e); err != nil {
		return nil, err
	}
	// OpenCode keeps the command and its arguments in one list, the
	// environment under its own name, and turns a server off by enabled
	// (before its second version) or disabled.
	out := &serverEntry{Args: e.Args, Env: e.Env, Off: e.Disabled || e.Enabled != nil && !*e.Enabled}
	if e.Environment != nil {
		out.Env = e.Environment
	}
	var argv []string
	if json.Unmarshal(e.Command, &argv) == nil && len(argv) > 0 {
		out.Command, out.Args = argv[0], argv[1:]
	} else {
		_ = json.Unmarshal(e.Command, &out.Command)
	}
	return out, nil
}

// jsonConfig is an app's JSON settings file, and the keys its servers
// are under.
func jsonConfig(id string, env agentEnv) (path string, keys []string) {
	switch id {
	case "claude":
		return filepath.Join(env.home, ".claude.json"), []string{"mcpServers"}
	case "claude-desktop":
		return filepath.Join(env.config, "Claude", "claude_desktop_config.json"), []string{"mcpServers"}
	case "cursor":
		return filepath.Join(env.home, ".cursor", "mcp.json"), []string{"mcpServers"}
	case "opencode":
		path = openCodeConfig(env)
		raw, _ := os.ReadFile(path) // #nosec G304 -- OpenCode's own settings file.
		return path, openCodeKeys(raw)
	}
	return "", nil
}

// openCodeConfig is OpenCode's own settings file, which may have
// comments (.jsonc).
func openCodeConfig(env agentEnv) string {
	dir := filepath.Join(env.home, ".config", "opencode")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dir = filepath.Join(x, "opencode")
	}
	if p := filepath.Join(dir, "opencode.jsonc"); exists(p) && !exists(filepath.Join(dir, "opencode.json")) {
		return p
	}
	return filepath.Join(dir, "opencode.json")
}

// openCodeKeys are where OpenCode's settings keep servers: under
// mcp.servers since its second version, under mcp before. A file with
// neither is taken as the second's.
func openCodeKeys(raw []byte) []string {
	mcp, err := nested(raw, []string{"mcp"})
	if err == nil && len(mcp) > 0 && mcp["servers"] == nil {
		return []string{"mcp"}
	}
	return []string{"mcp", "servers"}
}

// nested is the object under keys in a JSON document, or nil. The
// document may have comments and trailing commas (JSONC), as OpenCode's
// may.
func nested(raw []byte, keys []string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 {
		// Standardize works in place.
		std, err := hujson.Standardize(bytes.Clone(raw))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(std, &m); err != nil {
			return nil, err
		}
	}
	for _, k := range keys {
		if m == nil {
			return nil, nil
		}
		var next map[string]json.RawMessage
		if r := m[k]; r != nil {
			if err := json.Unmarshal(r, &next); err != nil {
				return nil, err
			}
		}
		m = next
	}
	return m, nil
}

// editServers sets the server named leaflet, under keys in a JSON
// settings file, to server, or removes it when server is nil. The rest of
// the file stays as it was, comments, order and spacing too; the server
// is written in the spacing of those around it.
func editServers(path string, keys []string, server any) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- an AI app's own settings file, at the place it keeps it.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if server == nil {
			return nil
		}
		raw = []byte("{}\n")
	}
	doc, err := hujson.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	ptr := func(keys []string) string {
		var b strings.Builder
		for _, k := range keys {
			b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k))
		}
		return b.String()
	}
	full := append(slices.Clone(keys), "leaflet")
	// A JSONC object may end in a comma, which hujson keeps as the last
	// member having an AfterExtra; the member that is last after the
	// change gets the comma, as the last one had.
	object := func(keys []string) *hujson.Object {
		if v := doc.Find(ptr(keys)); v != nil {
			obj, _ := v.Value.(*hujson.Object)
			return obj
		}
		return nil
	}
	endsInComma := func(obj *hujson.Object) bool {
		return obj != nil && len(obj.Members) > 0 && obj.Members[len(obj.Members)-1].Value.AfterExtra != nil
	}
	keepComma := func(obj *hujson.Object, comma bool) {
		if obj != nil && comma && len(obj.Members) > 0 && obj.Members[len(obj.Members)-1].Value.AfterExtra == nil {
			obj.Members[len(obj.Members)-1].Value.AfterExtra = hujson.Extra{}
		}
	}
	if server == nil {
		if doc.Find(ptr(full)) == nil {
			return nil
		}
		comma := endsInComma(object(keys))
		if err := doc.Patch(mustJSON([]map[string]string{{"op": "remove", "path": ptr(full)}})); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		keepComma(object(keys), comma)
		return writeAtomic(path, doc.Pack())
	}
	// The value goes in at the first of keys missing, holding the rest.
	at := 0
	for at < len(full)-1 && doc.Find(ptr(full[:at+1])) != nil {
		at++
	}
	value := server
	for i := len(full) - 1; i > at; i-- {
		value = map[string]any{full[i]: value}
	}
	unit := indentUnit(doc)
	indent := strings.Repeat(unit, at+1)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent(indent, unit)
	if err := enc.Encode(value); err != nil {
		return err
	}
	op := "add"
	if doc.Find(ptr(full[:at+1])) != nil {
		op = "replace"
	}
	comma := endsInComma(object(full[:at]))
	patch := `[{"op": "` + op + `", "path": ` + string(mustJSON(ptr(full[:at+1]))) + `, "value": ` + strings.TrimSuffix(b.String(), "\n") + `}]`
	if err := doc.Patch([]byte(patch)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// Lay out the new member as its neighbours are: on a line of its own.
	if obj := object(full[:at]); obj != nil {
		keepComma(obj, comma)
		for i := range obj.Members {
			m := &obj.Members[i]
			var name string
			_ = json.Unmarshal(m.Name.Value.(hujson.Literal), &name)
			if name != full[at] {
				continue
			}
			if op == "add" {
				m.Name.BeforeExtra = hujson.Extra("\n" + indent)
			}
			m.Value.BeforeExtra = hujson.Extra(" ")
		}
		if !bytes.Contains(obj.AfterExtra, []byte("\n")) {
			obj.AfterExtra = hujson.Extra("\n" + strings.Repeat(unit, at))
		}
	}
	return writeAtomic(path, doc.Pack())
}

// indentUnit is the indentation a JSON document steps in by: a tab, or
// as many spaces as its first member is in; two spaces for a document
// without one.
func indentUnit(doc hujson.Value) string {
	if obj, ok := doc.Value.(*hujson.Object); ok && len(obj.Members) > 0 {
		before := string(obj.Members[0].Name.BeforeExtra)
		if i := strings.LastIndex(before, "\n"); i >= 0 {
			if ws := before[i+1:]; ws != "" && strings.Trim(ws, " \t") == "" {
				return ws
			}
		}
	}
	return "  "
}

// codexConfig is Codex's settings file.
func codexConfig(env agentEnv) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "config.toml")
	}
	return filepath.Join(env.home, ".codex", "config.toml")
}

// editCodex changes Codex's config.toml by fn, and writes the change only
// when the file still reads as it did, but for the leaflet server, which
// is want after it (nil: gone). A spelling of the file fn cannot change so
// is left as it is, and said.
func editCodex(path string, want *serverEntry, fn func(string) (string, error)) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- Codex's own settings file.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	before, err := decodeTOML(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	text, err := fn(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	after, err := decodeTOML([]byte(text))
	if err != nil {
		return fmt.Errorf("%s: could not change it safely: %w", path, err)
	}
	got := tomlEntry(after)
	for _, doc := range []map[string]any{before, after} {
		// An mcp_servers that held only leaflet may come or go with it.
		if servers := tomlServers(doc); servers != nil {
			if delete(servers, "leaflet"); len(servers) == 0 {
				delete(doc, "mcp_servers")
			}
		}
	}
	if !reflect.DeepEqual(before, after) || (want == nil) != (got == nil) || want != nil && !reflect.DeepEqual(*want, *got) {
		return fmt.Errorf("%s: could not change it safely", path)
	}
	return writeAtomic(path, []byte(text))
}

func decodeTOML(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// tomlServers is the mcp_servers table of a decoded config.toml, or nil.
func tomlServers(doc map[string]any) map[string]any {
	servers, _ := doc["mcp_servers"].(map[string]any)
	return servers
}

// tomlEntry is the leaflet server of a decoded config.toml, or nil.
func tomlEntry(doc map[string]any) *serverEntry {
	server, ok := tomlServers(doc)["leaflet"].(map[string]any)
	if !ok {
		return nil
	}
	e := &serverEntry{Env: map[string]string{}}
	e.Command, _ = server["command"].(string)
	if args, ok := server["args"].([]any); ok {
		e.Args = []string{}
		for _, a := range args {
			s, _ := a.(string)
			e.Args = append(e.Args, s)
		}
	}
	if env, ok := server["env"].(map[string]any); ok {
		for k, v := range env {
			e.Env[k], _ = v.(string)
		}
	}
	if enabled, ok := server["enabled"].(bool); ok {
		e.Off = !enabled
	}
	return e
}

// codexEntry reads the leaflet server of a Codex config.toml, however
// its keys are spelled, or nil.
func codexEntry(text string) *serverEntry {
	doc, err := decodeTOML([]byte(text))
	if err != nil {
		return nil
	}
	return tomlEntry(doc)
}

// withoutCodexEntry is a Codex config.toml without the leaflet server:
// without each table header and key under mcp_servers.leaflet, as TOML
// reads them (quoted, spaced, dotted or inline), and the comments among
// them. Everything else stays as it was, strings that look like tables too.
func withoutCodexEntry(text string) (string, error) {
	data := []byte(text)
	p := unstable.Parser{KeepComments: true}
	p.Reset(data)
	type expr struct {
		start   int
		leaflet bool
		comment bool
	}
	var exprs []expr
	var table []string
	for p.NextExpression() {
		n := p.Expression()
		e := expr{start: -1}
		var key []string
		if n.Kind != unstable.Comment {
			for it := n.Key(); it.Next(); {
				k := it.Node()
				if e.start < 0 {
					e.start = int(k.Raw.Offset)
				}
				key = append(key, string(k.Data))
			}
		}
		switch n.Kind {
		case unstable.Comment:
			e.start, e.comment = int(n.Raw.Offset), true
			e.leaflet = isLeafletKey(table)
		case unstable.Table, unstable.ArrayTable:
			table = key
			e.leaflet = isLeafletKey(key)
		default:
			e.leaflet = isLeafletKey(append(slices.Clone(table), key...))
		}
		// From the start of its line.
		e.start = strings.LastIndex(text[:e.start], "\n") + 1
		exprs = append(exprs, e)
	}
	if err := p.Error(); err != nil {
		return "", err
	}
	// A comment among the leaflet server's lines goes with them, unless
	// what follows it is not the server's: then it heads that.
	for i := len(exprs) - 1; i >= 0; i-- {
		if exprs[i].comment && exprs[i].leaflet {
			exprs[i].leaflet = i+1 < len(exprs) && exprs[i+1].leaflet
		}
	}
	var b strings.Builder
	at := 0
	for i, e := range exprs {
		end := len(text)
		if i+1 < len(exprs) {
			end = exprs[i+1].start
		}
		if e.leaflet {
			// Keep the blank lines after it.
			cut := strings.TrimRight(text[e.start:end], " \t\r\n")
			b.WriteString(text[at:e.start])
			at = e.start + len(cut)
			at += len(text[at:end]) - len(strings.TrimLeft(text[at:end], " \t\r"))
			if at < end && text[at] == '\n' {
				at++
			}
			// Unless a blank line comes before it already, or nothing does.
			if out := b.String(); out == "" || strings.HasSuffix(out, "\n\n") {
				at = end
			}
		}
	}
	b.WriteString(text[at:])
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// isLeafletKey says whether a TOML key is the leaflet server or in it.
func isLeafletKey(key []string) bool {
	return len(key) >= 2 && key[0] == "mcp_servers" && key[1] == "leaflet"
}

// writeAtomic replaces a file whole, so an app reading it never finds it
// half written, and keeps its permissions. A link, as to settings kept in
// a dotfiles folder, stays a link: the file it leads to is replaced.
func writeAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	} else if to, err := os.Readlink(path); err == nil {
		// A link to a file not made yet makes it.
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(path), to)
		}
		path = to
	}
	// A new file only its owner reads, as settings may hold keys.
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
	// On Windows, a tool is a program or a script with one of the
	// extensions that run, such as claude.exe or npm's codex.cmd.
	names := []string{name}
	if runtime.GOOS == "windows" {
		names = names[:0]
		for _, ext := range windowsExts() {
			names = append(names, name+ext)
		}
	}
	for _, d := range filepath.SplitList(env.path) {
		for _, n := range names {
			if p := filepath.Join(d, n); isExecutable(p) {
				return p
			}
		}
	}
	var bundled []string
	switch name {
	case "claude":
		tool := filepath.Join("claude.app", "Contents", "MacOS", "claude")
		if runtime.GOOS == "windows" {
			tool = "claude.exe"
		}
		bundled, _ = filepath.Glob(filepath.Join(env.config, "Claude", "claude-code", "*", "*", tool))
		slices.SortFunc(bundled, func(a, b string) int { return compareVersions(versionOf(b), versionOf(a)) })
	case "codex":
		for _, apps := range []string{"/Applications", filepath.Join(env.home, "Applications")} {
			bundled = append(bundled, filepath.Join(apps, "Codex.app", "Contents", "Resources", "codex"),
				filepath.Join(apps, "ChatGPT.app", "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"))
		}
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

// hasApp says whether the app is installed: one of its bundles in an
// Applications folder on macOS, or one of its folders in %LOCALAPPDATA%
// on Windows.
func hasApp(app agentApp, home string) bool {
	if runtime.GOOS == "windows" {
		for _, d := range app.windows {
			if exists(filepath.Join(home, "AppData", "Local", d)) {
				return true
			}
		}
		return false
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, b := range app.bundles {
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
	cmd := exec.CommandContext(ctx, cli, args...) // #nosec G204 -- an AI app's own command-line tool, found where it installs, with arguments Leaflet writes.
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
	out, err := exec.CommandContext(ctx, sh, "-lic", `printf '\n__LEAFLET_PATH__%s\n' "$PATH"`).Output() // #nosec G204 -- the user's own login shell, from $SHELL.
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

// isExecutable says whether p is a file that runs: by its permissions,
// or on Windows, which has none, by its extension.
func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return slices.Contains(windowsExts(), strings.ToLower(filepath.Ext(p)))
	}
	return fi.Mode().Perm()&0o111 != 0
}

// windowsExts are the extensions of files Windows runs as commands, in
// the order it tries them.
func windowsExts() []string {
	var exts []string
	for _, e := range strings.Split(strings.ToLower(os.Getenv("PATHEXT")), ";") {
		if e != "" {
			exts = append(exts, e)
		}
	}
	if len(exts) == 0 {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	return exts
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
