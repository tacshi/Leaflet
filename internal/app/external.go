package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/mcp"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// External agents: the AI apps on this computer connected to `leaflet
// mcp` (internal/mcp), which is this app's own executable, with a click
// (connect.go), or by hand.

var externalClients = []struct{ id, label string }{
	{"codex", "Codex"}, {"claude", "Claude Code"}, {"opencode", "OpenCode"}, {"cursor", "Cursor"}, {"vscode", "VS Code"}, {"other", "Other"},
}

// external is the section's state: the apps found on this computer,
// what is being done to one, and the client the setup by hand shows.
var external struct {
	agents   []agentStatus
	checked  time.Time
	checking bool
	// busy is the app being connected, or disconnected while
	// disconnecting; waiting is the app asked by its install link, until
	// its settings have Leaflet.
	busy, waiting string
	disconnecting bool
	byHand        bool
	client        int
}

func (a *App) externalSettings(c *ui.Context) {
	pal := theme.Of(c)
	heading(c, locales.SettingsSectionExternal(), locales.ExternalDesc())
	// Looked for again as the section shows, and as agents write, which
	// wakes the app, so a connection shows once an app makes it.
	if !external.checking && time.Since(external.checked) > 3*time.Second {
		a.checkAgents()
	}
	exe, dir := executable(), a.st.Dir
	moved := translocated(exe)
	if moved {
		notice(c, "warn", "triangle-alert", locales.ExternalMoveFirst(), nil)
	}
	var found []agentStatus
	for _, s := range external.agents {
		if s.installed {
			found = append(found, s)
		}
	}
	switch {
	case external.checked.IsZero():
		ui.Text(c, locales.ExternalLooking()).FontSize(13).TextColor(pal.TextMuted)
	case len(found) == 0:
		ui.Text(c, locales.ExternalNoneFound()).FontSize(13).TextColor(pal.TextMuted)
	default:
		card(c, "agents", func() {
			for i, s := range found {
				a.agentRow(c, s, exe, dir, moved, i == 0)
			}
		})
	}
	ui.Collapsible(c.Key("by-hand"), locales.ExternalByHand(), &external.byHand, func() {
		a.setupByHand(c, exe, dir)
	})
}

// agentRow is an app found: how it stands with Leaflet, said in a line
// of its own color under its name, and what to do.
func (a *App) agentRow(c *ui.Context, s agentStatus, exe, dir string, moved, first bool) {
	pal := theme.Of(c)
	id := s.app.id
	// The line is always there, so the row keeps its height as it changes.
	icon, text, color := "", locales.ExternalNotConnected(), pal.TextMuted
	switch {
	case s.entry == nil && external.waiting == id:
		icon, text = "clock", locales.ExternalFinishIn(s.app.label)
	case s.entry == nil:
	case !s.current(exe, dir):
		icon, text, color = "circle-alert", locales.ExternalOtherCopy(), pal.WarnText
	case !s.seen.IsZero():
		icon, text, color = "check", locales.ExternalLastUsed(humanTime(s.seen)), pal.OkText
	case id == "chatgpt":
		// Its chats run no local servers; its Codex mode does.
		icon, text, color = "rotate-cw", locales.ExternalAddedChatGPT(), pal.WarnText
	default:
		icon, text, color = "rotate-cw", locales.ExternalAdded(s.app.label), pal.WarnText
	}
	row := ui.Row(c.Key(id)).Padding(12, 16).Gap(16).AlignItems(ui.Center).FillWidth()
	if !first {
		row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			ui.Text(c, s.app.label).FontSize(14).FontWeight(500).TextColor(pal.Text)
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				if icon != "" {
					ui.Icon(c, theme.Icon(icon)).FontSize(13).TextColor(color)
				}
				ui.Text(c, text).FontSize(12).TextColor(color)
			})
		})
		switch {
		case external.busy == id:
			label := locales.ExternalConnecting()
			if external.disconnecting {
				label = locales.ExternalDisconnecting()
			}
			ui.Button(c.Key("busy"), "").Label(label).Disabled(true).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13)
					ui.Text(c, label).SingleLine()
				})
			})
		case s.entry != nil && s.current(exe, dir):
			if button(c, "disconnect", "", locales.ExternalDisconnect()).Disabled(external.busy != "").Clicked() {
				a.disconnectAgent(s)
			}
		default:
			label := locales.ExternalConnect()
			if s.entry != nil {
				label = locales.ExternalUpdate()
			}
			if primary(c, "connect", "", label).Disabled(moved || external.busy != "").Clicked() {
				a.connectAgent(s, exe, dir)
			}
		}
	})
}

