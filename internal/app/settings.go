package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

var sections = []struct{ id, icon, label string }{
	{"account", "user", "Account"},
	{"appearance", "palette", "Appearance"},
	{"providers", "sparkles", "Agent providers"},
	{"external", "bot", "External agents"},
	{"speech", "mic", "Speech to text"},
	{"offline", "cloud-download", "Offline and storage"},
}

// settingsNav is MyGo's sidebar of the sections, the route's chosen.
func (a *App) settingsNav(c *ui.Context) ui.Element {
	pal := theme.Of(c)
	return ui.Sidebar(c.Key("settings-nav"), &a.settingsSection, func() {
		for _, s := range sections {
			ui.SidebarItem(c, s.id, theme.Icon(s.icon), s.label)
		}
	}).Width(settingsNavW).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Label("Settings")
}

// The settings dialog's room: its section list, its padding beside a
// section, and its size, kept as the window allows.
const settingsNavW, settingsPadX = 200, 28

func settingsSize(c *ui.Context) (w, h float32) {
	ww, wh := c.Size()
	return max(min(880, ww-64), 320), max(min(640, wh*0.86), 240)
}

// settingsDialog is Settings over the window: the sections' list beside
// the section chosen, closed by Escape, a click outside or its button.
func (a *App) settingsDialog(c *ui.Context) {
	pal := theme.Of(c)
	w, h := settingsSize(c)
	dialog(c, &a.settingsOpen, w, func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			// Focused as it opens, its section chosen shows in the accent
			// and the arrows move through the sections.
			a.settingsNav(c).FillHeight().AutoFocus()
			ui.Column(c).Grow(1).Shrink(1).Children(func() {
				ui.Row(c).Padding(14, 14, 10, settingsPadX).Gap(12).AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
					ui.Text(c, "Settings").FontSize(15).FontWeight(650).TextColor(pal.Text).Grow(1)
					tools(c, func() {
						if iconButton(c, "close", "x", "Close").Clicked() {
							a.settingsOpen = false
						}
					})
				})
				a.settingsBody(c)
			})
		})
	}).Height(h)
}

func (a *App) settingsBody(c *ui.Context) {
	ui.Scroll(c.Key("settings:" + a.settingsSection)).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(24, settingsPadX, 40).Children(func() {
			ui.Column(c).Width(a.settingsWidth(c)).Gap(18).Children(func() {
				switch a.settingsSection {
				case "appearance":
					a.appearanceSettings(c)
				case "providers":
					a.modelsSettings(c)
				case "external":
					a.externalSettings(c)
				case "speech":
					a.speechSettings(c)
				case "offline":
					a.offlineSettings(c)
				default:
					a.accountSettings(c)
				}
			})
		})
	})
}

// settingsWidth is the width of a settings section: up to 640, within
// the dialog beside the section list, less its padding.
func (a *App) settingsWidth(c *ui.Context) float32 {
	w, _ := settingsSize(c)
	return max(min(w-settingsNavW-1-2*settingsPadX, 640), 200)
}

func heading(c *ui.Context, title, desc string) {
	pal := theme.Of(c)
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, title).FontSize(22).FontWeight(700).TextColor(pal.Text)
		if desc != "" {
			ui.Text(c, desc).FontSize(13.5).TextColor(pal.TextMuted)
		}
	})
}

// Account ---------------------------------------------------------------

type accountState struct {
	name       string
	nameLoaded bool
	devices    []map[string]any
	loaded     bool
	deleteOpen bool
	confirm    string
	busy       bool
	// signingOut is set while waiting changes go up before signing out;
	// unsynced counts those that could not, to ask about.
	signingOut bool
	unsynced   int
}

var account accountState

// Appearance ------------------------------------------------------------

func (a *App) appearanceSettings(c *ui.Context) {
	heading(c, "Appearance", "How Leaflet and your pages look.")
	themeIdx := map[string]int{"system": 0, "light": 1, "sepia": 2, "dark": 3}[a.settings.Theme]
	card(c, "appearance", func() {
		settingRow(c, "theme", "Theme", "", true, func() {
			ui.Segmented(c.Key("theme"), &themeIdx, "System", "Light", "Sepia", "Dark")
			if want := []string{"system", "light", "sepia", "dark"}[themeIdx]; want != a.settings.Theme {
				a.settings.Theme = want
				a.saveSettings()
				a.applyTheme()
			}
		})
		settingRow(c, "accent", "Accent color", "", false, func() { a.accentDots(c) })
		settingRow(c, "size", "Page text size", "", false, func() {
			sizes := []float64{15, 17, 19, 21}
			chosen := 1
			for k, v := range sizes {
				if v == a.settings.TextSize {
					chosen = k
				}
			}
			if ui.Segmented(c.Key("size"), &chosen, "Small", "Default", "Large", "Larger"); sizes[chosen] != a.settings.TextSize {
				a.settings.TextSize = sizes[chosen]
				a.saveSettings()
			}
		})
	})
}

