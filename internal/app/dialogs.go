package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type inlinePrompt struct {
	blockID string
	// blocks are the blocks a selection spans, when it spans more than
	// blockID.
	blocks    []string
	quote     string
	selection bool
	agent     store.ModelChoice
	agentName string
	text      string
	runID     string
	reply     string
	working   bool
	// from and to are the text it was asked from, which it shows beside;
	// shown is set once it has.
	from, to editor.Pos
	shown    bool
}

type newComment struct {
	blockID string
	quote   string
	text    string
	agents  agentPicker
}

// dialogs is the state of the app's dialogs and transient panels.
type dialogs struct {
	searchOpen bool
	query      string
	hits       []store.SearchHit
	hitIndex   int
	hitList    ui.ListState

	folderOpen   bool
	folder       *store.Folder
	folderName   string
	folderSelect bool // select the name as the field takes the focus

	// The dialog renaming a tag on every page.
	tagOpen   bool
	tagOld    string
	tagName   string
	tagSelect bool

	moveOpen  bool
	movePage  string
	moveQuery string

	historyOpen bool
	historyPage string
	history     []store.HistoryEntry
	historyPick int
	// historyView shows the version chosen (0) or what changed since
	// (1), in historyDiff, made for the version historyDiffOf; naming is
	// set while the current version's name is typed in versionName.
	historyView   int
	historyDiff   codeeditor.DiffState
	historyDiffOf string
	// sideBySide shows the changes as two columns, kept while the app runs.
	sideBySide  bool
	naming      bool
	versionName string

	shareOpen   bool
	sharePage   string
	shareEmail  string
	shareRole   string
	shareBusy   bool
	shareError  string
	shareSent   string
	shareMember []store.Member
	// pubEmails is the public link's emails field, and pubComments its
	// Allow comments switch.
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
	embedID     string // the embed being edited, or "" for a new one
	embedTitle  string
	embedHTML   string
	embedHeight string

	// The dialog asking for a video's or a link card's link, or changing
	// an image's or video's.
	linkOpen  bool
	linkKind  string
	linkID    string // the block being changed, or "" for a new one
	linkURL   string
	linkError string

	// The dialog editing a math or diagram block's source.
	sourceOpen bool
	sourceID   string
	sourceKind string
	sourceText string

	inline       *inlinePrompt
	newComment   *newComment
	commentFocus string
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

// Search ----------------------------------------------------------------

func (a *App) openSearch() {
	a.flush()
	a.dialogs.searchOpen = true
	a.dialogs.query = ""
	a.dialogs.hitIndex = 0
	a.dialogs.hits = a.searchHits("")
}

func (a *App) searchDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	ui.DialogBase(c, &d.searchOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(10, 12, 16, 0.28)).Justify(ui.Start).PaddingY(90)
		panel.Width(600).Radius(12).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 24, 60, 0, pal.Shadow).Clip()
		ui.Row(c).Height(52).Padding(0, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
			// The header is its frame.
			in := ui.SearchField(c.Key("q"), &d.query).Placeholder(locales.DialogSearchPlaceholder()).FontSize(16).Grow(1).Padding(0).BorderWidth(0).Background(ui.Transparent).AutoFocus()
			if in.Changed() {
				d.hits = a.searchHits(d.query)
				d.hitIndex = 0
			}
			// The keys stay in the field; Up and Down move the list's
			// choice.
			if in.Shortcut(0, ui.KeyDown) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex + 1) % len(d.hits)
				d.hitList.ScrollIntoView(d.hitIndex)
			}
			if in.Shortcut(0, ui.KeyUp) && len(d.hits) > 0 {
				d.hitIndex = (d.hitIndex - 1 + len(d.hits)) % len(d.hits)
				d.hitList.ScrollIntoView(d.hitIndex)
			}
			if in.Submitted() && d.hitIndex < len(d.hits) {
				a.openHit(d.hits[d.hitIndex])
			}
			ui.Text(c, "esc").FontSize(11).TextColor(pal.TextMuted).Padding(2, 6).Radius(4).Border(1, pal.Divider)
		})
		if d.query == "" && len(d.hits) > 0 {
			ui.Box(c).Padding(6, 6, 0).Children(func() { label(c, locales.DialogSearchRecent()) })
		}
		if len(d.hits) > 0 {
			// MyGo's list: its row chosen, which the field's keys move, a
			// click and Enter open.
			d.hitList.Selected = &d.hitIndex
			d.hitList.Label = func(i int) string { return d.hits[i].Title }
			hits := ui.List(c, &d.hitList, len(d.hits), func(i int) {
				h := d.hits[i]
				ui.Row(c).Padding(8, 10).Gap(12).AlignItems(ui.Center).Children(func() {
					icon := map[string]string{"page": "file-text", "template": "layout-template", "recording": "mic", "comment": "message-square"}[h.Kind]
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
							// On the row chosen, in the accent, the snippet is in
							// its text's color, as the title is.
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
		// A template chosen starts a page; its own page is a click away.
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
					if a.page != nil && a.hub.Available() && button(c, "ask", "sparkles", locales.DialogSearchAskAgent()).Clicked() {
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

// openHit opens what a search hit found; a template, or a starter, starts a
// new page.
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
		a.panelOpen, a.panelTab = true, 1
		a.dialogs.commentFocus = h.BlockID
		// The comment found may be in a resolved thread.
		if a.page != nil && slices.ContainsFunc(a.page.threads, func(t store.Thread) bool { return t.Root.BlockID == h.BlockID && t.Root.Resolved }) {
			a.dialogs.showResolved = true
		}
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
					if f, err := a.st.CreateFolder(name); err == nil {
						a.treeOpen("folder", f.ID)
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

// Move ------------------------------------------------------------------

func (d *dialogs) openMove(id string) {
	d.moveOpen, d.movePage, d.moveQuery = true, id, ""
}

func (a *App) moveDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.moveOpen, 440, func() {
		dialogHeader(c, locales.DialogMoveTitle(a.pageTitleOf(d.movePage)), locales.DialogMoveSubtitle(), &d.moveOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(10).Children(func() {
			field(c, "q", &d.moveQuery, locales.DialogMoveSearch()).AutoFocus()
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
						if err := a.movePage(d.movePage, parent, func() error {
							return a.st.MovePage(d.movePage, parent, folder, "")
						}); err != nil {
							a.toast(err.Error())
						} else {
							d.moveOpen = false
							if parent != "" {
								a.treeOpen("page", parent)
							}
						}
					}
				}
				// The places a drag in the sidebar could put the page.
				q := strings.ToLower(d.moveQuery)
				if q == "" && a.canMoveInside(d.movePage, "") {
					target("top", "file-text", locales.DialogMoveTopLevel(), "", "")
					for _, f := range a.folders {
						target("folder:"+f.ID, "folder", f.Name, "", f.ID)
					}
				}
				for _, p := range a.pages {
					if !a.canMoveInside(d.movePage, "page:"+p.ID) {
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
	d.historyOpen, d.historyPage, d.historyPick, d.historyDiffOf, d.naming = true, id, 0, "", false
	d.history, _ = a.st.History(id)
}

// historyLabel says what a version is: its name, or why it was kept.
func historyLabel(h store.HistoryEntry) string {
	if h.Label != "" {
		return h.Label
	}
	switch h.Reason {
	case "agent_run":
		return locales.DialogHistoryAgentRun()
	case "autosave":
		return locales.DialogHistoryAutosave()
	case "restore":
		return locales.DialogHistoryBeforeRestore()
	case "conflict":
		return locales.DialogHistoryConflict()
	}
	return ""
}

// nameVersion keeps the page as it is now as a version with a name.
func (a *App) nameVersion(name string) {
	d := &a.dialogs
	if a.page != nil && a.page.id == d.historyPage {
		a.savePage()
	}
	c := a.st.Content(d.historyPage)
	if c == nil || strings.TrimSpace(name) == "" {
		return
	}
	if err := a.st.AddHistory(d.historyPage, c, "manual", "", strings.TrimSpace(name)); err != nil {
		a.toast(locales.DialogHistoryKeepFailed(err.Error()))
		return
	}
	d.history, _ = a.st.History(d.historyPage)
	d.historyPick, d.historyDiffOf, d.naming, d.versionName = 0, "", false, ""
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
					for i, h := range d.history {
						row := ui.ButtonBase(c.Key(h.ID)).Padding(8, 10).Radius(7).Justify(ui.Start).Gap(8).AlignItems(ui.Start)
						if i == d.historyPick {
							row.Background(pal.NavSelected)
						} else if row.Hovered() {
							row.Background(pal.Hover)
						}
						named := h.Reason == "manual"
						row.Children(func() {
							ui.Column(c).Gap(2).Grow(1).Children(func() {
								ui.Text(c, locales.DialogHistoryWhen(h.CreatedAt)).FontSize(13.5).FontWeight(550).TextColor(pal.Text)
								l := ui.Text(c, historyLabel(h)).FontSize(12).TextColor(pal.TextMuted).MaxLines(2)
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
				})
			})
			if len(d.history) == 0 {
				return
			}
			ui.Column(c).Grow(1).Children(func() {
				h := d.history[min(d.historyPick, len(d.history)-1)]
				content := doc.MustDecode(h.Content)
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
						md := doc.PageMarkdown("", content, a.pageTitleOf)
						markdown.View(c, md, &a.md).Font(theme.PageFamily(md)).FontSize(15)
					})
				}
				ui.Row(c).Padding(12, 16).Justify(ui.End).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
					if button(c, "close", "", locales.CommonClose()).Clicked() {
						d.historyOpen = false
					}
					if primary(c, "restore", "history", locales.DialogHistoryRestore()).Disabled(!a.canEdit(d.historyPage)).Clicked() {
						a.restoreVersion(d.historyPage, content)
						d.historyOpen = false
					}
				})
			})
		})
	})
}

// historyDiff compares a version's Markdown with the page's now, in the
// diff viewer, made again when another version is chosen.
func (a *App) historyDiff(h store.HistoryEntry, content *doc.Content) *codeeditor.DiffState {
	d := &a.dialogs
	if d.historyDiffOf != h.ID {
		now := a.st.Content(d.historyPage)
		if now == nil {
			now = doc.Empty()
		}
		old, cur := doc.PageMarkdown("", content, a.pageTitleOf), doc.PageMarkdown("", now, a.pageTitleOf)
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

// historyStats says how many lines changed since a version.
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

// historyChanges shows what changed on the page since a version: the
// lines of its Markdown removed and added, with the words changed in
// them, and the unchanged ones folded away.
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

// restoreVersion puts an earlier version back, keeping the current one in
// the history first.
func (a *App) restoreVersion(pageID string, c *doc.Content) {
	if a.page != nil && a.page.id == pageID {
		a.savePage()
	}
	if cur := a.st.Content(pageID); cur != nil {
		if err := a.st.AddHistory(pageID, cur, "restore", "", locales.DialogHistoryBeforeRestoring(time.Now())); err != nil {
			a.toast(locales.DialogHistoryRestoreFailed(err.Error()))
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
		a.toast(locales.DialogHistoryRestoreFailed(err.Error()))
		return
	}
	a.toast(locales.DialogHistoryRestored())
}

// Share -----------------------------------------------------------------

func (a *App) openShare(id string) {
	d := &a.dialogs
	d.shareOpen, d.sharePage, d.shareEmail, d.shareRole, d.shareError, d.shareSent = true, id, "", "editor", "", ""
	d.shareMember, _ = a.st.Members(id)
	d.pubEmails, d.pubComments = "", false
	if p := a.pub; p != nil && p.page == id {
		p.mu.Lock()
		d.pubEmails, d.pubComments = strings.Join(p.emails, ", "), p.comments
		p.mu.Unlock()
	}
	a.checkCloudflared()
	if a.backend.SignedIn() {
		go func() {
			a.loadMembers(id)
			a.update(func() { a.shareMembersOf(id) })
		}()
	}
}

// shareMembersOf shows page's members in the Share dialog, while it is
// still the page the dialog is for: an answer for one before it comes
// after the dialog opened for another.
func (a *App) shareMembersOf(page string) {
	if d := &a.dialogs; d.sharePage == page {
		d.shareMember, _ = a.st.Members(page)
	}
}

func (a *App) shareDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	p := a.pageByID(d.sharePage)
	dialog(c, &d.shareOpen, 480, func() {
		// The subtitle is about inviting people, which needs a server.
		subtitle := ""
		if a.hasServer() {
			subtitle = locales.DialogShareSubtitle()
		}
		dialogHeader(c, locales.DialogShareTitle(a.pageTitleOf(d.sharePage)), subtitle, &d.shareOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			// Only a page's owner opens it to the web.
			if a.ownsPage(d.sharePage) {
				a.publicLink(c)
				ui.Box(c).Height(1).FillWidth().Background(pal.Divider)
			}
			switch {
			case !a.hasServer():
				notice(c, "info", "info", locales.DialogShareLocalOnly(), nil)
				return
			case !a.session.SignedIn:
				notice(c, "info", "info", locales.DialogShareSignIn(), func() {
					if primary(c, "signin", "", locales.CommonSignIn()).Clicked() {
						d.shareOpen = false
						a.navigate(Route{Kind: "signin"})
					}
				})
				return
			case p != nil && a.isShared(*p):
				notice(c, "info", "info", locales.DialogShareOwnerOnly(), nil)
			default:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					in := growField(c, "email", &d.shareEmail, locales.DialogShareInvitee())
					selectBox(c, "role", &d.shareRole, []string{"editor", "viewer"}, func(s string) string {
						return shareRoleName(s)
					}, 116)
					invite := func() {
						email := strings.TrimSpace(d.shareEmail)
						if email == "" || d.shareBusy {
							return
						}
						d.shareBusy, d.shareError, d.shareSent = true, "", ""
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
								if d.sharePage != page {
									return
								}
								if err != nil {
									d.shareError = shareFailure(err)
									return
								}
								// The server answers alike whether or not the
								// email has an account, so this can't say which.
								d.shareEmail, d.shareSent = "", email
								a.shareMembersOf(page)
							})
						}()
					}
					if in.Submitted() {
						invite()
					}
					if primary(c, "invite", "", locales.DialogShareInvite()).Disabled(d.shareBusy).Clicked() {
						invite()
					}
				})
				if d.shareError != "" {
					ui.Text(c, d.shareError).FontSize(12.5).TextColor(pal.DangerText)
				} else if d.shareSent != "" {
					ui.Text(c, locales.DialogShareSent(d.shareSent)).FontSize(12.5).TextColor(pal.TextMuted)
				}
			}
			label(c, locales.DialogSharePeople())
			ui.Column(c).Gap(2).Children(func() {
				ui.Row(c).Padding(6, 0).Gap(10).AlignItems(ui.Center).Children(func() {
					avatar(c, a.session.DisplayName, 28, false)
					ui.Text(c, locales.DialogShareYou(a.session.DisplayName)).Grow(1).TextColor(pal.Text)
					ui.Text(c, locales.CommonOwner()).FontSize(12.5).TextColor(pal.TextMuted)
				})
				for _, m := range d.shareMember {
					if m.UserID == a.session.UserID {
						continue
					}
					ui.Row(c.Key(m.UserID)).Padding(6, 0).Gap(10).AlignItems(ui.Center).Children(func() {
						avatar(c, m.Name(), 28, false)
						ui.Column(c).Grow(1).Children(func() {
							ui.Text(c, m.Name()).TextColor(pal.Text)
							if m.Email != "" && m.DisplayName != "" {
								ui.Text(c, m.Email).FontSize(12).TextColor(pal.TextMuted)
							}
						})
						if !m.Accepted && m.Role != "owner" {
							ui.Text(c, locales.DialogShareInvited()).FontSize(12.5).TextColor(pal.TextMuted)
							ui.Text(c, "·").FontSize(12.5).TextColor(pal.ChartMuted)
						}
						ui.Text(c, shareRoleName(m.Role)).FontSize(12.5).TextColor(pal.TextMuted)
						if p != nil && !a.isShared(*p) && m.Role != "owner" {
							remove := false
							tools(c, func() { remove = iconButton(c, "remove", "x", locales.CommonRemove()).Clicked() })
							if remove {
								page, uid := d.sharePage, m.UserID
								go func() {
									ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
									defer cancel()
									if _, err := a.backend.RPC(ctx, "unshare_page", map[string]any{"p_page_id": page, "p_user_id": uid}); err != nil {
										msg := shareFailure(err)
										a.update(func() {
											if d.sharePage == page {
												d.shareError = msg
											}
										})
										return
									}
									a.loadMembers(page)
									a.update(func() { a.shareMembersOf(page) })
								}()
							}
						}
					})
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				if button(c, "copy", "link", locales.DialogShareCopyLink()).Clicked() {
					c.WriteClipboard(doc.PageLink(d.sharePage))
					c.Toast(locales.DialogShareLinkCopied())
				}
			})
		})
	})
}

