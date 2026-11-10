package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/mcp"
	"leaflet/internal/theme"
	"leaflet/locales"
)

var externalClients = []struct{ id, label string }{
	{"codex", "Codex"}, {"claude", "Claude Code"}, {"opencode", "OpenCode"}, {"cursor", "Cursor"}, {"vscode", "VS Code"}, {"other", "Other"},
}

type externalState struct {
	agents        []agentStatus
	checked       time.Time
	checking      bool
	busy, waiting string
	waitUntil     atomic.Int64
	disconnecting bool
	byHand        bool
	client        int
	command       commandUI
}

func (a *App) externalSettings(c *ui.Context) {
	pal := theme.Of(c)
	heading(c, locales.SettingsSectionExternal(), locales.ExternalDesc())
	if !a.external.checking && time.Since(a.external.checked) > 3*time.Second {
		a.checkAgents()
	}
	exe, dir := executable(), a.st.Dir
	moved := translocated(exe)
	if moved {
		notice(c, "warn", "triangle-alert", locales.ExternalMoveFirst(), nil)
	}
	var found []agentStatus
	for _, s := range a.external.agents {
		if s.installed {
			found = append(found, s)
		}
	}
	switch {
	case a.external.checked.IsZero():
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
	ui.Collapsible(c.Key("by-hand"), locales.ExternalByHand(), &a.external.byHand, func() {
		a.setupByHand(c, exe, dir)
	})
	a.commandSettings(c, exe, moved)
}

func (a *App) agentRow(c *ui.Context, s agentStatus, exe, dir string, moved, first bool) {
	pal := theme.Of(c)
	id := s.app.id
	icon, text, color := "", locales.ExternalNotConnected(), pal.TextMuted
	switch {
	case s.entry == nil && a.external.waiting == id:
		icon, text = "clock", locales.ExternalFinishIn(s.app.label)
	case s.entry == nil:
	case !s.current(exe, dir):
		icon, text, color = "circle-alert", locales.ExternalNotSetUp(), pal.WarnText
	case !s.seen.IsZero():
		icon, text, color = "check", locales.ExternalLastUsed(humanTime(s.seen)), pal.OkText
	case id == "chatgpt":
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
		case a.external.busy == id:
			label := locales.ExternalConnecting()
			if a.external.disconnecting {
				label = locales.ExternalDisconnecting()
			}
			ui.Button(c.Key("busy"), "").Label(label).Disabled(true).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13)
					ui.Text(c, label).SingleLine()
				})
			})
		case s.entry != nil && s.current(exe, dir):
			if button(c, "disconnect", "", locales.ExternalDisconnect()).Disabled(a.external.busy != "").Clicked() {
				a.disconnectAgent(s)
			}
		default:
			label := locales.ExternalConnect()
			if s.entry != nil {
				label = locales.ExternalUpdate()
			}
			if primary(c, "connect", "", label).Disabled(moved || a.external.busy != "").Clicked() {
				a.connectAgent(s, exe, dir)
			}
		}
	})
}

func (a *App) setupByHand(c *ui.Context, exe, dir string) {
	pal := theme.Of(c)
	where, snippet := agentSetup(externalClients[a.external.client].id, exe, dir)
	card(c, "connect", func() {
		ui.Row(c).Padding(12, 16).FillWidth().Children(func() {
			labels := make([]string, len(externalClients))
			for i, cl := range externalClients {
				labels[i] = cl.label
				if cl.id == "other" {
					labels[i] = locales.ExternalClientOther()
				}
			}
			ui.Segmented(c.Key("client"), &a.external.client, labels...)
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

func (a *App) checkAgents() {
	a.external.checking = true
	go func() {
		found := a.findAgents()
		a.update(func() { a.foundAgents(found) })
	}()
}

func (a *App) findAgents() []agentStatus {
	return detectAgents(currentAgentEnv(), a.st.ValuesWithPrefix(mcp.SeenPrefix))
}

func (a *App) foundAgents(found []agentStatus) {
	a.external.agents, a.external.checked, a.external.checking = found, time.Now(), false
	for _, s := range found {
		if s.app.id == a.external.waiting && s.entry != nil {
			a.external.waiting = ""
		}
	}
}

func (a *App) connectAgent(s agentStatus, exe, dir string) {
	if link := installLink(s.app.id, exe, dir); link != "" {
		again := a.external.waiting == s.app.id
		a.external.waiting = s.app.id
		a.external.waitUntil.Store(time.Now().Add(agentWait).UnixNano())
		if err := openInstallLink(link); err != nil {
			a.external.waiting = ""
			a.toast(locales.ExternalConnectFailed(s.app.label, err.Error()))
			return
		}
		if !again {
			go a.awaitAgent(s.app.id)
		}
		return
	}
	a.external.disconnecting = false
	a.changeAgent(s, func() error { return connectAgent(s.app.settingsID(), s.cli, currentAgentEnv(), exe, dir) }, locales.ExternalConnectFailed)
}

func (a *App) disconnectAgent(s agentStatus) {
	a.external.disconnecting = true
	a.changeAgent(s, func() error { return disconnectAgent(s.app.settingsID(), s.cli, currentAgentEnv()) }, locales.ExternalDisconnectFailed)
}

func (a *App) changeAgent(s agentStatus, fn func() error, failed func(app, err string) string) {
	a.external.busy = s.app.id
	go func() {
		err := fn()
		found := a.findAgents()
		a.update(func() {
			a.external.busy = ""
			a.foundAgents(found)
			if err != nil {
				a.toast(failed(s.app.label, err.Error()))
			}
		})
	}()
}

var openInstallLink = func(link string) error { return mygo.Shell.OpenExternal(link) }

const agentWait = 3 * time.Minute

func (a *App) awaitAgent(id string) {
	env := currentAgentEnv()
	for time.Now().UnixNano() < a.external.waitUntil.Load() {
		time.Sleep(2 * time.Second)
		if e, _ := readEntry(id, env); e != nil {
			a.update(a.checkAgents)
			return
		}
	}
	a.update(func() {
		switch {
		case a.external.waiting != id:
		case time.Now().UnixNano() < a.external.waitUntil.Load():
			go a.awaitAgent(id)
		default:
			a.external.waiting = ""
		}
	})
}

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

func agentSetup(client, exe, dir string) (where, snippet string) {
	server := `"command": ` + jsonString(exe) + `,
      "args": ["mcp"],
      "env": { "LEAFLET_DATA_DIR": ` + jsonString(dir) + ` }`
	switch client {
	case "claude":
		return locales.ExternalRunInTerminal(), "claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=" + shellWord(dir) + " -- " + shellWord(exe) + " mcp"
	case "opencode":
		return locales.ExternalRunInTerminal(), "opencode mcp add --global leaflet --env LEAFLET_DATA_DIR=" + shellWord(dir) + " -- " + shellWord(exe) + " mcp"
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

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func shellWord(s string) string {
	if runtime.GOOS == "windows" {
		return windowsQuote(s)
	}
	return shellQuote(s)
}

func windowsQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"$`&|<>()[]{}^;,=!%@#~+\t") {
		return s
	}
	return `"` + s + `"`
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"\\$`!*?;&|<>()[]{}#~\t\n") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
