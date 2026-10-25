package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

type inlinePrompt struct {
	blockID   string
	quote     string
	selection bool
	agent     store.ModelChoice
	agentName string
	text      string
	runID     string
	reply     string
	working   bool
}

type newComment struct {
	blockID string
	quote   string
	text    string
}

// dialogs is the state of the app's dialogs and transient panels.
type dialogs struct {
	searchOpen bool
	query      string
	hits       []store.SearchHit
	hitIndex   int

	folderOpen   bool
	folder       *store.Folder
	folderName   string
	folderSelect bool // select the name as the field takes the focus

	moveOpen  bool
	movePage  string
	moveQuery string

	historyOpen bool
	historyPage string
	history     []store.HistoryEntry
	historyPick int

	shareOpen   bool
	sharePage   string
	shareEmail  string
	shareRole   string
	shareBusy   bool
	shareError  string
	shareMember []store.Member

	chartOpen   bool
	chartID     string
	chartEditor *editor.Editor
	chartTitle  string
	chartKind   int
	chartCSV    string
	chartCap    string
	chartError  string

	embedOpen   bool
	embedID     string // the embed being edited, or "" for a new one
	embedTitle  string
	embedHTML   string
	embedHeight string

	inline       *inlinePrompt
	newComment   *newComment
	commentFocus string
	focusChat    bool
	hideRun      string
}

func (d *dialogs) view(c *ui.Context, a *App) {
	if d.searchOpen {
		a.searchDialog(c)
	}
	if d.folderOpen {
		a.folderDialog(c)
	}
	if d.moveOpen {
		a.moveDialog(c)
	}
	if d.historyOpen {
		a.historyDialog(c)
	}
	if d.shareOpen {
		a.shareDialog(c)
	}
	if d.chartOpen {
		a.chartDialog(c)
	}
	if d.embedOpen {
		a.embedDialog(c)
	}
}

// Search ----------------------------------------------------------------

func (a *App) openSearch() {
	a.flush()
	a.dialogs.searchOpen = true
	a.dialogs.query = ""
	a.dialogs.hitIndex = 0
	a.dialogs.hits, _ = a.st.Search("", 30)
}

func (a *App) searchDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	ui.DialogBase(c, &d.searchOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(10, 12, 16, 0.28)).Justify(ui.Start).PaddingY(90)
		panel.Width(600).Radius(12).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 24, 60, 0, pal.Shadow).Clip()
		ui.Row(c).Height(52).Padding(0, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
			ui.Icon(c, theme.Icon("search")).FontSize(17).TextColor(pal.TextMuted)
			in := ui.TextInputBase(c.Key("q"), &d.query).Placeholder("Search pages, recordings and comments").FontSize(16).Grow(1).AutoFocus()
			if in.Changed() {
				d.hits, _ = a.st.Search(d.query, 30)
				d.hitIndex = 0
			}
			if in.Shortcut(0, ui.KeyDown) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex + 1) % len(d.hits)
			}
			if in.Shortcut(0, ui.KeyUp) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex - 1 + len(d.hits)) % len(d.hits)
			}
			if in.Submitted() && d.hitIndex < len(d.hits) {
				a.openHit(d.hits[d.hitIndex])
			}
			ui.Text(c, "esc").FontSize(11).TextColor(pal.TextMuted).Padding(2, 6).Radius(4).Border(1, pal.Divider)
		})
		ui.Scroll(c).MaxHeight(420).Padding(6).Children(func() {
			if d.query == "" && len(d.hits) > 0 {
				label(c, "Recent")
			}
			for i, h := range d.hits {
				row := ui.ButtonBase(c.Key(fmt.Sprintf("hit:%d", i))).Padding(8, 10).Gap(12).Radius(8).AlignItems(ui.Center)
				if i == d.hitIndex {
					row.Background(pal.NavSelected)
					row.ScrollIntoView()
				} else if row.Hovered() {
					row.Background(pal.Hover)
				}
				row.Children(func() {
					icon := map[string]string{"page": "file-text", "recording": "mic", "comment": "message-square"}[h.Kind]
					ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.Icon)
					ui.Column(c).Grow(1).Children(func() {
						ui.Text(c, h.Title).FontSize(14).TextColor(pal.Text).SingleLine()
						if h.Snippet != "" {
							ui.RichText(c, matchSpans(h.Snippet, d.query, pal.Text)...).FontSize(12).TextColor(pal.TextMuted).SingleLine()
						}
					})
				})
				if row.Clicked() {
					a.openHit(h)
				}
			}
			if d.query != "" && len(d.hits) == 0 {
				ui.Column(c).Padding(24).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Nothing matches “"+d.query+"”").TextColor(pal.TextMuted)
					if a.page != nil && a.hub.Available() && button(c, "ask", "sparkles", "Ask an agent instead").Clicked() {
						q := d.query
						d.searchOpen = false
						a.panelOpen, a.panelTab = true, 0
						a.hub.Chat(a.page.id, q, nil)
					}
				})
			}
		})
	})
}