// shareRoleName says a role of a person a page is shared with.
func shareRoleName(role string) string {
	switch role {
	case "editor":
		return locales.DialogShareCanEdit()
	case "viewer":
		return locales.DialogShareCanView()
	case "owner":
		return locales.CommonOwner()
	}
	return ""
}

// cloudflaredDownloads is Cloudflare's page of cloudflared's installers.
const cloudflaredDownloads = "https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/"

// publicLink is the Share dialog's public link: a switch that opens the
// page to anyone with its link, or to some emails, through cloudflared.
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
			// The QR code opens the link on a phone; it is black on white
			// in either theme, as cameras read best.
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
			// Who may open a link is fixed as it starts: other emails
			// take a new link.
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

// installCloudflared guides the user to install cloudflared: a command to
// run in a terminal, where the system has one, or a prompt to give an AI
// agent that can run commands, as Claude Code or Codex. The dialog looks
// again every few seconds, so the switch shows once it is installed.
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
	b.WriteString(locales.DialogChartLabelColumn())
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
		return nil, nil, errors.New(locales.DialogChartNeedRows())
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
		return nil, nil, errors.New(locales.DialogChartNeedSeries())
	}
	values := make([][]any, len(header)-1)
	for i, line := range lines[1:] {
		cells := split(line)
		labels = append(labels, cells[0])
		for s := range values {
			v := 0.0
			if s+1 < len(cells) && cells[s+1] != "" {
				if v, err = strconv.ParseFloat(cells[s+1], 64); err != nil {
					return nil, nil, errors.New(locales.DialogChartNotNumber(i+2, cells[s+1]))
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
		dialogHeader(c, locales.DialogChartTitle(), locales.DialogChartSubtitle(), &d.chartOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.chartTitle, locales.DialogFieldTitle())
			ui.Segmented(c.Key("kind"), &d.chartKind, locales.DialogChartBar(), locales.DialogChartLine(), locales.DialogChartPie())
			area := ui.TextArea(c.Key("csv"), &d.chartCSV).Height(180).Font("monospace").FontSize(13)
			_ = area
			field(c, "caption", &d.chartCap, locales.DialogChartCaption())
			if d.chartError != "" {
				ui.Text(c, d.chartError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.chartOpen = false
				}
				if primary(c, "save", "", locales.DialogChartSave()).Clicked() {
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
	d.embedOpen, d.embedID, d.embedTitle, d.embedHeight = true, "", locales.DialogEmbedTitle(), "320"
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
		dialogHeader(c, locales.DialogEmbedTitle(), locales.DialogEmbedSubtitle(), &d.embedOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.embedTitle, locales.DialogFieldTitle())
			ui.TextArea(c.Key("html"), &d.embedHTML).Height(240).Font("monospace").FontSize(12.5)
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

// Links ------------------------------------------------------------------

// openLinkDialog asks for the link of a block of a kind: "video",
// "link_preview" or "image", which can be uploaded instead.
func (d *dialogs) openLinkDialog(kind string) {
	d.linkOpen, d.linkKind, d.linkID, d.linkURL, d.linkError = true, kind, "", "", ""
}

// editLink opens the link dialog on an image or video block from the web,
// to change its link.
func (d *dialogs) editLink(n *doc.Node) {
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
			insert := func() {
				link := strings.TrimSpace(d.linkURL)
				valid := doc.WebURL(link)
				if d.linkKind == "video" {
					_, valid = doc.ParseVideoURL(link)
				}
				if !valid {
					d.linkError = locales.DialogLinkNotWeb()
					if d.linkKind == "video" {
						d.linkError = locales.DialogLinkNotVideo()
					}
					return
				}
				switch {
				case a.page == nil:
				case d.linkID != "":
					a.setMediaLink(d.linkID, link)
				case d.linkKind == "video":
					n, _ := doc.VideoNode(link, "")
					a.page.editor.InsertBlocks(n)
				case d.linkKind == "image":
					n, _ := doc.ImageNode(link)
					a.page.editor.InsertBlocks(n)
				default:
					n, _ := doc.LinkNode(link)
					a.page.editor.InsertBlocks(n)
				}
				d.linkOpen = false
			}
			if in.Submitted() {
				insert()
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
				if primary(c, "insert", "", label).Clicked() {
					insert()
				}
			})
		})
	})
}

// setMediaLink points an image or video block of the open page at a new
// link. A video drops its player and the title learned from its old link.
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
	if wv := a.videos[id]; wv != nil {
		wv.Destroy()
		delete(a.videos, id)
	}
}

