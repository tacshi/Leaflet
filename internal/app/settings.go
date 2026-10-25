package app

import (
	"archive/zip"
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
	{"providers", "sparkles", "Agent providers"},
	{"speech", "mic", "Speech to text"},
	{"offline", "cloud-download", "Offline and storage"},
}

func (a *App) settingsView(c *ui.Context) {
	pal := theme.Of(c)
	a.titleBar(c, func() {
		ui.Text(c, "Settings").FontSize(13).FontWeight(600).TextColor(pal.Text).Margin(0, 0, 0, 6)
		ui.Spacer(c)
	})
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("settings-nav")).Width(220).Padding(16, 10).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Children(func() {
			for _, s := range sections {
				a.navRow(c, "set:"+s.id, s.icon, s.label, "", a.route.Section == s.id, func() {
					a.route.Section = s.id
				})
			}
		})
		ui.Scroll(c.Key("settings:" + a.route.Section)).Grow(1).Children(func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(28, 32, 60).Children(func() {
				ui.Column(c).Width(min(a.contentWidth(c)+40, 640)).Gap(18).Children(func() {
					switch a.route.Section {
					case "providers":
						a.modelsSettings(c)
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
	})
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
	themeIdx   int
	size       float64
	devices    []map[string]any
	loaded     bool
	deleteOpen bool
	confirm    string
	busy       bool
}

var account accountState

func (a *App) accountSettings(c *ui.Context) {
	pal := theme.Of(c)
	s := &account
	if !s.nameLoaded {
		s.name, s.nameLoaded = a.session.DisplayName, true
		s.themeIdx = map[string]int{"system": 0, "light": 1, "dark": 2}[a.settings.Theme]
		s.size = a.settings.TextSize
	}
	heading(c, "Account", "Your name, how pages look, and the devices you use.")
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
		title, desc := "Sync", "This build keeps pages on this device only"
		switch {
		case a.session.SignedIn:
			title, desc = "Signed in", a.session.Email
		case a.backend.Configured():
			desc = "Sign in to sync pages between devices and share them"
		}
		settingRow(c, "email", title, desc, false, func() {
			switch {
			case a.session.SignedIn:
				if button(c, "signout", "log-out", "Sign out").Clicked() {
					a.signOut()
				}
			case a.backend.Configured():
				if primary(c, "signin", "", "Sign in").Clicked() {
					a.navigate(Route{Kind: "signin"})
				}
			}
		})
	})
	label(c, "Appearance")
	card(c, "appearance", func() {
		settingRow(c, "theme", "Theme", "", true, func() {
			if segmented(c, "theme", &s.themeIdx, "System", "Light", "Dark"); true {
				want := []string{"system", "light", "dark"}[s.themeIdx]
				if want != a.settings.Theme {
					a.settings.Theme = want
					a.saveSettings()
					a.applyTheme()
				}
			}
		})
		settingRow(c, "size", "Page text size", "", false, func() {
			sizes := []float64{15, 17, 19, 21}
			chosen := 1
			for k, v := range sizes {
				if v == a.settings.TextSize {
					chosen = k
				}
			}
			if segmented(c, "size", &chosen, "Small", "Default", "Large", "Larger"); sizes[chosen] != a.settings.TextSize {
				a.settings.TextSize = sizes[chosen]
				a.saveSettings()
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
							a.backend.Upsert(ctx, "devices", map[string]any{"id": id, "user_id": a.backend.UserID(), "revoked_at": time.Now().UTC().Format(time.RFC3339)}, "")
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
								a.signOut()
								a.st.Wipe()
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
	a.st.SetDisplayName(name)
	a.session = a.st.Session()
	if a.backend.SignedIn() {
		go func() {
			a.backend.UpdateUser(map[string]any{"display_name": name})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			a.backend.Upsert(ctx, "profiles", map[string]any{"id": a.backend.UserID(), "display_name": name}, "")
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

func (a *App) writeExport(path string) error {
	pages, err := a.st.Pages()
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	used := map[string]int{}
	for _, p := range pages {
		c := a.st.Content(p.ID)
		if c == nil {
			continue
		}
		name := safeName(p.DisplayTitle())
		if n := used[name]; n > 0 {
			name = fmt.Sprintf("%s (%d)", name, n+1)
		}
		used[safeName(p.DisplayTitle())]++
		w, err := z.Create(name + ".md")
		if err != nil {
			return err
		}
		w.Write([]byte(doc.PageMarkdown(p.DisplayTitle(), c, a.pageTitleOf)))
	}
	return z.Close()
}

func (a *App) signOut() {
	a.flush()
	a.backend.SignOut()
	a.st.LoadSession(nil)
	a.session = a.st.Session()
	account.loaded, account.devices = false, nil
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
			if p.SupportsTools != nil && !*p.SupportsTools {
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
			group := ""
			for _, p := range agents.Presets {
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
				if toggle(c, "offline", &v).Changed() {
					a.settings.LocalWhenOffline = v
					a.saveSettings()
				}
			})
			r := a.settings.AgentsReadOtherPages
			settingRow(c, "cross", "Let agents read your other pages", "They only ever edit the page they are asked on", false, func() {
				if toggle(c, "cross", &r).Changed() {
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
			if account && form.chatgpt == nil && form.id == "" {
				return // nothing to save before signing in
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if form.id != "" && danger(c, "remove", "Remove").Clicked() {
					a.removeProvider(form.id)
				}
				ui.Spacer(c)
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
		g, err := agents.SignInChatGPT(ctx, host, clientID, func(u string) { mygo.Shell.OpenExternal(u) })
		a.update(func() {
			form.signingIn = false
			if err != nil {
				form.result, form.resultOK = err.Error(), false
				return
			}
			a.st.SetValue("chatgpt.client", g.Account().ClientID)
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
			agents.SetLocalKey(p.ID, form.apiKey)
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
			json.Unmarshal(raw, &row)
			p := store.ProviderFromRemote(row)
			if p.SupportsTools == nil {
				p.SupportsTools = tools
			}
			if len(p.Models) == 0 {
				p.Models = models
			}
			a.st.PutProvider(p)
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
			a.backend.Invoke(ctx, "providers", map[string]any{"action": "delete", "id": id})
		}()
	}
	if p.AuthKind == "account" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			agents.ForgetChatGPT(ctx, id)
		}()
	}
	agents.SetLocalKey(id, "")
	a.st.DeleteProvider(id)
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
				desc := fmt.Sprintf("Kept “%s” over “%s”", short(kept.Delta().Plain(), 40), short(other.Delta().Plain(), 40))
				settingRow(c, cf.ID, a.pageTitleOf(cf.PageID), desc, i == 0, func() {
					b := button(c, "act", "", "Resolve")
					b.Menu(func(m *ui.Menu) {
						if m.Item("Use this device's version").Chosen() {
							a.resolveConflict(cf, cf.LocalNode)
						}
						if m.Item("Keep the newer version").Chosen() {
							a.st.ResolveConflict(cf.ID)
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
				os.RemoveAll(a.images.dir)
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
				go a.sync.SyncNow()
			}
		})
		wo := a.settings.WorkOffline
		settingRow(c, "work-offline", "Work offline", "Keep changes on this device until you turn this off", false, func() {
			if toggle(c, "wo", &wo).Changed() {
				a.settings.WorkOffline = wo
				a.saveSettings()
				if !wo {
					a.sync.Kick()
				}
			}
		})
		all := a.settings.AllPagesOffline
		settingRow(c, "all", "Keep all pages on this device", "Otherwise only pages you mark, and those you open", false, func() {
			if toggle(c, "all", &all).Changed() {
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

// resolveConflict puts a version of a block back.
func (a *App) resolveConflict(cf store.ConflictRow, node *doc.Node) {
	apply := func(content *doc.Content) {
		if content.Find(cf.BlockID) != nil {
			content.Replace(cf.BlockID, node.Clone())
		} else {
			content.Insert(doc.Position{At: "end"}, node.Clone())
		}
	}
	if ed := a.Editor(cf.PageID); ed != nil {
		ed.Edit(apply)
		a.savePage()
	} else if c := a.st.Content(cf.PageID); c != nil {
		a.st.AddHistory(cf.PageID, c, "conflict", "", "Before resolving a conflict")
		apply(c)
		a.st.SaveContent(cf.PageID, c)
	}
	a.st.ResolveConflict(cf.ID)
}
