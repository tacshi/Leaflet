package app

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"

	"leaflet/internal/agents"
	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/menu"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

type inlinePrompt struct {
	blockID   string
	blocks    []string
	quote     string
	selection bool
	agent     store.ModelChoice
	agentName string
	text      string
	runID     string
	reply     string
	working   bool
	from, to  editor.Pos
	shown     bool
	panel     ui.Handle
}

type newComment struct {
	blockID string
	quote   string
	text    string
	postNow bool
	agents  agentPicker
}

type dialogs struct {
	searchOpen bool
	query      string
	hits       []store.SearchHit
	hitIndex   int
	hitList    ui.ListState
	searches   int
	searched   int
	submitted  bool

	folderOpen   bool
	folder       *store.Folder
	folderParent string
	folderName   string
	folderSelect bool

	tagOpen   bool
	tagOld    string
	tagName   string
	tagSelect bool

	recOpen   bool
	recBlock  string
	recName   string
	recSelect bool

	notesOpen  bool
	notesBlock string
	notesText  string
	notesFile  bool

	speakerOpen  bool
	speakerBlock string
	speakerFrom  string
	speakerLine  int
	speakerName  string

	moveOpen   bool
	movePage   string
	moveFolder string
	moveQuery  string

	historyOpen   bool
	historyPage   string
	history       []store.HistoryEntry
	historyPick   int
	historyView   int
	historyDiff   codeeditor.DiffState
	historyDiffOf string
	versionOf     string
	version       *doc.Content
	versionMD     string
	versionErr    error
	sideBySide    bool
	naming        bool
	versionName   string

	shareOpen   bool
	sharePage   string
	pubEmails   string
	pubComments bool

	chartOpen   bool
	chartID     string
	chartEditor *editor.Editor
	chartTitle  string
	chartKind   int
	chartCSV    string
	chartCap    string
	chartError  string

	embedOpen   bool
	embedID     string
	embedTitle  string
	embedHTML   string
	embedHeight string
	embedError  string

	linkOpen      bool
	linkKind      string
	linkID        string
	linkURL       string
	linkError     string
	linkFollowing bool
	linkOpens     int

	sourceOpen bool
	sourceID   string
	sourceKind string
	sourceText string

	inline       *inlinePrompt
	newComment   *newComment
	commentFocus string
	commentAt    time.Time
	commentShown bool
	showResolved bool
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
	if d.tagOpen {
		a.tagDialog(c)
	}
	if d.speakerOpen {
		a.speakerDialog(c)
	}
	if d.recOpen {
		a.recordingDialog(c)
	}
	if d.notesOpen {
		a.videoNotesDialog(c)
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
	if d.linkOpen {
		a.linkDialog(c)
	}
	if d.sourceOpen {
		a.sourceDialog(c)
	}
}

func (a *App) openSearch() {
	a.flush()
	a.dialogs.searchOpen = true
	a.dialogs.query = ""
	a.dialogs.hitIndex = 0
	a.dialogs.hits = a.searchHits("")
	a.dialogs.searches++
	a.dialogs.searched, a.dialogs.submitted = a.dialogs.searches, false
}

// submitSearch opens the chosen hit of the query typed: once its results
// are in, when the search is still under way.
func (a *App) submitSearch() {
	d := &a.dialogs
	if d.searched != d.searches {
		d.submitted = true
		return
	}
	if d.hitIndex < len(d.hits) {
		a.openHit(d.hits[d.hitIndex])
	}
}

func (a *App) searchDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	ui.DialogBase(c, &d.searchOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(10, 12, 16, 0.28)).Justify(ui.Start).PaddingY(90)
		panel.Width(600).Radius(12).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 24, 60, 0, pal.Shadow).Clip()
		ui.Row(c).Height(52).Padding(0, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
			in := ui.SearchField(c.Key("q"), &d.query).Placeholder(locales.DialogSearchPlaceholder()).FontSize(16).Grow(1).Padding(0).BorderWidth(0).Background(ui.Transparent).AutoFocus()
			if in.Changed() {
				a.search(d.query)
			}
			if in.Shortcut(0, ui.KeyDown) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex + 1) % len(d.hits)
				d.hitList.ScrollIntoView(d.hitIndex)
			}
			if in.Shortcut(0, ui.KeyUp) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex - 1 + len(d.hits)) % len(d.hits)
				d.hitList.ScrollIntoView(d.hitIndex)
			}
			if in.Submitted() {
				a.submitSearch()
			}
			ui.Text(c, locales.DialogSearchEscKey()).FontSize(11).TextColor(pal.TextMuted).Padding(2, 6).Radius(4).Border(1, pal.Divider)
		})
		if d.query == "" && len(d.hits) > 0 {
			ui.Box(c).Padding(6, 6, 0).Children(func() { label(c, locales.DialogSearchRecent()) })
		}
		if len(d.hits) > 0 {
			d.hitList.Selected = &d.hitIndex
			d.hitList.Label = func(i int) string { return d.hits[i].Title }
			hits := ui.List(c, &d.hitList, len(d.hits), func(i int) {
				h := d.hits[i]
				ui.Row(c).Padding(8, 10).Gap(12).AlignItems(ui.Center).Children(func() {
					icon := map[string]string{"page": "file-text", "template": "layout-template", "recording": "audio-lines", "comment": "message-square"}[h.Kind]
					ui.Icon(c, theme.Icon(icon)).FontSize(15)
					ui.Column(c).Grow(1).Shrink(1).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, h.Title).FontSize(14).SingleLine().Shrink(1)
							if h.Kind == "template" {
								templatePill(c)
							}
							if pg := a.pageByID(h.PageID); pg != nil && h.Kind == "page" {
								for _, t := range pg.Tags {
									if i == d.hitIndex {
										tagChipOnAccent(c, t)
									} else {
										tagChip(c, t)
									}
								}
							}
						})
						if h.Snippet != "" {
							muted, match := pal.TextMuted, pal.Text
							if i == d.hitIndex {
								muted, match = pal.OnAccent.Alpha(0.75), pal.OnAccent
							}
							ui.RichText(c, matchSpans(h.Snippet, d.query, match)...).FontSize(12).TextColor(muted).SingleLine()
						}
					})
				})
			}).MaxHeight(420).Padding(6)
			if hits.Changed() || hits.Submitted() {
				a.openHit(d.hits[min(d.hitIndex, len(d.hits)-1)])
			}
		}
		if d.searchOpen && d.hitIndex < len(d.hits) {
			if t, ok := a.hitTemplate(d.hits[d.hitIndex]); ok {
				ui.Row(c.Key("template-bar")).Padding(6, 10, 6, 16).Gap(10).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
					ui.Text(c, locales.DialogSearchTemplateEnter()).FontSize(12).TextColor(pal.TextMuted).Grow(1).SingleLine()
					if button(c, "edit-template", "pencil", locales.TemplateEdit()).Height(26).Clicked() {
						d.searchOpen = false
						a.editTemplate(t)
					}
				})
			}
		}
		ui.Box(c).Children(func() {
			if d.query != "" && len(d.hits) == 0 {
				ui.Column(c).Padding(24).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.DialogSearchNoMatch(d.query)).TextColor(pal.TextMuted)
					if a.hub.Available() && button(c, "ask", "sparkles", locales.DialogSearchAskAgent()).Clicked() {
						q := d.query
						d.searchOpen = false
						sess := agents.WorkspaceID
						if a.page != nil {
							sess = a.page.id
							a.openPageAgent()
						} else {
							a.openWorkspace()
						}
						switch {
						case a.hub.Session(sess).Running:
							a.draftsOf(sess).chat.draft = q
						case a.page != nil:
							a.hub.Chat(a.page.id, q, nil)
						default:
							a.askWorkspace(q)
						}
					}
				})
			}
		})
	})
}

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
	if t, ok := a.hitTemplate(h); ok {
		a.newPageFrom("", "", &t)
		return
	}
	a.openPage(h.PageID)
	if h.BlockID != "" && a.page != nil && a.page.id == h.PageID {
		a.page.editor.PlaceCaret(h.BlockID, 0)
	}
	if h.Kind == "comment" {
		a.showComments(h.BlockID)
		if a.page != nil && slices.ContainsFunc(a.page.threads, func(t store.Thread) bool { return t.Root.BlockID == h.BlockID && t.Root.Resolved }) {
			a.dialogs.showResolved = true
		}
	}
}