// matchSpans sets the first match of query in text apart, in color.
func matchSpans(text, query string, color ui.Color) []ui.Span {
	q := strings.ToLower(strings.TrimSpace(query))
	lower := strings.ToLower(text)
	i := strings.Index(lower, q)
	if q == "" || i < 0 || len(lower) != len(text) {
		return []ui.Span{{Text: text}}
	}
	return []ui.Span{{Text: text[:i]}, {Text: text[i : i+len(q)], Color: color, Weight: 600}, {Text: text[i+len(q):]}}
}

func (a *App) openHit(h store.SearchHit) {
	a.dialogs.searchOpen = false
	a.openPage(h.PageID)
	if h.BlockID != "" && a.page != nil {
		a.page.editor.PlaceCaret(h.BlockID, 0)
	}
	if h.Kind == "comment" {
		a.panelOpen, a.panelTab = true, 1
		a.dialogs.commentFocus = h.BlockID
	}
}

// Folders ---------------------------------------------------------------

func (d *dialogs) openRenameFolder(f *store.Folder) {
	d.folderOpen, d.folder = true, f
	d.folderName = ""
	if f != nil {
		d.folderName, d.folderSelect = f.Name, true
	}
}

func (a *App) folderDialog(c *ui.Context) {
	d := &a.dialogs
	title := "New folder"
	if d.folder != nil {
		title = "Rename folder"
	}
	dialog(c, &d.folderOpen, 380, func() {
		dialogHeader(c, title, "Folders group pages and files in your space.", &d.folderOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			in := field(c, "name", &d.folderName, "Folder name").AutoFocus()
			if d.folderSelect && in.Focused() {
				d.folderSelect = false
				in.SetTextSelection(0, utf8.RuneCountInString(d.folderName))
			}
			save := func() {
				name := strings.TrimSpace(d.folderName)
				if name == "" {
					return
				}
				if d.folder == nil {
					if f, err := a.st.CreateFolder(name); err == nil {
						a.folderOpen[f.ID] = true
					}
				} else {
					if err := a.st.RenameFolder(*d.folder, name); err != nil {
						a.toast("Could not rename: " + err.Error())
					}
				}
				d.folderOpen = false
			}
			if in.Submitted() {
				save()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", "Cancel").Clicked() {
					d.folderOpen = false
				}
				label := "Create"
				if d.folder != nil {
					label = "Rename"
				}
				if primary(c, "save", "", label).Clicked() {
					save()
				}
			})
		})
	})
}

// Move ------------------------------------------------------------------

func (d *dialogs) openMove(id string) {
	d.moveOpen, d.movePage, d.moveQuery = true, id, ""
}

