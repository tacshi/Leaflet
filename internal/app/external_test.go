package app

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/locales"
)

func TestAgentSetup(t *testing.T) {
	exe := "/Applications/Leaflet.app/Contents/MacOS/Leaflet"
	dir := "/Users/me/Library/Application Support/Leaflet"
	// dir as one word of the shell a terminal here opens.
	word := "'" + dir + "'"
	if runtime.GOOS == "windows" {
		word = `"` + dir + `"`
	}
	for _, cl := range externalClients {
		where, snippet := agentSetup(cl.id, exe, dir)
		if where == "" || snippet == "" {
			t.Errorf("%s: no setup", cl.id)
			continue
		}
		switch cl.id {
		case "claude":
			want := "claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=" + word + " -- " + exe + " mcp"
			if snippet != want {
				t.Errorf("claude:\n%s\nwant\n%s", snippet, want)
			}
		case "opencode":
			want := "opencode mcp add --global leaflet --env LEAFLET_DATA_DIR=" + word + " -- " + exe + " mcp"
			if snippet != want {
				t.Errorf("opencode:\n%s\nwant\n%s", snippet, want)
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

// On Windows the paths go in double quotes, which PowerShell and cmd.exe
// both take one word from; cmd.exe keeps single quotes as they are.
func TestWindowsQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Program Files\Leaflet\Leaflet.exe`: `"C:\Program Files\Leaflet\Leaflet.exe"`,
		`C:\Users\rocky\AppData\Local\Leaflet`: `C:\Users\rocky\AppData\Local\Leaflet`,
		`C:\Users\O'Brien\Leaflet`:             `"C:\Users\O'Brien\Leaflet"`,
		"":                                     `""`,
	} {
		if got := windowsQuote(in); got != want {
			t.Errorf("windowsQuote(%q) = %s, want %s", in, got, want)
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

// While an app's install link waits to be finished in the app, Connect
// opens the link again, as when its question there was closed, and waits
// longer with the watch already on the app's settings.
func TestConnectOpensTheInstallLinkAgain(t *testing.T) {
	was := openInstallLink
	t.Cleanup(func() { openInstallLink = was })
	var opened []string
	openInstallLink = func(link string) error { opened = append(opened, link); return nil }
	a := templatesApp(t)
	s := agentStatus{app: agentApp{id: "cursor", label: "Cursor"}, installed: true}
	a.external.waiting = "cursor"
	soon := time.Now().Add(time.Second).UnixNano()
	a.external.waitUntil.Store(soon)
	dir := t.TempDir()
	tt := ui.NewTester(func(c *ui.Context) {
		a.agentRow(c, s, "/Applications/Leaflet.app/Contents/MacOS/Leaflet", dir, false, true)
	}, 700, 100)
	tt.Frame()
	if !tt.HasText(locales.ExternalFinishIn("Cursor")) {
		t.Fatalf("row %v", tt.Texts())
	}
	if err := tt.Click(locales.ExternalConnect()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(opened) != 1 || !strings.HasPrefix(opened[0], "cursor://") || a.external.waiting != "cursor" || a.external.waitUntil.Load() <= soon {
		t.Errorf("opened %q, waiting for %q until %v", opened, a.external.waiting, time.Unix(0, a.external.waitUntil.Load()))
	}
}