func (d *dialogs) openRenameFolder(f *store.Folder) {
	d.folderOpen, d.folder, d.folderParent = true, f, ""
	d.folderName = ""
	if f != nil {
		d.folderName, d.folderSelect = f.Name, true
	}
}

func (d *dialogs) openNewFolder(parentID string) {
	d.openRenameFolder(nil)
	d.folderParent = parentID
}

func (a *App) folderDialog(c *ui.Context) {
	d := &a.dialogs
	title := locales.DialogFolderNew()
	if d.folder != nil {
		title = locales.DialogFolderRename()
	}
	dialog(c, &d.folderOpen, 380, func() {
		dialogHeader(c, title, locales.DialogFolderSubtitle(), &d.folderOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			in := field(c, "name", &d.folderName, locales.DialogFolderName()).AutoFocus()
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
					if f, err := a.st.CreateFolderIn(name, d.folderParent); err != nil {
						a.toast(locales.DialogFolderCreateFailed(err.Error()))
					} else {
						if d.folderParent != "" {
							a.openFolders(d.folderParent)
						}
						a.showInTree("folder", f.ID)
					}
				} else {
					if err := a.st.RenameFolder(*d.folder, name); err != nil {
						a.toast(locales.DialogFolderRenameFailed(err.Error()))
					}
				}
				d.folderOpen = false
			}
			if in.Submitted() {
				save()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.folderOpen = false
				}
				label := locales.DialogFolderCreate()
				if d.folder != nil {
					label = locales.CommonRename()
				}
				if primary(c, "save", "", label).Clicked() {
					save()
				}
			})
		})
	})
}

func (d *dialogs) openMove(id string) {
	d.moveOpen, d.movePage, d.moveFolder, d.moveQuery = true, id, "", ""
}

func (d *dialogs) openMoveFolder(id string) {
	d.moveOpen, d.movePage, d.moveFolder, d.moveQuery = true, "", id, ""
}