func (a *App) moveDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.moveOpen, 440, func() {
		dialogHeader(c, "Move “"+a.pageTitleOf(d.movePage)+"”", "Choose where it goes.", &d.moveOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(10).Children(func() {
			field(c, "q", &d.moveQuery, "Search pages").AutoFocus()
			ui.Scroll(c).MaxHeight(320).Children(func() {
				target := func(key, icon, title, parent, folder string) {
					row := ui.ButtonBase(c.Key(key)).Padding(7, 10).Gap(10).Radius(7).AlignItems(ui.Center).Justify(ui.Start).FillWidth()
					if row.Hovered() {
						row.Background(pal.Hover)
					}
					row.Children(func() {
						ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.Icon)
						ui.Text(c, title).FontSize(13.5).TextColor(pal.Text).SingleLine()
					})
					if row.Clicked() {
						if err := a.st.MovePage(d.movePage, parent, folder, ""); err != nil {
							a.toast(err.Error())
						} else {
							d.moveOpen = false
							if parent != "" {
								a.expanded[parent] = true
							}
						}
					}
				}
				q := strings.ToLower(d.moveQuery)
				if q == "" {
					target("top", "file-text", "Top level of My space", "", "")
					for _, f := range a.folders {
						target("folder:"+f.ID, "folder", f.Name, "", f.ID)
					}
				}
				for _, p := range a.pages {
					if p.ID == d.movePage || a.st.IsDescendant(p.ID, d.movePage) || !a.canEdit(p.ID) {
						continue
					}
					if q != "" && !strings.Contains(strings.ToLower(p.DisplayTitle()), q) {
						continue
					}
					target("page:"+p.ID, "file-text", p.DisplayTitle(), p.ID, "")
				}
			})
		})
	})
}

// History ---------------------------------------------------------------

func (a *App) openHistory(id string) {
	if a.page != nil && a.page.id == id {
		a.savePage()
	}
	d := &a.dialogs
	d.historyOpen, d.historyPage, d.historyPick = true, id, 0
	d.history, _ = a.st.History(id)
}

func (a *App) historyDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.historyOpen, 760, func() {
		dialogHeader(c, "Page history", a.pageTitleOf(d.historyPage), &d.historyOpen)
		if len(d.history) == 0 {
			ui.Text(c, "No earlier versions yet. Leaflet keeps one every ten minutes of editing and before each agent run.").
				TextColor(pal.TextMuted).Padding(0, 20, 24)
			return
		}
		ui.Row(c).Height(440).AlignItems(ui.Stretch).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
			ui.Scroll(c).Width(250).Padding(6).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Children(func() {
				for i, h := range d.history {
					row := ui.ButtonBase(c.Key(h.ID)).Padding(8, 10).Radius(7).Justify(ui.Start)
					if i == d.historyPick {
						row.Background(pal.NavSelected)
					} else if row.Hovered() {
						row.Background(pal.Hover)
					}
					row.Children(func() {
						ui.Column(c).Gap(2).Children(func() {
							ui.Text(c, h.CreatedAt.Format("Jan 2, 15:04")).FontSize(13.5).FontWeight(550).TextColor(pal.Text)
							l := h.Label
							if l == "" {
								l = map[string]string{"agent_run": "Before an agent run", "autosave": "Autosave", "restore": "Before a restore", "conflict": "Conflict"}[h.Reason]
							}
							ui.Text(c, l).FontSize(12).TextColor(pal.TextMuted).MaxLines(2)
						})
					})
					if row.Clicked() {
						d.historyPick = i
					}
				}
			})
			ui.Column(c).Grow(1).Children(func() {
				h := d.history[min(d.historyPick, len(d.history)-1)]
				content := doc.MustDecode(h.Content)
				ui.Scroll(c).Grow(1).Padding(18, 24).Children(func() {
					markdown.View(c, doc.PageMarkdown("", content, a.pageTitleOf), &a.md).Font(theme.PageFont).FontSize(15)
				})
				ui.Row(c).Padding(12, 16).Justify(ui.End).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
					if button(c, "close", "", "Close").Clicked() {
						d.historyOpen = false
					}
					if primary(c, "restore", "history", "Restore this version").Disabled(!a.canEdit(d.historyPage)).Clicked() {
						a.restoreVersion(d.historyPage, content)
						d.historyOpen = false
					}
				})
			})
		})
	})
}