// accentDots is a row of dots choosing the accent: the desktop's, where
// it has one, then Leaflet's.
func (a *App) accentDots(c *ui.Context) {
	pal := theme.Of(c)
	isDark := c.Theme().Dark
	ui.Row(c.Key("accents")).Gap(8).Children(func() {
		dot := func(name, label string, color ui.Color) {
			d := ui.ButtonBase(c.Key(name)).Size(20, 20).Radius(999).Background(color).Label(label).Tooltip(label)
			chosen := a.settings.Accent == name || a.settings.Accent == "" && name == theme.Accents[0].Name
			if chosen {
				d.Border(2, pal.Text)
			} else if d.Hovered() {
				d.Border(2, pal.Border)
			}
			if d.Clicked() && !chosen {
				a.settings.Accent = name
				a.saveSettings()
				a.applyTheme()
			}
		}
		if system, ok := theme.Accent("system", isDark, c.Preferences().Accent); ok {
			dot("system", "System", system)
		}
		for _, acc := range theme.Accents {
			color := acc.Light
			if isDark {
				color = acc.Dark
			}
			dot(acc.Name, acc.Name, color)
		}
	})
}

func (a *App) accountSettings(c *ui.Context) {
	pal := theme.Of(c)
	s := &account
	if !s.nameLoaded {
		s.name, s.nameLoaded = a.session.DisplayName, true
	}
	heading(c, "Account", "Your name and the devices you use.")
	card(c, "profile", func() {
		settingRow(c, "name", "Name", "Shown on your comments and edits", true, func() {
			in := field(c, "name", &s.name, "Your name").Width(220)
			if in.Submitted() || (in.Changed() && false) {
				a.saveName(s.name)
			}
			if !in.Focused() && s.name != a.session.DisplayName && strings.TrimSpace(s.name) != "" {
				a.saveName(s.name)
			}
		})
		// A build without a server says so once, in Offline and storage.
		if !a.backend.Configured() {
			return
		}
		title, desc := "Sync", "Sign in to sync pages between devices and share them"
		if a.session.SignedIn {
			title, desc = "Signed in", a.session.Email
		}
		settingRow(c, "email", title, desc, false, func() {
			switch {
			case a.session.SignedIn:
				if button(c, "signout", "log-out", "Sign out").Disabled(s.signingOut).Clicked() {
					a.signOut()
				}
			case a.backend.Configured():
				if primary(c, "signin", "", "Sign in").Clicked() {
					a.navigate(Route{Kind: "signin"})
				}
			}
		})
	})
	if a.session.SignedIn {
		label(c, "Devices")
		if !s.loaded {
			s.loaded = true
			go a.loadDevices()
		}
		card(c, "devices", func() {
			if len(s.devices) == 0 {
				settingRow(c, "none", "Loading devices…", "", true, nil)
			}
			me := a.st.DeviceID()
			for i, d := range s.devices {
				id := store.S(d, "id")
				desc := "Last active " + humanTime(store.Time(d["last_active_at"]))
				if id == me {
					desc = "This device"
				}
				if d["revoked_at"] != nil {
					desc = "Signed out"
				}
				settingRow(c, "dev:"+id, store.S(d, "name"), desc, i == 0, func() {
					if id != me && d["revoked_at"] == nil && button(c, "revoke", "", "Sign out").Clicked() {
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							if _, err := a.backend.Upsert(ctx, "devices", map[string]any{"id": id, "user_id": a.backend.UserID(), "revoked_at": time.Now().UTC().Format(time.RFC3339)}, ""); err != nil {
								a.update(func() { a.toast("Could not sign out the device: " + err.Error()) })
								return
							}
							a.loadDevices()
						}()
					}
				})
			}
		})
	}
	label(c, "Your data")
	card(c, "data", func() {
		settingRow(c, "export", "Export all pages", "A zip of Markdown files, one per page", true, func() {
			if button(c, "export", "download", "Export…").Clicked() {
				a.exportAll()
			}
		})
		if a.session.SignedIn {
			settingRow(c, "delete", "Delete account", "Deletes your space, pages, recordings and keys on the server", false, func() {
				if danger(c, "delete", "Delete…").Clicked() {
					s.deleteOpen, s.confirm = true, ""
				}
			})
		}
	})
	if s.unsynced > 0 {
		open := true
		dialog(c, &open, 420, func() {
			dialogHeader(c, "Sign out anyway?", fmt.Sprintf("%d %s not uploaded yet. Signing out removes %s from this device.",
				s.unsynced, plural(s.unsynced, "change is", "changes are"), plural(s.unsynced, "it", "them")), &open)
			ui.Row(c).Padding(0, 20, 20).Justify(ui.End).Gap(8).Children(func() {
				if button(c, "cancel", "", "Cancel").Clicked() {
					open = false
				}
				if danger(c, "go", "Sign out").Clicked() {
					open = false
					a.leave(a.st.ForgetAccount)
				}
			})
		})
		if !open {
			s.unsynced = 0
		}
	}
	if s.deleteOpen {
		dialog(c, &s.deleteOpen, 420, func() {
			dialogHeader(c, "Delete your account?", "This cannot be undone.", &s.deleteOpen)
			ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
				ui.Text(c, "Type DELETE to confirm.").FontSize(13).TextColor(pal.TextMuted)
				field(c, "confirm", &s.confirm, "DELETE").AutoFocus()
				ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
					if button(c, "cancel", "", "Cancel").Clicked() {
						s.deleteOpen = false
					}
					if danger(c, "go", "Delete account").Clicked() && s.confirm == "DELETE" && !s.busy {
						s.busy = true
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
							defer cancel()
							_, err := a.backend.Invoke(ctx, "account", map[string]any{"action": "delete"})
							a.update(func() {
								s.busy = false
								if err != nil {
									a.toast("Could not delete the account: " + err.Error())
									return
								}
								s.deleteOpen = false
								a.leave(a.st.Wipe)
								a.toast("Your account was deleted")
							})
						}()
					}
				})
			})
		})
	}
}