func (a *App) moveDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	lib := a.library()
	title, search := a.pageTitleOf(d.movePage), locales.DialogMoveSearch()
	if d.moveFolder != "" {
		f, ok := lib.Folder(d.moveFolder)
		if !ok {
			d.moveOpen = false
			return
		}
		title, search = f.Name, locales.DialogMoveSearchFolders()
	}
	dialog(c, &d.moveOpen, 440, func() {
		dialogHeader(c, locales.DialogMoveTitle(title), locales.DialogMoveSubtitle(), &d.moveOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(10).Children(func() {
			field(c, "q", &d.moveQuery, search).AutoFocus()
			ui.Scroll(c).MaxHeight(320).Children(func() {
				target := func(key, icon, title string, depth int, move func() error) {
					row := ui.ButtonBase(c.Key(key)).Padding(7, 10).PaddingStart(10 + float32(depth)*16).Gap(10).Radius(7).
						AlignItems(ui.Center).Justify(ui.Start).FillWidth()
					if row.Hovered() {
						row.Background(pal.Hover)
					}
					row.Children(func() {
						ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.Icon)
						ui.Text(c, title).FontSize(13.5).TextColor(pal.Text).SingleLine()
					})
					if row.Clicked() {
						if err := move(); err != nil {
							a.toast(locales.SidebarCouldNotMove(err.Error()))
						} else {
							d.moveOpen = false
						}
					}
				}
				q := strings.ToLower(d.moveQuery)
				folders, depths := lib.FolderOrder()
				if id := d.moveFolder; id != "" {
					if q == "" {
						target("top", "file-text", locales.DialogMoveTopLevel(), 0, func() error { return a.st.MoveFolder(id, "", "") })
					}
					for i, f := range folders {
						if lib.FolderUnder(f.ID, id) || q != "" && !strings.Contains(strings.ToLower(f.Name), q) {
							continue
						}
						depth := depths[i]
						if q != "" {
							depth = 0
						}
						target("folder:"+f.ID, "folder", f.Name, depth, func() error {
							if err := a.st.MoveFolder(id, f.ID, ""); err != nil {
								return err
							}
							a.openFolders(f.ID)
							return nil
						})
					}
					return
				}
				movePage := func(parent, folder string) func() error {
					return func() error {
						err := a.pageCore().MovePage(d.movePage, parent, func() error {
							return a.st.MovePage(d.movePage, parent, folder, "")
						})
						switch {
						case err != nil:
						case parent != "":
							a.treeOpen("page", parent)
						case folder != "":
							a.openFolders(folder)
						}
						return err
					}
				}
				if q == "" && a.canMoveInside(d.movePage, "") {
					target("top", "file-text", locales.DialogMoveTopLevel(), 0, movePage("", ""))
					for i, f := range folders {
						target("folder:"+f.ID, "folder", f.Name, depths[i], movePage("", f.ID))
					}
				}
				for _, p := range a.pages {
					if !a.canMoveInside(d.movePage, "page:"+p.ID) {
						continue
					}
					if q != "" && !strings.Contains(strings.ToLower(p.DisplayTitle()), q) {
						continue
					}
					target("page:"+p.ID, "file-text", p.DisplayTitle(), 0, movePage(p.ID, ""))
				}
			})
		})
	})
}

func (a *App) openHistory(id string) {
	if a.page != nil && a.page.id == id {
		a.savePage()
	}
	d := &a.dialogs
	d.historyOpen, d.historyPage, d.historyPick, d.historyDiffOf, d.versionOf, d.naming = true, id, 0, "", "", false
	d.history, _ = a.st.History(id)
}

func (a *App) nameVersion(name string) {
	d := &a.dialogs
	if strings.TrimSpace(name) == "" {
		return
	}
	if err := a.pageCore().NameVersion(d.historyPage, name); err != nil {
		a.toast(locales.DialogHistoryKeepFailed(err.Error()))
		return
	}
	reread(&d.history, func() ([]store.HistoryEntry, error) { return a.st.History(d.historyPage) })
	d.historyPick, d.historyDiffOf, d.versionOf, d.naming, d.versionName = 0, "", "", false, ""
}

func historyDays(history []store.HistoryEntry) [][]int {
	var days [][]int
	for i, h := range history {
		if i > 0 {
			y, m, d := h.CreatedAt.Local().Date()
			py, pm, pd := history[i-1].CreatedAt.Local().Date()
			if y == py && m == pm && d == pd {
				days[len(days)-1] = append(days[len(days)-1], i)
				continue
			}
		}
		days = append(days, []int{i})
	}
	return days
}

func (a *App) historyRow(c *ui.Context, i int) {
	pal := theme.Of(c)
	d := &a.dialogs
	h := d.history[i]
	row := ui.ButtonBase(c.Key(h.ID)).Padding(8, 10).Radius(7).Justify(ui.Start).Gap(8).AlignItems(ui.Start)
	if i == d.historyPick {
		row.Background(pal.NavSelected)
	} else if row.Hovered() {
		row.Background(pal.Hover)
	}
	named := h.Reason == "manual"
	row.Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, locales.DialogHistoryTime(h.CreatedAt)).FontSize(13.5).FontWeight(550).TextColor(pal.Text)
			l := ui.Text(c, core.HistoryLabel(h)).FontSize(12).TextColor(pal.TextMuted).MaxLines(2)
			if named {
				l.TextColor(pal.AccentText).FontWeight(600)
			}
		})
		if named {
			ui.Icon(c, theme.Icon("bookmark")).FontSize(13).TextColor(pal.AccentText)
		}
	})
	if row.Clicked() {
		d.historyPick = i
	}
}