// restoreVersion puts an earlier version back, keeping the current one in
// the history first.
func (a *App) restoreVersion(pageID string, c *doc.Content) {
	if a.page != nil && a.page.id == pageID {
		a.savePage()
	}
	if cur := a.st.Content(pageID); cur != nil {
		if err := a.st.AddHistory(pageID, cur, "restore", "", "Before restoring "+time.Now().Format("Jan 2, 15:04")); err != nil {
			a.toast("Could not restore: " + err.Error())
			return
		}
	}
	if ed := a.Editor(pageID); ed != nil {
		ed.Edit(func(content *doc.Content) { content.Children = c.Copy().Children })
		a.savePage()
		// The last run's edits are gone or back with the version: undoing
		// it now would do nothing, or something else.
		if run := a.hub.Session(pageID).Latest(); run != nil {
			a.dialogs.hideRun = run.ID
		}
		if a.page != nil && a.page.id == pageID {
			clear(a.page.tinted)
			// A selection made on the other version means nothing here.
			ed.Select(ed.Caret(), ed.Caret())
		}
	} else if _, _, err := a.st.SaveContent(pageID, c); err != nil {
		a.toast("Could not restore: " + err.Error())
		return
	}
	a.toast("Restored the earlier version")
}

// Share -----------------------------------------------------------------

func (a *App) openShare(id string) {
	d := &a.dialogs
	d.shareOpen, d.sharePage, d.shareEmail, d.shareRole, d.shareError = true, id, "", "editor", ""
	d.shareMember, _ = a.st.Members(id)
	if a.backend.SignedIn() {
		go func() {
			a.loadMembers(id)
			a.update(func() { d.shareMember, _ = a.st.Members(id) })
		}()
	}
}

func (a *App) shareDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	p := a.pageByID(d.sharePage)
	dialog(c, &d.shareOpen, 480, func() {
		dialogHeader(c, "Share “"+a.pageTitleOf(d.sharePage)+"”", "People you add can open this page and its subpages.", &d.shareOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			switch {
			case !a.backend.Configured():
				notice(c, "info", "info", "This build runs on this device only. Sharing needs a Leaflet server.", nil)
				return
			case !a.session.SignedIn:
				notice(c, "info", "info", "Sign in to share pages with other people.", func() {
					if primary(c, "signin", "", "Sign in").Clicked() {
						d.shareOpen = false
						a.navigate(Route{Kind: "signin"})
					}
				})
				return
			case p != nil && a.isShared(*p):
				notice(c, "info", "info", "Only the owner of this space can change who it is shared with.", nil)
			default:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					in := growField(c, "email", &d.shareEmail, "Name or email")
					selectBox(c, "role", &d.shareRole, []string{"editor", "viewer"}, func(s string) string {
						return map[string]string{"editor": "Can edit", "viewer": "Can view"}[s]
					}, 116)
					invite := func() {
						email := strings.TrimSpace(d.shareEmail)
						if email == "" || d.shareBusy {
							return
						}
						d.shareBusy, d.shareError = true, ""
						page, role := d.sharePage, d.shareRole
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							_, err := a.backend.RPC(ctx, "share_page", map[string]any{"p_page_id": page, "p_email": email, "p_role": role})
							if err == nil {
								a.loadMembers(page)
							}
							a.update(func() {
								d.shareBusy = false
								if err != nil {
									d.shareError = err.Error()
									return
								}
								d.shareEmail = ""
								d.shareMember, _ = a.st.Members(page)
							})
						}()
					}
					if in.Submitted() {
						invite()
					}
					if primary(c, "invite", "", "Invite").Disabled(d.shareBusy).Clicked() {
						invite()
					}
				})
				if d.shareError != "" {
					ui.Text(c, d.shareError).FontSize(12.5).TextColor(pal.DangerText)
				}
			}
			label(c, "People with access")
			ui.Column(c).Gap(2).Children(func() {
				ui.Row(c).Padding(6, 0).Gap(10).AlignItems(ui.Center).Children(func() {
					avatar(c, a.session.Initial(), 28, false)
					ui.Text(c, a.session.DisplayName+" (you)").Grow(1).TextColor(pal.Text)
					ui.Text(c, "Owner").FontSize(12.5).TextColor(pal.TextMuted)
				})
				for _, m := range d.shareMember {
					if m.UserID == a.session.UserID {
						continue
					}
					ui.Row(c.Key(m.UserID)).Padding(6, 0).Gap(10).AlignItems(ui.Center).Children(func() {
						avatar(c, strings.ToUpper(firstRune(m.Name())), 28, false)
						ui.Column(c).Grow(1).Children(func() {
							ui.Text(c, m.Name()).TextColor(pal.Text)
							if m.Email != "" && m.DisplayName != "" {
								ui.Text(c, m.Email).FontSize(12).TextColor(pal.TextMuted)
							}
						})
						ui.Text(c, map[string]string{"editor": "Can edit", "viewer": "Can view", "owner": "Owner"}[m.Role]).FontSize(12.5).TextColor(pal.TextMuted)
						if p != nil && !a.isShared(*p) && m.Role != "owner" {
							if iconButton(c, "remove", "x", "Remove").Clicked() {
								page, uid := d.sharePage, m.UserID
								go func() {
									ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
									defer cancel()
									if _, err := a.backend.RPC(ctx, "unshare_page", map[string]any{"p_page_id": page, "p_user_id": uid}); err != nil {
										a.update(func() { d.shareError = err.Error() })
										return
									}
									a.loadMembers(page)
									a.update(func() { d.shareMember, _ = a.st.Members(page) })
								}()
							}
						}
					})
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				if button(c, "copy", "link", "Copy link").Clicked() {
					c.WriteClipboard("leaflet://page/" + d.sharePage)
					c.Toast("Link copied")
				}
			})
		})
	})
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return "?"
}