func (a *App) saveName(name string) {
	name = strings.TrimSpace(name)
	if name == "" || name == a.session.DisplayName {
		return
	}
	if err := a.st.SetDisplayName(name); err != nil {
		account.name = a.session.DisplayName
		a.toast("Could not save: " + err.Error())
		return
	}
	a.session = a.st.Session()
	if a.backend.SignedIn() {
		go func() {
			err := a.backend.UpdateUser(map[string]any{"display_name": name})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, uerr := a.backend.Upsert(ctx, "profiles", map[string]any{"id": a.backend.UserID(), "display_name": name}, "")
			if err == nil {
				err = uerr
			}
			if err != nil {
				a.update(func() { a.toast("Could not save your name on the server: " + err.Error()) })
			}
		}()
	}
}

func (a *App) loadDevices() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := a.backend.Select(ctx, "devices", remoteQuery("last_active_at.desc"))
	if err != nil {
		return
	}
	a.update(func() { account.devices = rows })
}

func (a *App) exportAll() {
	a.flush()
	go func() {
		path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Export all pages", DefaultPath: "Leaflet pages.zip"})
		if path == "" {
			return
		}
		err := a.writeExport(path)
		a.update(func() {
			if err != nil {
				a.toast("Could not export: " + err.Error())
			} else {
				a.toast("Exported " + filepath.Base(path))
			}
		})
	}()
}

// signOut uploads the changes waiting, then signs out and removes the
// account's pages from this device. Changes that could not go up are
// asked about first.
func (a *App) signOut() {
	a.flush()
	account.signingOut = true
	go func() {
		_ = a.sync.SyncNow()
		n := a.st.PendingCount()
		a.update(func() {
			account.signingOut = false
			if n > 0 {
				account.unsynced = n
				return
			}
			a.leave(a.st.ForgetAccount)
		})
	}()
}

// leave signs out and clears this device with clear, between sync passes.
func (a *App) leave(clear func() error) {
	a.flush()
	go func() {
		var err error
		a.sync.Hold(func() {
			a.backend.SignOut()
			err = clear()
		})
		a.update(func() {
			if err != nil {
				a.toast("Could not remove your pages from this device: " + err.Error())
			}
			if _, err := a.st.LoadSession(nil); err != nil {
				a.toast(err.Error())
			}
			a.session = a.st.Session()
			account.loaded, account.devices, account.nameLoaded = false, nil, false
		})
	}()
}

// Agent providers -------------------------------------------------------

type providerForm struct {
	open      bool
	id        string
	preset    string
	name      string
	baseURL   string
	apiKey    string
	model     string
	models    []string
	custom    bool
	tools     *bool
	fetching  bool
	fetchedAt string
	testing   bool
	result    string
	resultOK  bool
	saving    bool
	errorText string
	// A ChatGPT sign-in, before it is saved.
	signingIn bool
	chatgpt   *agents.ChatGPT
	email     string
	keyTimer  *time.Timer
}

var form providerForm