// setupByHand is each client's setup to copy, for an app not found or
// not listed.
func (a *App) setupByHand(c *ui.Context, exe, dir string) {
	pal := theme.Of(c)
	where, snippet := agentSetup(externalClients[external.client].id, exe, dir)
	card(c, "connect", func() {
		ui.Row(c).Padding(12, 16).FillWidth().Children(func() {
			labels := make([]string, len(externalClients))
			for i, cl := range externalClients {
				labels[i] = cl.label
				if cl.id == "other" {
					labels[i] = locales.ExternalClientOther()
				}
			}
			ui.Segmented(c.Key("client"), &external.client, labels...)
		})
		ui.Column(c).Padding(0, 16, 14).Gap(8).FillWidth().Children(func() {
			ui.Text(c, where).FontSize(12).TextColor(pal.TextMuted)
			ui.Row(c).FillWidth().Padding(8, 8, 8, 12).Gap(8).Radius(8).Border(1, pal.Divider).Background(pal.Ground).AlignItems(ui.Start).Children(func() {
				ui.Text(c, snippet).Font("monospace").FontSize(12).TextColor(pal.Text).Wrap().Selectable().Grow(1).MinWidth(0)
				tools(c, func() {
					if iconButton(c, "copy", "copy", locales.CommonCopy()).Clicked() {
						c.WriteClipboard(snippet)
						c.Toast(locales.CommonCopied())
					}
				})
			})
		})
	})
}

// checkAgents looks for the apps again, away from the window's thread.
func (a *App) checkAgents() {
	external.checking = true
	go func() {
		found := a.findAgents()
		a.update(func() { a.foundAgents(found) })
	}()
}

func (a *App) findAgents() []agentStatus {
	return detectAgents(currentAgentEnv(), a.st.ValuesWithPrefix(mcp.SeenPrefix))
}

func (a *App) foundAgents(found []agentStatus) {
	external.agents, external.checked, external.checking = found, time.Now(), false
	for _, s := range found {
		if s.app.id == external.waiting && s.entry != nil {
			external.waiting = ""
		}
	}
}

// connectAgent connects an app: by its install link, which asks in the
// app, so the section waits to see its settings change; or here.
func (a *App) connectAgent(s agentStatus, exe, dir string) {
	if link := installLink(s.app.id, exe, dir); link != "" {
		external.waiting = s.app.id
		if err := mygo.Shell.OpenExternal(link); err != nil {
			external.waiting = ""
			a.toast(locales.ExternalConnectFailed(s.app.label, err.Error()))
			return
		}
		go a.awaitAgent(s.app.id)
		return
	}
	external.disconnecting = false
	a.changeAgent(s, func() error { return connectAgent(s.app.settingsID(), s.cli, currentAgentEnv(), exe, dir) }, locales.ExternalConnectFailed)
}

func (a *App) disconnectAgent(s agentStatus) {
	external.disconnecting = true
	a.changeAgent(s, func() error { return disconnectAgent(s.app.settingsID(), s.cli, currentAgentEnv()) }, locales.ExternalDisconnectFailed)
}

// changeAgent runs fn on an app's settings, then looks at them again
// before the row stops being busy, so it goes straight to how the app
// now stands.
func (a *App) changeAgent(s agentStatus, fn func() error, failed func(app, err string) string) {
	external.busy = s.app.id
	go func() {
		err := fn()
		found := a.findAgents()
		a.update(func() {
			external.busy = ""
			a.foundAgents(found)
			if err != nil {
				a.toast(failed(s.app.label, err.Error()))
			}
		})
	}()
}

// awaitAgent watches an app's settings for a few minutes after its
// install link opened, as the window may not redraw meanwhile.
func (a *App) awaitAgent(id string) {
	env := currentAgentEnv()
	for range 90 {
		time.Sleep(2 * time.Second)
		if e, _ := readEntry(id, env); e != nil {
			a.update(a.checkAgents)
			return
		}
	}
	a.update(func() {
		if external.waiting == id {
			external.waiting = ""
		}
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
		return locales.ExternalRunInTerminal(), "claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=" + shellQuote(dir) + " -- " + shellQuote(exe) + " mcp"
	case "opencode":
		return locales.ExternalRunInTerminal(), "opencode mcp add --global leaflet --env LEAFLET_DATA_DIR=" + shellQuote(dir) + " -- " + shellQuote(exe) + " mcp"
	case "codex":
		return locales.ExternalAddTo("~/.codex/config.toml"), `[mcp_servers.leaflet]
command = ` + jsonString(exe) + `
args = ["mcp"]
env = { LEAFLET_DATA_DIR = ` + jsonString(dir) + ` }`
	case "cursor":
		return locales.ExternalAddTo("~/.cursor/mcp.json"), `{
  "mcpServers": {
    "leaflet": {
      ` + server + `
    }
  }
}`
	case "vscode":
		return locales.ExternalAddToVSCode(), `{
  "servers": {
    "leaflet": {
      "type": "stdio",
      ` + server + `
    }
  }
}`
	}
	return locales.ExternalAddToOther(), `{
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