// Charts ----------------------------------------------------------------

var chartKinds = []string{"bar", "line", "pie"}

func (d *dialogs) openChart(id string, ed *editor.Editor) {
	n := ed.Content.Find(id)
	if n == nil {
		return
	}
	kind, title, caption, labels, series := editor.ChartOf(n)
	d.chartOpen, d.chartID, d.chartEditor, d.chartTitle, d.chartCap, d.chartError = true, id, ed, title, caption, ""
	d.chartKind = 0
	for i, k := range chartKinds {
		if k == kind {
			d.chartKind = i
		}
	}
	// One row per label: label, then a value per series.
	var b strings.Builder
	b.WriteString("label")
	for _, s := range series {
		b.WriteString(", " + s.Name)
	}
	for i, l := range labels {
		b.WriteString("\n" + l)
		for _, s := range series {
			v := 0.0
			if i < len(s.Values) {
				v = s.Values[i]
			}
			b.WriteString(", " + strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	d.chartCSV = b.String()
}

func parseChartCSV(csv string) (labels []any, series []any, err error) {
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if len(lines) < 2 {
		return nil, nil, fmt.Errorf("Add a header row and at least one row of values.") //nolint:staticcheck // shown to people as a sentence.
	}
	split := func(s string) []string {
		parts := strings.Split(s, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	header := split(lines[0])
	if len(header) < 2 {
		return nil, nil, fmt.Errorf("The header needs a label column and at least one series.") //nolint:staticcheck // shown to people as a sentence.
	}
	values := make([][]any, len(header)-1)
	for i, line := range lines[1:] {
		cells := split(line)
		labels = append(labels, cells[0])
		for s := range values {
			v := 0.0
			if s+1 < len(cells) && cells[s+1] != "" {
				if v, err = strconv.ParseFloat(cells[s+1], 64); err != nil {
					return nil, nil, fmt.Errorf("Row %d: %q is not a number.", i+2, cells[s+1]) //nolint:staticcheck // shown to people as a sentence.
				}
			}
			values[s] = append(values[s], v)
		}
	}
	for s, name := range header[1:] {
		series = append(series, map[string]any{"name": name, "values": values[s]})
	}
	return labels, series, nil
}

func (a *App) chartDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.chartOpen, 520, func() {
		dialogHeader(c, "Chart data", "One row per label. Columns after the first are series.", &d.chartOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.chartTitle, "Title")
			segmented(c, "kind", &d.chartKind, "Bar", "Line", "Pie")
			area := ui.TextArea(c.Key("csv"), &d.chartCSV).Height(180).Font("monospace").FontSize(13)
			_ = area
			field(c, "caption", &d.chartCap, "Caption (optional)")
			if d.chartError != "" {
				ui.Text(c, d.chartError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", "Cancel").Clicked() {
					d.chartOpen = false
				}
				if primary(c, "save", "", "Save chart").Clicked() {
					labels, series, err := parseChartCSV(d.chartCSV)
					if err == nil {
						var data map[string]any
						data, err = doc.ChartData(map[string]any{"chart_type": chartKinds[d.chartKind], "title": d.chartTitle,
							"labels": labels, "series": series, "caption": d.chartCap})
						if err == nil {
							id := d.chartID
							d.chartEditor.Edit(func(content *doc.Content) {
								if n := content.Find(id); n != nil {
									data["author"] = n.Data["author"]
									n.Data = data
								}
							})
							d.chartOpen = false
						}
					}
					if err != nil {
						d.chartError = err.Error()
					}
				}
			})
		})
	})
}