func (a *App) modelsSettings(c *ui.Context) {
	pal := theme.Of(c)
	heading(c, "Agent providers", "The services agents run on. Cloud keys stay on the server; a model server on your machine works offline.")
	card(c, "providers", func() {
		if len(a.providers) == 0 {
			settingRow(c, "none", "No providers yet", "Sign in with ChatGPT, add a cloud service such as Claude with an API key, or a model server such as Ollama.", true, nil)
		}
		for i, p := range a.providers {
			where := "Cloud · key on the server"
			switch {
			case p.AuthKind == "account":
				where = "ChatGPT plan"
				if g, err := a.hub.ChatGPT(p.ID); err == nil && g.Account().Email != "" {
					where += " · " + g.Account().Email
				}
			case p.IsLocal:
				where = "On this device · " + p.BaseURL
			}
			// Agents always use tools on a ChatGPT plan (hub.go), whatever
			// an older test left in supports_tools.
			if p.SupportsTools != nil && !*p.SupportsTools && p.AuthKind != "account" {
				where += " · chat only"
			}
			pp := p
			row := ui.ButtonBase(c.Key("p:" + p.ID)).Justify(ui.Start).Label("Edit " + p.Name)
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
			}
			if row.Hovered() {
				row.Background(pal.Hover)
			}
			row.Children(func() {
				settingRow(c, "row", p.Name+" · "+p.DefaultModel, where, true, func() {
					ui.Icon(c, theme.Icon("chevron-right")).FontSize(14).TextColor(pal.TextMuted)
				})
			})
			if row.Clicked() {
				a.editProvider(&pp)
			}
		}
	})
	ui.Row(c).Gap(8).Children(func() {
		add := primary(c, "add", "plus", "Add a provider")
		add.Menu(func(m *ui.Menu) {
			// One ChatGPT sign-in is enough: its row edits it.
			signedIn := false
			for _, p := range a.providers {
				signedIn = signedIn || p.AuthKind == "account"
			}
			group := ""
			for _, p := range agents.Presets {
				if p.Auth == "account" && signedIn {
					continue
				}
				if p.Group != group {
					if group != "" {
						m.Separator()
					}
					group = p.Group
				}
				label := p.Name
				if p.Auth == "account" {
					label = "Sign in with " + p.Name + "…"
				}
				if m.Item(label).Chosen() {
					a.newProvider(p)
				}
			}
		})
	})
	if len(a.providers) > 0 {
		label(c, "Defaults")
		card(c, "defaults", func() {
			var choices []string
			names := map[string]string{}
			for _, o := range a.hub.Options() {
				for _, m := range agents.Models(o.Provider) {
					ch := store.ModelChoice{ProviderID: o.Provider.ID, Model: m}.Encode()
					choices = append(choices, ch)
					names[ch] = o.Name() + " · " + m
				}
			}
			cur := a.settings.DefaultModel
			if cur == "" && len(choices) > 0 {
				cur = choices[0]
			}
			settingRow(c, "default", "Default agent", "Used unless you pick another", true, func() {
				if selectBox(c, "default", &cur, choices, func(s string) string { return names[s] }, 240); cur != a.settings.DefaultModel {
					a.settings.DefaultModel = cur
					a.saveSettings()
				}
			})
			v := a.settings.LocalWhenOffline
			settingRow(c, "offline", "Use a provider on this machine when offline", "Agents keep working without a connection", false, func() {
				if ui.Switch(c.Key("offline"), &v).Changed() {
					a.settings.LocalWhenOffline = v
					a.saveSettings()
				}
			})
			r := a.settings.AgentsReadOtherPages
			settingRow(c, "cross", "Let agents read your other pages", "They only ever edit the page they are asked on", false, func() {
				if ui.Switch(c.Key("cross"), &r).Changed() {
					a.settings.AgentsReadOtherPages = r
					a.saveSettings()
				}
			})
		})
	}
	if form.open {
		a.providerDialog(c)
	}
}

func (a *App) newProvider(p agents.Preset) {
	form = providerForm{open: true, preset: p.ID, name: p.Name, baseURL: p.BaseURL, models: p.Suggested}
	if len(p.Suggested) > 0 {
		form.model = p.Suggested[0]
	}
	if p.Auth == "none" {
		// A server on this machine lists its models without a key.
		a.fetchModels()
	}
}

func (a *App) editProvider(p *store.Provider) {
	form = providerForm{open: true, id: p.ID, preset: p.Preset, name: p.Name, baseURL: p.BaseURL, model: p.DefaultModel,
		models: agents.Models(*p), tools: p.SupportsTools}
	if p.IsLocal && p.AuthKind != "account" {
		form.apiKey = agents.LocalKey(p.ID)
	}
	if p.AuthKind == "account" {
		if g, err := a.hub.ChatGPT(p.ID); err == nil {
			form.chatgpt, form.email = g, g.Account().Email
		}
	}
	a.fetchModels()
}