func (a *App) historyDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.historyOpen, 860, func() {
		dialogHeader(c, locales.DialogHistoryTitle(), a.pageTitleOf(d.historyPage), &d.historyOpen)
		ui.Row(c).Height(500).AlignItems(ui.Stretch).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
			ui.Column(c).Width(250).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Children(func() {
				ui.Column(c).Padding(8, 8, 4).Children(func() {
					if !d.naming {
						if ghost(c, "name-version", "bookmark", locales.DialogHistoryNameVersion()).Justify(ui.Start).FillWidth().Clicked() {
							d.naming = true
						}
						return
					}
					in := ui.TextInput(c.Key("version-name"), &d.versionName).Placeholder(locales.DialogHistoryNamePlaceholder()).AutoFocus()
					if in.Submitted() {
						a.nameVersion(d.versionName)
					}
					if in.Shortcut(0, ui.KeyEscape) {
						d.naming, d.versionName = false, ""
					}
				})
				ui.Scroll(c).Grow(1).Padding(0, 6, 6).Children(func() {
					if len(d.history) == 0 {
						ui.Text(c, locales.DialogHistoryEmpty()).
							FontSize(12.5).TextColor(pal.TextMuted).Padding(8, 10)
					}
					for _, day := range historyDays(d.history) {
						ui.Column(c.Key(d.history[day[0]].ID + ":day")).Children(func() {
							ui.Text(c, locales.Day(d.history[day[0]].CreatedAt, time.Now())).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).
								Padding(10, 10, 4).FillWidth().Background(pal.Surface).Sticky().Top(0)
							for _, i := range day {
								a.historyRow(c, i)
							}
						})
					}
				})
			})
			if len(d.history) == 0 {
				return
			}
			ui.Column(c).Grow(1).Children(func() {
				h := d.history[min(d.historyPick, len(d.history)-1)]
				content, md, err := a.historyVersion(h)
				if err != nil {
					ui.Text(c, locales.DialogHistoryUnreadable()).FontSize(13).TextColor(pal.TextMuted).Padding(18, 24).Grow(1)
					ui.Row(c).Padding(12, 16).Justify(ui.End).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
						if button(c, "close", "", locales.CommonClose()).Clicked() {
							d.historyOpen = false
						}
					})
					return
				}
				ui.Row(c).Padding(10, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
					ui.Segmented(c.Key("history-view"), &d.historyView, locales.DialogHistoryThisVersion(), locales.DialogHistoryChangesSince())
					ui.Spacer(c)
					if d.historyView == 1 {
						a.historyStats(c, h, content)
						tools(c, func() {
							side := ui.Toggle(c.Key("side-by-side"), &d.sideBySide, "").Label(locales.DialogHistorySideBySide()).Tooltip(locales.DialogHistorySideBySide())
							side.Children(func() { ui.Icon(c, theme.Icon("columns-2")).FontSize(16) })
						})
					}
				})
				if d.historyView == 1 {
					a.historyChanges(c, h, content)
				} else {
					ui.Scroll(c).Grow(1).Padding(18, 24).Children(func() {
						markdownView(c, md, &a.md).Font(theme.PageFamily(md)).FontSize(15)
					})
				}
				ui.Row(c).Padding(12, 16).Justify(ui.End).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
					if button(c, "close", "", locales.CommonClose()).Clicked() {
						d.historyOpen = false
					}
					if primary(c, "restore", "history", locales.DialogHistoryRestore()).Disabled(!a.canEdit(d.historyPage)).Clicked() {
						a.restoreVersion(d.historyPage, h.ID)
						d.historyOpen = false
					}
				})
			})
		})
	})
}

func (a *App) historyVersion(h store.HistoryEntry) (*doc.Content, string, error) {
	d := &a.dialogs
	if d.versionOf != h.ID {
		d.versionOf, d.versionMD = h.ID, ""
		if d.version, d.versionErr = doc.Decode(h.Content); d.versionErr == nil {
			d.versionMD = doc.PageMarkdown("", d.version, a.markdownOptions())
		}
	}
	return d.version, d.versionMD, d.versionErr
}

func (a *App) historyDiff(h store.HistoryEntry, content *doc.Content) *codeeditor.DiffState {
	d := &a.dialogs
	if d.historyDiffOf != h.ID {
		now := a.st.Content(d.historyPage)
		if now == nil {
			now = doc.Empty()
		}
		old, cur := doc.PageMarkdown("", content, a.markdownOptions()), doc.PageMarkdown("", now, a.markdownOptions())
		d.historyDiff = codeeditor.DiffState{
			Old:  codeeditor.NewBuffer(old),
			New:  codeeditor.NewBuffer(cur),
			Wrap: true, NoLineNumbers: true,
			Font: ui.Font{Family: theme.PageFamily(old + cur), Size: 14},
		}
		d.historyDiffOf = h.ID
	}
	d.historyDiff.Layout = codeeditor.DiffUnified
	if d.sideBySide {
		d.historyDiff.Layout = codeeditor.DiffSplit
	}
	return &d.historyDiff
}

func (a *App) historyStats(c *ui.Context, h store.HistoryEntry, content *doc.Content) {
	pal := theme.Of(c)
	added, deleted := a.historyDiff(h, content).Stats()
	if added == 0 && deleted == 0 {
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.Textf(c, "+%d", added).FontSize(12.5).FontWeight(600).TextColor(pal.OkText)
		ui.Textf(c, "−%d", deleted).FontSize(12.5).FontWeight(600).TextColor(pal.DangerText)
		ui.Text(c, locales.DialogHistoryLines(added+deleted)).FontSize(12.5).TextColor(pal.TextMuted)
	})
}