// Embeds ----------------------------------------------------------------

func (d *dialogs) openEmbed() {
	d.embedOpen, d.embedID, d.embedTitle, d.embedHeight = true, "", "Interactive embed", "320"
	d.embedHTML = "<!doctype html>\n<html><body style=\"font-family: system-ui; padding: 16px\">\n  <button onclick=\"this.textContent = Number(this.textContent) + 1\">0</button>\n</body></html>"
}

// editEmbed opens the dialog on an embed in the page, to change it.
func (d *dialogs) editEmbed(n *doc.Node) {
	d.embedOpen, d.embedID = true, n.ID
	d.embedTitle, d.embedHTML = n.Str("title"), n.Str("html")
	d.embedHeight = strconv.Itoa(n.Int("height", 320))
}

func (a *App) embedDialog(c *ui.Context) {
	d := &a.dialogs
	dialog(c, &d.embedOpen, 600, func() {
		dialogHeader(c, "Interactive embed", "HTML, CSS and JavaScript that run sandboxed, with no network or storage.", &d.embedOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.embedTitle, "Title")
			ui.TextArea(c.Key("html"), &d.embedHTML).Height(240).Font("monospace").FontSize(12.5)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Height").FontSize(13)
				field(c, "height", &d.embedHeight, "320").Width(80)
				ui.Spacer(c)
				if button(c, "cancel", "", "Cancel").Clicked() {
					d.embedOpen = false
				}
				label := "Insert"
				if d.embedID != "" {
					label = "Save"
				}
				if primary(c, "insert", "", label).Clicked() && a.page != nil {
					h, _ := strconv.Atoi(d.embedHeight)
					n, err := doc.SpecToNode(map[string]any{"type": "embed", "embed": map[string]any{"title": d.embedTitle, "html": d.embedHTML, "height": float64(h)}}, "", a.session.DisplayName)
					if err != nil {
						a.toast(err.Error())
						return
					}
					if id := d.embedID; id != "" {
						a.page.editor.Edit(func(content *doc.Content) {
							if old := content.Find(id); old != nil {
								n.Data["author"] = old.Data["author"]
								old.Data = n.Data
							}
						})
					} else {
						a.page.editor.InsertBlocks(n)
					}
					d.embedOpen = false
				}
			})
		})
	})
}

// Inline agent prompt ---------------------------------------------------