func (a *App) providerDialog(c *ui.Context) {
	pal := theme.Of(c)
	preset := agents.PresetByID(form.preset)
	account := preset.Auth == "account"
	local := preset.Local
	title := "Connect " + preset.Name
	if form.id != "" {
		title = "Edit " + form.name
	}
	desc := "Runs in Leaflet's server with your key, which never comes back to this device."
	switch {
	case account:
		desc = "Agents use your ChatGPT Plus or Pro plan. Sign-in stays on this device."
	case local:
		desc = "Runs on this device against your model server, so agents work offline."
	case !a.backend.Configured():
		desc = "Cloud providers need a Leaflet server, which this build doesn't have."
	case !a.session.SignedIn:
		desc = "Cloud providers run on the server: sign in first, or add a model server on this machine."
	}
	if preset.ID == "claude" {
		desc += " Claude plans only work in Claude's own apps, so this takes an API key."
	}
	dialog(c, &form.open, 500, func() {
		dialogHeader(c, title, desc, &form.open)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			fieldRow := func(name string, fn func()) {
				ui.Column(c).Gap(5).Children(func() {
					label(c, name)
					fn()
				})
			}
			if account {
				a.chatgptRow(c)
			} else {
				fieldRow("Name", func() { field(c, "name", &form.name, preset.Name) })
				fieldRow("Base URL", func() {
					ph := preset.Placeholder
					if ph == "" {
						ph = preset.BaseURL
					}
					field(c, "url", &form.baseURL, ph)
				})
			}
			testButton := func() {
				label := "Test"
				if form.testing {
					label = "Testing…"
				}
				if button(c, "test", "", label).MinWidth(82).Justify(ui.Center).Disabled(form.testing || (account && form.chatgpt == nil)).Clicked() {
					a.testProvider()
				}
			}
			if !account {
				fieldRow("API key", func() {
					ph := "Paste a key"
					if form.id != "" && !local {
						ph = "Saved on the server · paste a new key to replace it"
					}
					if local {
						ph = "Optional"
					}
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						var in ui.Element
						ui.Box(c).Grow(1).Children(func() { in = password(c, "key", &form.apiKey, ph) })
						if in.Changed() {
							a.fetchModelsSoon()
						}
						testButton()
					})
				})
			}
			if form.result != "" {
				kind := "danger"
				if form.resultOK {
					kind = "ok"
				}
				notice(c, kind, map[bool]string{true: "check", false: "circle-alert"}[form.resultOK], form.result, nil)
			}
			if !account || form.chatgpt != nil {
				fieldRow("Model", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						a.modelPicker(c)
						if account {
							testButton()
						}
					})
				})
			}
			if form.errorText != "" {
				ui.Text(c, form.errorText).FontSize(12.5).TextColor(pal.DangerText)
			}
			// Nothing to save before signing in: an existing sign-in can
			// only be removed.
			signedOut := account && form.chatgpt == nil
			if signedOut && form.id == "" {
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if form.id != "" && danger(c, "remove", "Remove").Clicked() {
					a.removeProvider(form.id)
				}
				ui.Spacer(c)
				if signedOut {
					return
				}
				cannot := form.saving || (!local && !a.session.SignedIn) || (account && form.chatgpt == nil) || strings.TrimSpace(form.model) == ""
				if primary(c, "save", "", "Save").Disabled(cannot).Clicked() {
					a.saveProvider()
				}
			})
		})
	})
}

// chatgptRow signs in with ChatGPT, or shows who is signed in.
func (a *App) chatgptRow(c *ui.Context) {
	pal := theme.Of(c)
	if form.chatgpt != nil {
		ui.Row(c).Padding(10, 12).Gap(10).Radius(8).Background(pal.OkBg).Border(1, pal.OkBorder).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, theme.Icon("check")).FontSize(15).TextColor(pal.OkText)
			who := form.email
			if who == "" {
				who = "your ChatGPT account"
			}
			ui.Text(c, "Signed in as "+who).FontSize(13.5).TextColor(pal.OkText).Grow(1)
			if ghost(c, "again", "", "Switch account").Clicked() {
				a.chatgptSignIn()
			}
		})
		return
	}
	label := "Sign in with ChatGPT"
	if form.signingIn {
		label = "Waiting for the browser…"
	}
	ui.Column(c).Gap(8).Children(func() {
		if primary(c, "signin", "user", label).Height(36).Disabled(form.signingIn).Clicked() {
			a.chatgptSignIn()
		}
		ui.Text(c, "Your browser opens to sign in. Leaflet never sees your password, and you can set a usage limit for it in ChatGPT's settings.").
			FontSize(12).TextColor(pal.TextMuted)
	})
}

func (a *App) chatgptSignIn() {
	form.signingIn, form.result = true, ""
	host := a.hub.HostID()
	clientID := ""
	if form.chatgpt != nil {
		clientID = form.chatgpt.Account().ClientID
	} else if id, ok := a.st.Value("chatgpt.client"); ok {
		clientID = id
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		g, err := agents.SignInChatGPT(ctx, host, clientID, func(u string) { _ = mygo.Shell.OpenExternal(u) })
		a.update(func() {
			form.signingIn = false
			if err != nil {
				form.result, form.resultOK = err.Error(), false
				return
			}
			_ = a.st.SetValue("chatgpt.client", g.Account().ClientID)
			form.chatgpt, form.email = g, g.Account().Email
			form.name = "ChatGPT"
			a.fetchModels()
		})
	}()
}