func (a *App) historyChanges(c *ui.Context, h store.HistoryEntry, content *doc.Content) {
	pal := theme.Of(c)
	diff := a.historyDiff(h, content)
	if added, deleted := diff.Stats(); added == 0 && deleted == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, locales.DialogHistoryNoChanges()).TextColor(pal.TextMuted)
		})
		return
	}
	ui.Box(c).Grow(1).Padding(8, 0, 0, 8).Children(func() {
		codeeditor.DiffView(c, diff).Fill()
	})
}

func (a *App) restoreVersion(pageID, id string) {
	kept, ok := a.putBackVersion(pageID, id)
	if !ok {
		return
	}
	if kept == "" {
		a.toast(locales.DialogHistoryRestored())
		return
	}
	a.toastAction(locales.DialogHistoryRestored(), locales.CommonUndo(), func() { a.putBackVersion(pageID, kept) })
}

func (a *App) putBackVersion(pageID, id string) (kept string, ok bool) {
	kept, run, err := a.pageCore().RestoreVersion(pageID, id)
	if err != nil {
		a.toast(locales.DialogHistoryRestoreFailed(err.Error()))
		return "", false
	}
	if ed := a.openEditor(pageID); ed != nil {
		if run != "" {
			a.dialogs.hideRun = run
		}
		clear(a.page.tinted)
		ed.Select(ed.Caret(), ed.Caret())
	}
	return kept, true
}

func (a *App) openShare(id string) {
	d := &a.dialogs
	d.shareOpen, d.sharePage = true, id
	d.pubEmails, d.pubComments = "", false
	if p := a.pub; p != nil && p.page == id {
		p.mu.Lock()
		d.pubEmails, d.pubComments = strings.Join(p.emails, ", "), p.comments
		p.mu.Unlock()
	}
	a.checkCloudflared()
}

func (a *App) shareDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.shareOpen, 480, func() {
		dialogHeader(c, locales.DialogShareTitle(a.pageTitleOf(d.sharePage)), "", &d.shareOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			if a.ownsPage(d.sharePage) {
				a.publicLink(c)
				ui.Box(c).Height(1).FillWidth().Background(pal.Divider)
			} else {
				notice(c, "info", "info", locales.DialogShareOwnerOnly(), nil)
			}
			ui.Row(c).Gap(8).Children(func() {
				if button(c, "copy", "link", locales.DialogShareCopyLink()).Clicked() {
					c.WriteClipboard(doc.PageLink(d.sharePage))
					c.Toast(locales.DialogShareLinkCopied())
				}
			})
		})
	})
}

const cloudflaredDownloads = "https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/"

func (a *App) publicLink(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	p := a.pub
	cf := a.cloudflared
	label(c, locales.PubTitle())
	switch {
	case p == nil && !cf.checked:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.PubChecking()).FontSize(13).TextColor(pal.TextMuted)
		})
		return
	case p == nil && cf.path == "":
		a.installCloudflared(c)
		return
	case p != nil && p.page != d.sharePage:
		notice(c, "info", "globe", locales.PubOtherPage(a.pageTitleOf(p.page)), func() {
			if button(c, "stop-other", "", locales.PubStop()).Clicked() {
				a.stopPub()
			}
		})
		return
	}

	emails := splitEmails(d.pubEmails)
	valid := validEmails(emails)
	start := func() {
		if valid {
			a.startPub(d.sharePage, emails, d.pubComments)
		}
	}
	on := p != nil
	ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, locales.PubShare()).FontSize(14).FontWeight(500).TextColor(pal.Text)
			ui.Text(c, locales.PubShareDesc()).FontSize(12).TextColor(pal.TextMuted)
		})
		if ui.Switch(c.Key("pub"), &on).Disabled(!on && !valid).Changed() {
			if on {
				start()
			} else {
				a.stopPub()
			}
		}
	})
	if p != nil {
		switch p.state {
		case "starting":
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, locales.PubStartingLink()).FontSize(13).TextColor(pal.TextMuted)
			})
		case "live":
			ui.Row(c).Gap(14).AlignItems(ui.Start).FillWidth().Children(func() {
				if p.qr != nil {
					ui.Image(c, p.qr).Size(112, 112).Radius(8).Border(1, pal.Divider).Label(locales.PubQRCode())
				}
				ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
					ui.Row(c).Padding(6, 6, 6, 10).Gap(8).Radius(8).Border(1, pal.Divider).Background(pal.Ground).AlignItems(ui.Center).Children(func() {
						ui.Text(c, p.url).FontSize(12.5).TextColor(pal.Text).Selectable().Grow(1).MinWidth(0)
						tools(c, func() {
							if iconButton(c, "copy-link", "copy", locales.CommonCopy()).Clicked() {
								c.WriteClipboard(p.url)
								c.Toast(locales.DialogShareLinkCopied())
							}
						})
					})
					note := locales.PubAnyone()
					if len(p.emails) > 0 {
						note = locales.PubOnlyEmails()
					}
					ui.Text(c, note).FontSize(12.5).TextColor(pal.TextMuted)
				})
			})
		case "failed":
			notice(c, "danger", "triangle-alert", locales.PubStopped(), func() {
				if p.err != "" {
					ui.Text(c, p.err).Font("monospace").FontSize(11.5).TextColor(pal.DangerText).Wrap().Selectable()
				}
				if button(c, "retry", "", locales.CommonRetry()).Disabled(!valid).Clicked() {
					start()
				}
			})
		}
	}
	ui.Column(c).Gap(6).FillWidth().Children(func() {
		if !cf.canRestrict() && cf.path != "" {
			ui.Text(c, locales.PubEmailsUpdate(cloudflaredMailVersion)).FontSize(12).TextColor(pal.TextMuted)
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			growField(c, "emails", &d.pubEmails, locales.PubEmails())
			if p != nil && p.state != "failed" && !slices.Equal(emails, p.emails) {
				if primary(c, "apply", "", locales.PubApply()).Disabled(!valid).Clicked() {
					start()
				}
			}
		})
		if !valid {
			ui.Text(c, locales.PubEmailsInvalid()).FontSize(12).TextColor(pal.DangerText)
		} else {
			ui.Text(c, locales.PubEmailsHint()).FontSize(12).TextColor(pal.TextMuted)
		}
	})
	ui.Row(c).Gap(16).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, locales.PubComments()).FontSize(14).FontWeight(500).TextColor(pal.Text)
			ui.Text(c, locales.PubCommentsDesc()).FontSize(12).TextColor(pal.TextMuted)
		})
		if ui.Switch(c.Key("pub-comments"), &d.pubComments).Changed() {
			a.setPubComments(d.pubComments)
		}
	})
}

