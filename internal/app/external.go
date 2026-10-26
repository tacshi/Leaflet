package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/theme"
)

// External agents: how to connect AI apps on this computer to
// `leaflet mcp` (internal/mcp), which is this app's own executable.

var externalClients = []struct{ id, label string }{
	{"claude", "Claude Code"}, {"codex", "Codex"}, {"cursor", "Cursor"}, {"vscode", "VS Code"}, {"other", "Other"},
}

// external is the client the section shows the setup of.
var external struct{ client int }

func (a *App) externalSettings(c *ui.Context) {
	pal := theme.Of(c)
	heading(c, "External agents", "AI apps on this computer, such as Claude Code, Codex and Cursor, can find, read and edit your pages. "+
		"What they change shows here as they change it, and each page keeps a version from before their edits in its history.")
	where, snippet := agentSetup(externalClients[external.client].id, executable(), a.st.Dir)
	card(c, "connect", func() {
		ui.Row(c).Padding(12, 16).FillWidth().Children(func() {
			labels := make([]string, len(externalClients))
			for i, cl := range externalClients {
				labels[i] = cl.label
			}
			ui.Segmented(c.Key("client"), &external.client, labels...)
		})
		ui.Column(c).Padding(0, 16, 14).Gap(8).FillWidth().Children(func() {
			ui.Text(c, where).FontSize(12).TextColor(pal.TextMuted)
			ui.Row(c).FillWidth().Padding(8, 8, 8, 12).Gap(8).Radius(8).Border(1, pal.Divider).Background(pal.Ground).AlignItems(ui.Start).Children(func() {
				ui.Text(c, snippet).Font("monospace").FontSize(12).TextColor(pal.Text).Wrap().Selectable().Grow(1).MinWidth(0)
				tools(c, func() {
					if iconButton(c, "copy", "copy", "Copy").Clicked() {
						c.WriteClipboard(snippet)
						c.Toast("Copied")
					}
				})
			})
		})
	})
}

// executable is the path of the app's executable, which serves MCP when
// run as `<executable> mcp`.
func executable() string {
	p, err := os.Executable()
	if err != nil {
		return "leaflet"
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return p
}

// agentSetup is how to connect a client to the pages in dir: where the
// setup goes, and the setup. The data folder is spelled out, so the
// server opens this app's pages whatever it is started as.
func agentSetup(client, exe, dir string) (where, snippet string) {
	server := `"command": ` + jsonString(exe) + `,
      "args": ["mcp"],
      "env": { "LEAFLET_DATA_DIR": ` + jsonString(dir) + ` }`
	switch client {
	case "claude":
		return "Run in a terminal:", "claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=" + shellQuote(dir) + " -- " + shellQuote(exe) + " mcp"
	case "codex":
		return "Add to ~/.codex/config.toml:", `[mcp_servers.leaflet]
command = ` + jsonString(exe) + `
args = ["mcp"]
env = { LEAFLET_DATA_DIR = ` + jsonString(dir) + ` }`
	case "cursor":
		return "Add to ~/.cursor/mcp.json:", `{
  "mcpServers": {
    "leaflet": {
      ` + server + `
    }
  }
}`
	case "vscode":
		return "Add to your user mcp.json (MCP: Open User Configuration):", `{
  "servers": {
    "leaflet": {
      "type": "stdio",
      ` + server + `
    }
  }
}`
	}
	return "For Claude Desktop (claude_desktop_config.json) and other apps that read an mcpServers JSON:", `{
  "mcpServers": {
    "leaflet": {
      ` + server + `
    }
  }
}`
}

// jsonString is s as a JSON string, which TOML reads the same.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// shellQuote is s as one word of a POSIX shell.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"\\$`!*?;&|<>()[]{}#~\t\n") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