// modelPicker chooses a model from the provider's list, or takes any ID.
func (a *App) modelPicker(c *ui.Context) {
	const other = "\x00other"
	ui.Box(c).Grow(1).Children(func() {
		if form.custom || len(form.models) == 0 {
			ph := "Model ID"
			if form.fetching {
				ph = "Fetching models…"
			}
			field(c, "model-id", &form.model, ph)
			return
		}
		options := append([]string{}, form.models...)
		if form.model != "" && !contains(options, form.model) {
			options = append([]string{form.model}, options...)
		}
		options = append(options, other)
		choice := form.model
		selectBox(c, "model", &choice, options, func(s string) string {
			if s == other {
				return "Other model…"
			}
			if s == "" {
				return "Choose a model"
			}
			return s
		}, 0).FillWidth()
		if choice == other {
			form.custom, form.model = true, ""
		} else {
			form.model = choice
		}
	})
	if !form.custom && len(form.models) == 0 && !form.fetching {
		return
	}
	if form.custom && len(form.models) > 0 && ghost(c, "list", "", "From the list").Clicked() {
		form.custom = false
		if len(form.models) > 0 {
			form.model = form.models[0]
		}
	}
}

// fetchModelsSoon lists the models once a pasted key settles.
func (a *App) fetchModelsSoon() {
	if form.keyTimer != nil {
		form.keyTimer.Stop()
	}
	form.keyTimer = time.AfterFunc(700*time.Millisecond, func() { a.update(a.fetchModels) })
}

// fetchModels lists the provider's models for the dropdown: from its
// server with the key typed here, from Leaflet's server for a saved cloud
// provider, or from the ChatGPT plan.
func (a *App) fetchModels() {
	preset := agents.PresetByID(form.preset)
	base, key, id, g := form.baseURL, strings.TrimSpace(form.apiKey), form.id, form.chatgpt
	switch {
	case preset.Auth == "account" && g == nil:
		return
	case preset.Auth == "key" && key == "" && (id == "" || !a.backend.SignedIn()):
		return
	}
	at := base + "|" + key
	form.fetching, form.fetchedAt = true, at
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var models []string
		var err error
		switch {
		case g != nil:
			models, err = g.ListModels(ctx)
		case key != "" || preset.Local:
			models, err = agents.NewClient(base, key).ListModels(ctx)
		default:
			var res map[string]any
			res, err = a.backend.Invoke(ctx, "providers", map[string]any{"action": "models", "provider_id": id})
			for _, m := range listAny(res["models"]) {
				models = append(models, fmt.Sprint(m))
			}
		}
		a.update(func() {
			if form.fetchedAt != at {
				return // the key or address changed since
			}
			form.fetching = false
			if err != nil {
				if !preset.Local || form.id != "" {
					form.result, form.resultOK = "Could not list models: "+err.Error(), false
				}
				return
			}
			models = agents.ChatModels(models, preset.Suggested)
			if len(models) == 0 {
				return
			}
			form.models = models
			if form.model == "" || !contains(models, form.model) && !form.custom {
				form.model = models[0]
			}
			if form.result != "" && !form.resultOK {
				form.result = ""
			}
		})
	}()
}

func listAny(v any) []any {
	l, _ := v.([]any)
	return l
}

func (a *App) testProvider() {
	preset := agents.PresetByID(form.preset)
	base, key, id, model, g := form.baseURL, strings.TrimSpace(form.apiKey), form.id, form.model, form.chatgpt
	form.testing, form.result = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var res agents.TestResult
		switch {
		case g != nil:
			if model == "" {
				res = agents.TestResult{Message: "Choose a model to test with"}
			} else {
				res = agents.TestModel(ctx, g, model, nil)
			}
		case preset.Local || key != "":
			// A key typed here is tried from this device, before it goes
			// to the server.
			res = agents.NewClient(base, key).Test(ctx, model)
		case id != "":
			r, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "test", "provider_id": id, "model": model})
			if err != nil {
				res = agents.TestResult{Message: err.Error()}
			} else {
				res.OK, _ = r["ok"].(bool)
				res.SupportsTools, _ = r["supports_tools"].(bool)
				res.Message, _ = r["message"].(string)
			}
		default:
			res = agents.TestResult{Message: "Paste a key to test"}
		}
		a.update(func() {
			form.testing = false
			form.result, form.resultOK = res.Message, res.OK
			if res.OK {
				t := res.SupportsTools
				form.tools = &t
				if ms := agents.ChatModels(res.Models, preset.Suggested); len(ms) > 0 {
					form.models = ms
				}
			}
		})
	}()
}