func (a *App) installCloudflared(c *ui.Context) {
	pal := theme.Of(c)
	cf := a.cloudflared
	if !cf.busy && time.Since(cf.at) > 3*time.Second {
		a.checkCloudflared()
	}
	c.After(3 * time.Second)
	copyBox := func(key, text string, mono bool) {
		ui.Row(c).Padding(6, 6, 6, 10).Gap(8).Radius(6).Border(1, pal.Divider).Background(pal.Ground).AlignItems(ui.Start).Children(func() {
			t := ui.Text(c, text).FontSize(12).TextColor(pal.Text).Selectable().Wrap().Grow(1).MinWidth(0)
			if mono {
				t.Font("monospace")
			}
			tools(c, func() {
				if iconButton(c, key, "copy", locales.CommonCopy()).Clicked() {
					c.WriteClipboard(text)
					c.Toast(locales.CommonCopied())
				}
			})
		})
	}
	notice(c, "info", "info", locales.PubNeedsCloudflared(), func() {
		if cmd := cloudflaredCommand(); cmd != "" {
			ui.Text(c, locales.PubInstallCommand()).FontSize(12).TextColor(pal.TextMuted)
			copyBox("copy-command", cmd, true)
		}
		ui.Text(c, locales.PubInstallAgent()).FontSize(12).TextColor(pal.TextMuted)
		copyBox("copy-prompt", cloudflaredPrompt(), false)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, locales.PubWaiting()).FontSize(12).TextColor(pal.TextMuted).Grow(1)
			if ghost(c, "guide", "", locales.PubInstallGuide()).Clicked() {
				_ = mygo.Shell.OpenExternal(cloudflaredDownloads)
			}
		})
	})
}

var chartKinds = []string{"bar", "line", "pie"}

func (d *dialogs) openChart(id string, ed *editor.Editor) {
	n := ed.Content.Find(id)
	if n == nil {
		return
	}
	kind, title, caption, _, _ := editor.ChartOf(n)
	d.chartOpen, d.chartID, d.chartEditor, d.chartTitle, d.chartCap, d.chartError = true, id, ed, title, caption, ""
	d.chartKind = 0
	for i, k := range chartKinds {
		if k == kind {
			d.chartKind = i
		}
	}
	d.chartCSV = doc.ChartCSV(n, locales.DialogChartLabelColumn())
}

func (a *App) chartDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.chartOpen, 520, func() {
		dialogHeader(c, locales.DialogChartTitle(), locales.DialogChartSubtitle(), &d.chartOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.chartTitle, locales.DialogFieldTitle())
			ui.Segmented(c.Key("kind"), &d.chartKind, locales.DialogChartBar(), locales.DialogChartLine(), locales.DialogChartPie())
			ui.TextArea(c.Key("csv"), &d.chartCSV).Height(180).Font("monospace").FontSize(13)
			field(c, "caption", &d.chartCap, locales.DialogChartCaption())
			if d.chartError != "" {
				ui.Text(c, d.chartError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.chartOpen = false
				}
				if primary(c, "save", "", locales.DialogChartSave()).Clicked() {
					data, err := menu.ChartData(chartKinds[d.chartKind], d.chartTitle, d.chartCap, d.chartCSV)
					if err != nil {
						d.chartError = err.Error()
						return
					}
					id := d.chartID
					d.chartEditor.Edit(func(content *doc.Content) {
						if n := content.Find(id); n != nil {
							data["author"] = n.Data["author"]
							n.Data = data
						}
					})
					d.chartOpen = false
				}
			})
		})
	})
}

func (d *dialogs) openEmbed() {
	d.embedOpen, d.embedID, d.embedTitle, d.embedHeight = true, "", locales.DialogEmbedTitle(), "320"
	d.embedHTML, d.embedError = menu.EmbedStart, ""
}