// inlinePrompt asks an agent to work on a block, from @ or a selection.
func (a *App) inlinePrompt(c *ui.Context) {
	pal := theme.Of(c)
	ip := a.dialogs.inline
	if ip == nil || a.page == nil {
		return
	}
	ps := a.page
	ui.Column(c.Key("inline-prompt")).Margin(14, 0, 0).Padding(12).Gap(10).Radius(10).Background(pal.AgentSection).
		Border(1, pal.AccentSoftBorder).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			avatar(c, "", 22, true)
			name := ip.agentName
			if name == "" {
				if o, err := a.hub.Resolve(nil); err == nil {
					name = o.Name()
				} else {
					name = "Agent"
				}
			}
			title := "Ask " + name
			if ip.quote != "" {
				title += " about “" + short(ip.quote, 48) + "”"
			}
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(pal.AccentText).Grow(1).SingleLine()
			if iconButton(c, "close", "x", "Close").Clicked() {
				a.dialogs.inline = nil
				ps.editor.Focus()
			}
		})
		if ip.reply != "" || ip.working {
			if ip.working {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "Working…").FontSize(13).TextColor(pal.TextMuted)
				})
			} else {
				run := a.hub.Session(ps.id).Run(ip.runID)
				ui.Text(c, ip.reply).FontSize(13.5).TextColor(pal.Text).Selectable()
				// A reply in words can go in the page; edits are there already.
				if run != nil && run.Error == "" && run.Reply != "" && len(run.Ops) == 0 {
					ui.Row(c).Gap(8).Children(func() {
						if button(c, "insert", "plus", "Insert into page").Clicked() {
							a.insertReply(ip.blockID, run.Reply)
							a.dialogs.inline = nil
						}
					})
				}
			}
			return
		}
		if ip.selection && ip.text == "" {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, s := range []string{"Explain this", "Make it shorter", "Fix the grammar", "Turn into a list", "Chart these numbers"} {
					if ghost(c, "chip:"+s, "", s).Border(1, pal.AccentSoftBorder).Clicked() {
						ip.text = s
						a.sendInline(ip)
					}
				}
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			in := growField(c, "ask", &ip.text, "Ask the agent to write, rewrite or explain…").AutoFocus()
			if in.Submitted() {
				a.sendInline(ip)
			}
			if in.OverlayShortcut(0, ui.KeyEscape) {
				a.dialogs.inline = nil
				ps.editor.Focus()
			}
			if primary(c, "send", "arrow-up", "Send").Disabled(strings.TrimSpace(ip.text) == "").Clicked() {
				a.sendInline(ip)
			}
		})
	})
}

// insertReply puts an agent's reply in the page after a block, as
// paragraphs, list items and headings written in Markdown.
func (a *App) insertReply(after, reply string) {
	if a.page == nil {
		return
	}
	var nodes []*doc.Node
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		typ, level := "paragraph", 0
		switch {
		case strings.HasPrefix(line, "### "):
			typ, level, line = "heading", 3, line[4:]
		case strings.HasPrefix(line, "## "):
			typ, level, line = "heading", 2, line[3:]
		case strings.HasPrefix(line, "# "):
			typ, level, line = "heading", 1, line[2:]
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			typ, line = "bulleted_list", line[2:]
		}
		n := &doc.Node{ID: doc.NewBlockID(), Type: typ, Data: map[string]any{}}
		if level > 0 {
			n.Data["level"] = level
		}
		n.SetDelta(doc.FromMarkdown(line))
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 {
		return
	}
	a.page.editor.Edit(func(c *doc.Content) {
		if c.Find(after) == nil {
			after = ""
		}
		if after == "" {
			_ = c.Insert(doc.Position{}, nodes...)
			return
		}
		_ = c.Insert(doc.Position{After: after}, nodes...)
	})
}

func short(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return string(rs)
}

func (a *App) sendInline(ip *inlinePrompt) {
	if strings.TrimSpace(ip.text) == "" || a.page == nil {
		return
	}
	surface := "inline"
	if ip.selection {
		surface = "selection"
	}
	var agent *store.ModelChoice
	if ip.agent.ProviderID != "" {
		ch := ip.agent
		agent = &ch
	}
	ip.working = true
	run := a.hub.Start(a.page.id, agents.RunOptions{
		Surface: surface, Instruction: ip.text, Focus: []string{ip.blockID}, Selection: ip.quote, Agent: agent,
		Done: func(r *agents.Run) {
			ip.working = false
			ip.reply = agents.Describe(r)
			// What it made is in the page, with Undo below it.
			if r.Error == "" && len(r.Ops) > 0 && a.dialogs.inline == ip {
				a.dialogs.inline = nil
			}
		},
	})
	ip.runID = run.ID
}