func (a *App) saveProvider() {
	preset := agents.PresetByID(form.preset)
	if preset.Auth != "account" && strings.TrimSpace(form.baseURL) == "" || strings.TrimSpace(form.model) == "" {
		form.errorText = "A base URL and a model are needed."
		return
	}
	if preset.Local {
		p := store.Provider{ID: form.id, Preset: form.preset, Name: strings.TrimSpace(form.name), BaseURL: agents.NormalizeBaseURL(form.baseURL),
			AuthKind: "none", DefaultModel: form.model, IsLocal: true, SupportsTools: form.tools, Models: form.models}
		if p.ID == "" {
			p.ID = doc.NewRowID()
		}
		if p.Name == "" {
			p.Name = preset.Name
		}
		if form.apiKey != "" {
			p.AuthKind = "api_key"
		}
		if preset.Auth == "account" {
			p.AuthKind, p.BaseURL = "account", preset.BaseURL
			if err := agents.UseChatGPT(p.ID, form.chatgpt); err != nil {
				form.errorText = "Could not keep the sign-in: " + err.Error()
				return
			}
		}
		if old, err := a.st.Provider(p.ID); err == nil {
			p.CreatedAt = old.CreatedAt
		}
		if err := a.st.PutProvider(p); err != nil {
			form.errorText = err.Error()
			return
		}
		if preset.Auth != "account" {
			if err := agents.SetLocalKey(p.ID, form.apiKey); err != nil {
				form.errorText = "Could not save the key: " + err.Error()
				return
			}
		}
		if a.settings.DefaultModel == "" {
			a.settings.DefaultModel = store.ModelChoice{ProviderID: p.ID, Model: p.DefaultModel}.Encode()
			a.saveSettings()
		}
		form.open = false
		return
	}
	body := map[string]any{"preset": form.preset, "name": strings.TrimSpace(form.name), "base_url": agents.NormalizeBaseURL(form.baseURL),
		"auth_kind": "api_key", "default_model": form.model}
	if form.id != "" {
		body["id"] = form.id
	}
	if form.apiKey != "" {
		body["api_key"] = form.apiKey
	}
	form.saving = true
	tools := form.tools
	models := form.models
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "save", "provider": body})
		a.update(func() {
			form.saving = false
			if err != nil {
				form.errorText = err.Error()
				return
			}
			raw, _ := json.Marshal(res["provider"])
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				form.errorText = err.Error()
				return
			}
			p := store.ProviderFromRemote(row)
			if tools != nil {
				// What the dialog knows, from the stored value or a test
				// run in it, over what the server last heard.
				p.SupportsTools = tools
			}
			if len(p.Models) == 0 {
				p.Models = models
			}
			if err := a.st.PutProvider(p); err != nil {
				form.errorText = err.Error()
				return
			}
			if a.settings.DefaultModel == "" {
				a.settings.DefaultModel = store.ModelChoice{ProviderID: p.ID, Model: p.DefaultModel}.Encode()
				a.saveSettings()
			}
			form.open = false
		})
	}()
}

func (a *App) removeProvider(id string) {
	p, err := a.st.Provider(id)
	if err != nil {
		return
	}
	if !p.IsLocal && a.backend.SignedIn() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := a.backend.Invoke(ctx, "providers", map[string]any{"action": "delete", "id": id}); err != nil {
				a.update(func() { a.toast("Could not delete: " + err.Error()) })
			}
		}()
	}
	if p.AuthKind == "account" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			agents.ForgetChatGPT(ctx, id)
		}()
	}
	if err := agents.SetLocalKey(id, ""); err != nil {
		a.toast("Could not delete the key: " + err.Error())
	}
	if err := a.st.DeleteProvider(id); err != nil {
		a.toast("Could not delete: " + err.Error())
		return
	}
	if ch, ok := a.settings.Default(); ok && ch.ProviderID == id {
		a.settings.DefaultModel = ""
		a.saveSettings()
	}
	form.open = false
}

// Offline and storage ---------------------------------------------------

type offlineState struct {
	queue  []store.OutboxEntry
	loaded time.Time
}

var offline offlineState

func (a *App) offlineSettings(c *ui.Context) {
	heading(c, "Offline and storage", "Everything is saved on this device first, and goes to the server when you are online.")
	if a.backend.Configured() {
		a.syncSettings(c)
	} else {
		card(c, "status", func() {
			settingRow(c, "local", "Pages are kept on this device", "This build has no server to sync with", true, nil)
		})
	}
	if len(a.conflicts) > 0 {
		label(c, "Conflicts")
		card(c, "conflicts", func() {
			for i, cf := range a.conflicts {
				cf := cf
				kept, other := cf.LocalNode, cf.RemoteNode
				if cf.Kept == "remote" {
					kept, other = cf.RemoteNode, cf.LocalNode
				}
				// A block one side deleted and the other edited was kept,
				// and can be deleted again.
				deleted := other == nil
				desc := ""
				switch {
				case cf.LocalNode == nil:
					desc = fmt.Sprintf("Kept “%s”: deleted on this device, edited on another", short(kept.Delta().Plain(), 40))
				case cf.RemoteNode == nil:
					desc = fmt.Sprintf("Kept “%s”: deleted on another device, edited on this one", short(kept.Delta().Plain(), 40))
				default:
					desc = fmt.Sprintf("Kept “%s” over “%s”", short(kept.Delta().Plain(), 40), short(other.Delta().Plain(), 40))
				}
				settingRow(c, cf.ID, a.pageTitleOf(cf.PageID), desc, i == 0, func() {
					ui.MenuButton(c.Key("act"), "Resolve", func(m *ui.Menu) {
						if deleted {
							if m.Item("Delete it").Chosen() {
								a.resolveConflict(cf, nil)
							}
							if m.Item("Keep it").Chosen() {
								if err := a.st.ResolveConflict(cf.ID); err != nil {
									a.toast("Could not resolve: " + err.Error())
								}
							}
						}
						if !deleted && m.Item("Use this device's version").Chosen() {
							a.resolveConflict(cf, cf.LocalNode)
						}
						if !deleted && m.Item("Keep the newer version").Chosen() {
							if err := a.st.ResolveConflict(cf.ID); err != nil {
								a.toast("Could not resolve: " + err.Error())
							}
						}
						if m.Item("Open page").Chosen() {
							a.openPage(cf.PageID)
						}
					})
				})
			}
		})
	}
	label(c, "Storage")
	card(c, "storage", func() {
		settingRow(c, "where", "Data folder", "Pages, recordings and files on this device", true, func() {
			if button(c, "reveal", "folder-open", "Show").Clicked() {
				mygo.Shell.ShowItemInFolder(a.st.Dir)
			}
		})
		settingRow(c, "images", "Image cache", "Pictures of image blocks loaded from the web", false, func() {
			if button(c, "clear", "trash", "Clear").Clicked() {
				a.images = newImageCache(a.images.dir)
				_ = os.RemoveAll(a.images.dir)
				c.Toast("Cleared the image cache")
			}
		})
	})
}