// shareFailure says why share_page refused an invite, in words for the
// user where the server names the reason.
func shareFailure(err error) string {
	var re *remote.Error
	if errors.As(err, &re) {
		switch re.Message {
		case "invalid_email":
			return locales.DialogShareInvalidEmail()
		case "cannot_share_with_self":
			return locales.DialogShareWithSelf()
		case "inherited_share":
			// Removing someone shared with on a page above, which keeps
			// them.
			return locales.DialogShareInherited()
		}
	}
	return err.Error()
}

// Inline agent prompt ---------------------------------------------------

// inlinePrompt asks an agent to work on a block, from @ or a selection:
// a panel below the text it was asked from, or above it where there is no
// room below.
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
	ui.Overlay(c, func() {
		anchor := ui.Box(c.Key("inline-anchor")).Absolute().Left(at.X).Top(at.Y).Size(at.W, at.H).PassThrough()
		panel := ui.Column(c.Key("inline-prompt")).AttachTo(anchor, ui.AnchorBottomLeft, ui.AnchorTopLeft).Width(at.W).Margin(8, 0, 0).
			Padding(12).Gap(10).Radius(10).Background(pal.AgentSection).Border(1, pal.AccentSoftBorder).Shadow(0, 10, 30, 0, pal.Shadow)
		// A press elsewhere puts away a prompt with nothing in it yet, but
		// not the press on Ask agent that opened it.
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
					markdown.View(c, ip.reply, &a.md).FontSize(13.5)
				})
				// A reply in words can go in the page; edits are there already.
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

// insertReply puts an agent's reply in the page after a block, its
// Markdown read as blocks, as pasting it does.
func (a *App) insertReply(after, reply string) {
	if a.page == nil {
		return
	}
	nodes := editor.MarkdownBlocks(reply)
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

// short is s on one line, cut to n runes.
func short(s string, n int) string {
	rs := []rune(strings.Join(strings.Fields(s), " "))
	if len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return string(rs)
}

// focus is the blocks the prompt is about, in order.
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
			// What it made is in the page, with Undo below it.
			if r.Error == "" && len(r.Ops) > 0 && a.dialogs.inline == ip {
				a.dialogs.inline = nil
			}
		},
	})
	ip.runID = run.ID
}
