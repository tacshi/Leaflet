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

	"leaflet/locales"
)

type agentApp struct {
	id, label string
	bundles   []string
	windows   []string
	cli       string
	settings  string
}

func (a agentApp) settingsID() string {
	if a.settings != "" {
		return a.settings
	}
	return a.id
}

var agentApps = []agentApp{
	{id: "chatgpt", label: "ChatGPT", bundles: []string{"ChatGPT.app/Contents/Resources/codex-cli"}, cli: "codex", settings: "codex"},
	{id: "claude-desktop", label: "Claude Desktop", bundles: []string{"Claude.app"}, windows: []string{`Packages\Claude_pzs8sxrjxfjjc`, "AnthropicClaude"}},
	{id: "codex", label: "Codex", bundles: []string{"Codex.app"}, windows: []string{`Packages\OpenAI.Codex_2p2nqsd0c76g0`}, cli: "codex"},
	{id: "claude", label: "Claude Code", cli: "claude"},
	{id: "opencode", label: "OpenCode", bundles: []string{"OpenCode.app"}, cli: "opencode"},
	{id: "cursor", label: "Cursor", bundles: []string{"Cursor.app"}, windows: []string{`Programs\cursor`}},
}

type agentEnv struct{ home, config, path string }

func currentAgentEnv() agentEnv {
	home, _ := os.UserHomeDir()
	config, _ := os.UserConfigDir()
	dirs := append(filepath.SplitList(loginPath()), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local"),
		"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".opencode", "bin"))
	if runtime.GOOS == "windows" {
		dirs = append(dirs, filepath.Join(config, "npm"))
	}
	return agentEnv{home, config, strings.Join(dirs, string(filepath.ListSeparator))}
}

type serverEntry struct {
	Command string
	Args    []string
	Env     map[string]string
	Off     bool
}

type agentStatus struct {
	app       agentApp
	cli       string
	installed bool
	entry     *serverEntry
	seen      time.Time
}

func (s agentStatus) current(exe, dir string) bool {
	e := s.entry
	return e != nil && !e.Off && e.Command == exe && slices.Equal(e.Args, []string{"mcp"}) && e.Env["LEAFLET_DATA_DIR"] == dir
}

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
		case "chatgpt":
			s.installed = hasApp(app, env.home)
		case "codex":
			s.installed = s.cli != "" && !strings.Contains(s.cli, "/ChatGPT.app/") || hasApp(app, env.home)
		default:
			path, _ := jsonConfig(app.id, env)
			s.installed = s.installed || exists(filepath.Dir(path))
		}
		s.entry, _ = readEntry(app.settingsID(), env)
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

func connectAgent(id, cli string, env agentEnv, exe, dir string) error {
	switch id {
	case "claude":
		if cli == "" {
			return errors.New(locales.ExternalClaudeNotFound())
		}
		old := claudeEntry(env)
		_ = runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
		err := runCLI(cli, env, "mcp", "add", "-s", "user", "leaflet", "-e", "LEAFLET_DATA_DIR="+dir, "--", exe, "mcp")
		if err != nil && old != nil {
			if rerr := runCLI(cli, env, "mcp", "add-json", "-s", "user", "leaflet", string(old)); rerr != nil {
				return errors.New(locales.ExternalOldEntryNotBack(err.Error(), rerr.Error()))
			}
		}
		return err
	case "codex":
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

func claudeEntry(env agentEnv) json.RawMessage {
	path, keys := jsonConfig("claude", env)
	raw, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil
	}
	servers, err := nested(raw, keys)
	if err != nil {
		return nil
	}
	return servers["leaflet"]
}

func disconnectAgent(id, cli string, env agentEnv) error {
	switch id {
	case "claude":
		return runCLI(cli, env, "mcp", "remove", "-s", "user", "leaflet")
	case "codex":
		return editCodex(codexConfig(env), nil, withoutCodexEntry)
	}
	path, keys := jsonConfig(id, env)
	return editServers(path, keys, nil)
}

func installLink(id, exe, dir string) string {
	if id != "cursor" {
		return ""
	}
	server := map[string]any{"command": exe, "args": []string{"mcp"}, "env": map[string]string{"LEAFLET_DATA_DIR": dir}}
	config := base64.StdEncoding.EncodeToString(mustJSON(server))
	return "cursor://anysphere.cursor-deeplink/mcp/install?name=leaflet&config=" + url.QueryEscape(config)
}

func readEntry(id string, env agentEnv) (*serverEntry, error) {
	if id == "codex" {
		raw, err := os.ReadFile(codexConfig(env)) // #nosec G304
		if err != nil {
			return nil, ignoreMissing(err)
		}
		return codexEntry(string(raw)), nil
	}
	path, keys := jsonConfig(id, env)
	raw, err := os.ReadFile(path) // #nosec G304
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
		raw, _ := os.ReadFile(path) // #nosec G304
		return path, openCodeKeys(raw)
	}
	return "", nil
}

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