func (d *dialogs) editEmbed(n *doc.Node) {
	d.embedOpen, d.embedID, d.embedError = true, n.ID, ""
	d.embedTitle, d.embedHTML = n.Str("title"), n.Str("html")
	d.embedHeight = strconv.Itoa(n.Int("height", 320))
}

func (a *App) embedDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.embedOpen, 600, func() {
		dialogHeader(c, locales.DialogEmbedTitle(), locales.DialogEmbedSubtitle(), &d.embedOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.embedTitle, locales.DialogFieldTitle())
			ui.TextArea(c.Key("html"), &d.embedHTML).Height(240).Font("monospace").FontSize(12.5)
			if d.embedError != "" {
				ui.Text(c, d.embedError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.DialogEmbedHeight()).FontSize(13)
				field(c, "height", &d.embedHeight, "320").Width(80)
				ui.Spacer(c)
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.embedOpen = false
				}
				label := locales.DialogInsert()
				if d.embedID != "" {
					label = locales.CommonSave()
				}
				insert := primary(c, "insert", "", label).Disabled(strings.TrimSpace(d.embedHTML) == "")
				if insert.Clicked() && a.page != nil {
					h, _ := strconv.Atoi(d.embedHeight)
					n, err := menu.EmbedBlock(d.embedTitle, d.embedHTML, h, a.session.DisplayName)
					if err != nil {
						d.embedError = locales.DialogEmbedFailed(err.Error())
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

func (d *dialogs) openLinkDialog(kind string) {
	d.linkOpens++
	d.linkFollowing = false
	d.linkOpen, d.linkKind, d.linkID, d.linkURL, d.linkError = true, kind, "", "", ""
}

func (d *dialogs) editLink(n *doc.Node) {
	d.linkOpens++
	d.linkFollowing = false
	d.linkOpen, d.linkKind, d.linkID, d.linkURL, d.linkError = true, n.Type, n.ID, n.Str("url"), ""
}

func (a *App) linkDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	title, subtitle, example := locales.DialogLinkCardTitle(), locales.DialogLinkCardSubtitle(), "https://"
	switch d.linkKind {
	case "video":
		title, subtitle, example = locales.DialogLinkVideoTitle(), locales.DialogLinkVideoSubtitle(), "https://www.youtube.com/watch?v=…"
	case "image":
		title, subtitle = locales.DialogLinkImageTitle(), locales.DialogLinkImageSubtitle()
	}
	dialog(c, &d.linkOpen, 480, func() {
		dialogHeader(c, title, subtitle, &d.linkOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			in := field(c, "url", &d.linkURL, example).AutoFocus()
			if d.linkError != "" {
				ui.Text(c, d.linkError).FontSize(12.5).TextColor(pal.DangerText)
			}
			if in.Submitted() {
				a.submitLink()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if d.linkKind == "image" {
					if button(c, "upload", "upload", locales.DialogLinkImageUpload()).Clicked() {
						d.linkOpen = false
						a.uploadImage(d.linkID)
					}
					ui.Spacer(c)
				}
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.linkOpen = false
				}
				label := locales.DialogInsert()
				if d.linkID != "" {
					label = locales.CommonSave()
				}
				if primary(c, "insert", "", label).Disabled(d.linkFollowing).Clicked() {
					a.submitLink()
				}
			})
		})
	})
}

var followVideoLink = web.VideoLink

func (a *App) submitLink() {
	d := &a.dialogs
	link := strings.TrimSpace(d.linkURL)
	if d.linkFollowing {
		return
	}
	if d.linkKind != "video" || !doc.ShortVideoLink(link) {
		a.insertLink(link)
		return
	}
	d.linkFollowing, d.linkError = true, ""
	n := d.linkOpens
	var to string
	a.offMain(func() { to = followVideoLink(link) }, func() {
		if n != d.linkOpens {
			return
		}
		d.linkFollowing = false
		if d.linkOpen && strings.TrimSpace(d.linkURL) == link {
			a.insertLink(to)
		}
	})
}

func (a *App) insertLink(link string) {
	d := &a.dialogs
	n, err := menu.LinkBlock(d.linkKind, link)
	if err != nil {
		d.linkError = err.Error()
		return
	}
	switch {
	case a.page == nil:
	case d.linkID != "":
		a.setMediaLink(d.linkID, n.Str("url"))
	default:
		a.page.editor.InsertBlocks(n)
	}
	d.linkOpen = false
}

func (a *App) setMediaLink(id, link string) {
	ed := a.page.editor
	if n := ed.Content.Find(id); n == nil || n.Str("url") == link {
		return
	}
	ed.Edit(func(content *doc.Content) {
		n := content.Find(id)
		if n == nil {
			return
		}
		n.Set("url", link)
		if n.Type == "video" {
			n.Set("title", "")
		}
	})
	a.closePlayer(id)
}