// syncSettings are the parts of Offline and storage about the server.
func (a *App) syncSettings(c *ui.Context) {
	if time.Since(offline.loaded) > 2*time.Second {
		offline.queue, _ = a.st.Pending()
		offline.loaded = time.Now()
		c.After(2 * time.Second)
	}
	card(c, "status", func() {
		if !a.session.SignedIn {
			settingRow(c, "sync", "Not syncing", "Sign in to sync pages between devices", true, func() {
				if primary(c, "signin", "", "Sign in").Clicked() {
					a.navigate(Route{Kind: "signin"})
				}
			})
			return
		}
		status := "Up to date"
		switch {
		case a.settings.WorkOffline:
			status = "Working offline by choice"
		case !a.online:
			status = "No connection"
		case a.syncing:
			status = "Syncing…"
		case a.syncError != "":
			status = "Last sync failed: " + a.syncError
		}
		title := "Synced"
		if n := len(offline.queue); n > 0 {
			title = fmt.Sprintf("%d %s waiting", n, plural(n, "change", "changes"))
		}
		settingRow(c, "sync", title, status, true, func() {
			if button(c, "sync-now", "refresh-cw", "Sync now").Disabled(a.syncing).Clicked() {
				go func() { _ = a.sync.SyncNow() }()
			}
		})
		wo := a.settings.WorkOffline
		settingRow(c, "work-offline", "Work offline", "Keep changes on this device until you turn this off", false, func() {
			if ui.Switch(c.Key("wo"), &wo).Changed() {
				a.settings.WorkOffline = wo
				a.saveSettings()
				if !wo {
					a.sync.Kick()
				}
			}
		})
		all := a.settings.AllPagesOffline
		settingRow(c, "all", "Keep all pages on this device", "Otherwise only pages you mark, and those you open", false, func() {
			if ui.Switch(c.Key("all"), &all).Changed() {
				a.settings.AllPagesOffline = all
				a.saveSettings()
			}
		})
		keep := a.settings.KeepAudioOnDevice
		settingRow(c, "audio", "Keep recordings' audio on this device", "", false, func() {
			if selectBox(c, "audio", &keep, []string{"until_uploaded", "30_days", "always"}, func(s string) string {
				return map[string]string{"until_uploaded": "Until uploaded", "30_days": "For 30 days", "always": "Always"}[s]
			}, 170); keep != a.settings.KeepAudioOnDevice {
				a.settings.KeepAudioOnDevice = keep
				a.saveSettings()
			}
		})
	})
}

// resolveConflict puts a version of a block back, or deletes the block
// for a nil node.
func (a *App) resolveConflict(cf store.ConflictRow, node *doc.Node) {
	apply := func(content *doc.Content) {
		if node == nil {
			content.Remove(cf.BlockID)
			if len(content.Children) == 0 {
				content.Children = doc.Empty().Children
			}
			return
		}
		if content.Find(cf.BlockID) != nil {
			_ = content.Replace(cf.BlockID, node.Clone())
		} else {
			_ = content.Insert(doc.Position{At: "end"}, node.Clone())
		}
	}
	if ed := a.Editor(cf.PageID); ed != nil {
		ed.Edit(apply)
		a.savePage()
	} else if c := a.st.Content(cf.PageID); c != nil {
		if err := a.st.AddHistory(cf.PageID, c, "conflict", "", "Before resolving a conflict"); err != nil {
			a.toast("Could not resolve: " + err.Error())
			return
		}
		apply(c)
		if _, _, err := a.st.SaveContent(cf.PageID, c); err != nil {
			a.toast("Could not resolve: " + err.Error())
			return
		}
	}
	if err := a.st.ResolveConflict(cf.ID); err != nil {
		a.toast("Could not resolve: " + err.Error())
	}
}