func openCodeKeys(raw []byte) []string {
	mcp, err := nested(raw, []string{"mcp"})
	if err == nil && len(mcp) > 0 && mcp["servers"] == nil {
		return []string{"mcp"}
	}
	return []string{"mcp", "servers"}
}

func nested(raw []byte, keys []string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 {
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

func editServers(path string, keys []string, server any) error {
	raw, err := os.ReadFile(path) // #nosec G304
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

func codexConfig(env agentEnv) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return filepath.Join(h, "config.toml")
	}
	return filepath.Join(env.home, ".codex", "config.toml")
}

func editCodex(path string, want *serverEntry, fn func(string) (string, error)) error {
	raw, err := os.ReadFile(path) // #nosec G304
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
		return fmt.Errorf("%s: %s: %w", path, locales.ExternalCouldNotChangeSafely(), err)
	}
	got := tomlEntry(after)
	for _, doc := range []map[string]any{before, after} {
		if servers := tomlServers(doc); servers != nil {
			if delete(servers, "leaflet"); len(servers) == 0 {
				delete(doc, "mcp_servers")
			}
		}
	}
	if !reflect.DeepEqual(before, after) || (want == nil) != (got == nil) || want != nil && !reflect.DeepEqual(*want, *got) {
		return fmt.Errorf("%s: %s", path, locales.ExternalCouldNotChangeSafely())
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

func tomlServers(doc map[string]any) map[string]any {
	servers, _ := doc["mcp_servers"].(map[string]any)
	return servers
}

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

func codexEntry(text string) *serverEntry {
	doc, err := decodeTOML([]byte(text))
	if err != nil {
		return nil
	}
	return tomlEntry(doc)
}

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
		e.start = strings.LastIndex(text[:e.start], "\n") + 1
		exprs = append(exprs, e)
	}
	if err := p.Error(); err != nil {
		return "", err
	}
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
			cut := strings.TrimRight(text[e.start:end], " \t\r\n")
			b.WriteString(text[at:e.start])
			at = e.start + len(cut)
			at += len(text[at:end]) - len(strings.TrimLeft(text[at:end], " \t\r"))
			if at < end && text[at] == '\n' {
				at++
			}
			if out := b.String(); out == "" || strings.HasSuffix(out, "\n\n") {
				at = end
			}
		}
	}
	b.WriteString(text[at:])
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func isLeafletKey(key []string) bool {
	return len(key) >= 2 && key[0] == "mcp_servers" && key[1] == "leaflet"
}

func writeAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	} else if to, err := os.Readlink(path); err == nil {
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(path), to)
		}
		path = to
	}
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

func findCLI(name string, env agentEnv) string {
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

func versionOf(p string) string {
	rel := strings.Split(filepath.ToSlash(p), "/claude-code/")
	if len(rel) < 2 {
		return ""
	}
	return strings.Split(rel[1], "/")[0]
}

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

func runCLI(cli string, env agentEnv, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...) // #nosec G204
	hideConsole(cmd)
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

var loginPath = sync.OnceValue(func() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("PATH")
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	if p, ok := shellPath(sh, 5*time.Second); ok {
		return p
	}
	return os.Getenv("PATH")
})

func shellPath(sh string, wait time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-lic", `printf '\n__LEAFLET_PATH__%s\n' "$PATH"`) // #nosec G204
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output()
	for line := range strings.SplitSeq(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "__LEAFLET_PATH__"); ok {
			return p, true
		}
	}
	return "", false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

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