func (a *App) inlinePrompt(c *ui.Context) {
	pal := theme.Of(c)
	ip := a.dialogs.inline
	if ip == nil || a.page == nil {
		return
	}
	ps := a.page
	at, ok := ps.editor.RangeRect(ip.from, ip.to)
	if !ok {
		return
	}
	if run := a.hub.Session(ps.id).Run(ip.runID); run != nil {
		for _, id := range run.BlockIDs() {
			if r, ok := ps.editor.BlockRect(id); ok && r.Y+r.H > at.Y+at.H {
				at.H = r.Y + r.H - at.Y
			}
		}
	}
	if view := ps.view.Bounds(c); ip.working && view.H > 0 {
		const gap = 8 + 24
		end := view.Y + view.H
		if room := end - ip.panel.Bounds(c).H - gap - at.Y; room > 0 && at.H > room {
			at.H = room
		}
	}
	ui.Overlay(c, func() {
		anchor := ui.Box(c.Key("inline-anchor")).Absolute().Left(at.X).Top(at.Y).Size(at.W, at.H).PassThrough()
		panel := ui.Column(c.Key("inline-prompt")).AttachTo(anchor, ui.AnchorBottomLeft, ui.AnchorTopLeft).Width(at.W).Margin(8, 0, 0).
			Padding(12).Gap(10).Radius(10).Background(pal.AgentSection).Border(1, pal.AccentSoftBorder).Shadow(0, 10, 30, 0, pal.Shadow).
			Bind(&ip.panel)
		if panel.PressedOutside() && ip.shown && strings.TrimSpace(ip.text) == "" && !ip.working && ip.reply == "" {
			a.dialogs.inline = nil
		}
		ip.shown = true
		a.inlinePanel(c, panel, ip, pal)
	})
}

func (a *App) inlinePanel(c *ui.Context, panel ui.Element, ip *inlinePrompt, pal *theme.Palette) {
	ps := a.page
	panel.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			avatar(c, "", 22, true)
			name := ip.agentName
			if name == "" {
				if o, err := a.hub.Resolve(nil); err == nil {
					name = o.Name()
				} else {
					name = locales.CommonAgent()
				}
			}
			title := locales.DialogInlineAsk(name)
			if ip.quote != "" {
				title = locales.DialogInlineAskAbout(name, short(ip.quote, 48))
			}
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(pal.AccentText).Grow(1).SingleLine()
			tools(c, func() {
				if iconButton(c, "close", "x", locales.CommonClose()).Clicked() {
					a.dialogs.inline = nil
					ps.editor.Focus()
				}
			})
		})
		if ip.reply != "" || ip.working {
			if ip.working {
				label := locales.DialogInlineWorking()
				if run := a.hub.Session(ps.id).Run(ip.runID); run != nil {
					label = runProgress(run, label)
					if run.Answer != "" {
						ui.Scroll(c).MaxHeight(320).Children(func() {
							markdownView(c, run.Answer, &a.md).FontSize(13.5)
						})
					}
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, label).FontSize(13).TextColor(pal.TextMuted).Grow(1)
					if run := a.hub.Session(ps.id).Run(ip.runID); run != nil && run.Stoppable() {
						if button(c, "stop", "square-stop", locales.PageStopRun()).Clicked() {
							a.hub.Stop(ps.id, run.ID)
						}
					}
				})
			} else {
				run := a.hub.Session(ps.id).Run(ip.runID)
				ui.Scroll(c).MaxHeight(320).Children(func() {
					markdownView(c, ip.reply, &a.md).FontSize(13.5)
				})
				if run != nil && run.Error == "" && run.Reply != "" && len(run.Ops) == 0 {
					ui.Row(c).Gap(8).Children(func() {
						if button(c, "insert", "plus", locales.DialogInlineInsert()).Clicked() {
							a.insertReply(ip.focus()[len(ip.focus())-1], run.Reply)
							a.dialogs.inline = nil
						}
					})
				}
			}
			return
		}
		if ip.selection && ip.text == "" {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				chips := []string{locales.DialogInlineExplain(), locales.DialogInlineShorter(), locales.DialogInlineGrammar()}
				for i, s := range chips {
					if ghost(c, "chip:"+strconv.Itoa(i), "", s).Border(1, pal.AccentSoftBorder).Clicked() {
						ip.text = s
						a.sendInline(ip)
					}
				}
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			in := growField(c, "ask", &ip.text, locales.DialogInlinePlaceholder()).AutoFocus()
			if in.Submitted() {
				a.sendInline(ip)
			}
			if in.OverlayShortcut(0, ui.KeyEscape) {
				a.dialogs.inline = nil
				ps.editor.Focus()
			}
			if primary(c, "send", "arrow-up", locales.DialogInlineSend()).Disabled(strings.TrimSpace(ip.text) == "").Clicked() {
				a.sendInline(ip)
			}
		})
	})
}

func (a *App) insertReply(after, reply string) {
	if a.page == nil {
		return
	}
	var run *agents.Run
	if ip := a.dialogs.inline; ip != nil && ip.runID != "" {
		if r := a.hub.Session(a.page.id).Run(ip.runID); r != nil && r.Reply == reply {
			run = r
		}
	}
	nodes := agents.ReplyBlocks(reply, run)
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
	rs := []rune(strings.Join(strings.Fields(s), " "))
	if len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return string(rs)
}

func (ip *inlinePrompt) focus() []string {
	if len(ip.blocks) > 0 {
		return ip.blocks
	}
	return []string{ip.blockID}
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
		Surface: surface, Instruction: ip.text, Focus: ip.focus(), Selection: ip.quote, Agent: agent,
		Done: func(r *agents.Run) {
			ip.working = false
			ip.reply = agents.Describe(r)
			if r.Error == "" && len(r.Ops) > 0 && a.dialogs.inline == ip {
				a.dialogs.inline = nil
			}
		},
	})
	ip.runID = run.ID
}
