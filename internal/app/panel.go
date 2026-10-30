package app

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// panel is the side panel of a page: the agent chat, the comments and the
// outline.
func (a *App) panel(c *ui.Context) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	// Only the tabs: the window controls sit at the top right on Windows
	// and Linux, and the page's title bar has the panel's toggle.
	ui.Row(c.Key("panel-top")).Height(max(bar.Height, headerHeight)).Padding(0, bar.Right+panelTopPadR, 0, panelTopPadL).AlignItems(ui.Center).DragWindow().
		BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
		// MyGo's tabs, as high as the bar, so the chosen one's line is on
		// the bar's bottom edge. They keep their width, which panelMinW
		// keeps the panel wide enough for.
		tabs := ui.Tabs(c.Key("panel-tab"), &a.panelTab, locales.CommonAgent(), locales.PanelTabComments(), locales.PanelTabOutline()).FillHeight().AlignItems(ui.Stretch).Shrink(0)
		a.panelTabsW = tabs.Bounds().W
		ui.Spacer(c)
	})
	switch a.panelTab {
	case 0:
		a.agentPanel(c)
	case 1:
		a.commentsPanel(c)
	default:
		a.outlinePanel(c)
	}
}

type chatState struct {
	draft   string
	input   ui.Handle
	end     ui.ScrollState
	sendNow bool // Enter was pressed in the composer
}

var chat = map[string]*chatState{}

func chatOf(pageID string) *chatState {
	s := chat[pageID]
	if s == nil {
		s = &chatState{}
		chat[pageID] = s
	}
	return s
}

func (a *App) agentPanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	sess := a.hub.Session(ps.id)
	cs := chatOf(ps.id)
	options := a.hub.Options()
	if len(options) == 0 {
		ui.Column(c).Grow(1).Padding(24).Gap(12).Justify(ui.Center).Children(func() {
			avatar(c, "", 40, true)
			ui.Text(c, locales.PanelNoProviderTitle()).FontSize(16).FontWeight(650).TextColor(pal.Text)
			ui.Text(c, locales.PanelNoProviderBody()).
				FontSize(13).TextColor(pal.TextMuted)
			if primary(c, "connect", "sparkles", locales.PanelOpenProviders()).Clicked() {
				a.openSettings("providers")
			}
		})
		return
	}
	if cs.end.Y >= cs.end.MaxY-4 {
		cs.end.Y = 1e9
	}
	ui.Scroll(c.Key("chat:" + ps.id)).Grow(1).TrackScroll(&cs.end).Padding(14).Children(func() {
		ui.Column(c).Gap(14).Children(func() {
			if len(sess.Messages) == 0 {
				ui.Column(c).Gap(10).Padding(12, 2).Children(func() {
					ui.Text(c, locales.PanelChatEmpty()).FontSize(13.5).TextColor(pal.TextMuted)
					suggestions := []string{locales.PanelSuggestSummarize(), locales.PanelSuggestStructure(), locales.PanelSuggestGaps(), locales.PanelSuggestChart()}
					for i, s := range suggestions {
						if ghost(c, fmt.Sprintf("suggest:%d", i), "sparkles", s).Border(1, pal.Divider).Justify(ui.Start).Height(32).Clicked() {
							a.hub.Chat(ps.id, s, a.focusBlocks())
						}
					}
				})
			}
			for i, m := range sess.Messages {
				a.chatMessage(c, i, m, sess)
			}
		})
	})
	ui.Column(c.Key("composer")).Padding(10, 12, 12).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			// MyGo's text area. It takes a plain Enter as a newline before
			// any Shortcut sees it; OnTextKey sees it first. Shift+Enter
			// still breaks the line.
			ui.TextArea(c.Key("draft"), &cs.draft).Bind(&cs.input).Placeholder(locales.PanelAskPlaceholder()).MinHeight(64).MaxHeight(180).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key != ui.KeyEnter || k.Mods != 0 {
						return false
					}
					cs.sendNow = true
					return true
				})
			if a.dialogs.focusChat {
				a.dialogs.focusChat = false
				cs.input.Focus()
			}
			send := func() {
				if strings.TrimSpace(cs.draft) == "" || sess.Running {
					return
				}
				a.hub.Chat(ps.id, cs.draft, a.focusBlocks())
				cs.draft = ""
				cs.end.Y = 1e9
			}
			if cs.sendNow {
				cs.sendNow = false
				send()
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				a.agentPicker(c, sess, options)
				tools(c, func() {
					if iconButton(c, "new-chat", "message-square-plus", locales.PanelNewConversation()).Disabled(sess.Running).Clicked() {
						a.hub.NewConversation(ps.id)
					}
					if len(sess.Past) > 0 {
						a.pastConversations(c, sess)
					}
				})
				ui.Spacer(c)
				// While its run works on this device, Send stops it.
				if run := chatRun(sess); run != nil && run.Stoppable() {
					stop := primary(c, "stop", "square-stop", "").Size(28, 28).Padding(0).Label(locales.PageStopRun()).Tooltip(locales.PageStopRun())
					if stop.Clicked() {
						a.hub.Stop(ps.id, run.ID)
					}
					return
				}
				b := primary(c, "send", "arrow-up", "").Size(28, 28).Padding(0).Label(locales.PanelSend())
				b.Disabled(strings.TrimSpace(cs.draft) == "" || sess.Running)
				if b.Clicked() {
					send()
				}
			})
		})
	})
}

// chatRun is the run the chat waits for, or nil.
func chatRun(sess *agents.Session) *agents.Run {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if m := sess.Messages[i]; m.Pending {
			return sess.Run(m.RunID)
		}
	}
	return nil
}

// focusBlocks are the blocks the editor's selection is on.
func (a *App) focusBlocks() []string {
	if a.page == nil || !a.page.editor.Focused() {
		return nil
	}
	return []string{a.page.editor.FocusBlock()}
}

// agentPicker is MyGo's menu button choosing the agent and model the
// panel's chat asks.
func (a *App) agentPicker(c *ui.Context, sess *agents.Session, options []agents.Option) {
	current, err := a.hub.Resolve(sess.Selected)
	name := locales.PanelNoAgent()
	if err == nil {
		name = current.Label()
	}
	ui.MenuButton(c.Key("agent-picker"), name, func(m *ui.Menu) {
		for _, o := range options {
			for _, model := range agents.Models(o.Provider) {
				ch := store.ModelChoice{ProviderID: o.Provider.ID, Model: model}
				on := err == nil && current.Provider.ID == o.Provider.ID && current.Model == model
				if m.Item(o.Name() + " · " + model).Radio(on).Chosen() {
					sess.Selected = &ch
				}
			}
		}
		m.Separator()
		if m.Item(locales.PanelManageProviders()).Chosen() {
			a.openSettings("providers")
		}
	}).Label(locales.CommonAgent()).FontSize(12)
}

// pastConversations is MyGo's menu on an icon button, going back to one
// of the page's earlier conversations.
func (a *App) pastConversations(c *ui.Context, sess *agents.Session) {
	b := iconButton(c, "past-chats", "history", locales.PanelPastConversations()).Disabled(sess.Running)
	b.Menu(func(m *ui.Menu) {
		open := ""
		for _, p := range sess.Past {
			if m.Item(p.Title()).Chosen() {
				open = p.ID
			}
		}
		if open != "" {
			a.hub.OpenConversation(sess.PageID, open)
		}
	})
}

func (a *App) chatMessage(c *ui.Context, i int, m *agents.Message, sess *agents.Session) {
	pal := theme.Of(c)
	ui.Column(c.Key(fmt.Sprintf("msg:%d", i))).Gap(6).Children(func() {
		if m.Mine {
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Text(c, m.Text).FontSize(13.5).TextColor(pal.OnInverse).Padding(8, 12).Radius(12).Background(pal.Inverse).
					MaxWidth(280).Selectable().SelectionColor(pal.OnInverse.Alpha(0.3))
			})
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			avatar(c, "", 20, true)
			name := m.AgentName
			if name == "" {
				name = locales.CommonAgent()
			}
			ui.Text(c, name).FontSize(12.5).FontWeight(600).TextColor(pal.TextSecondary)
		})
		body := ui.Column(c).PaddingStart(28).Gap(8)
		body.Children(func() {
			switch {
			case m.Pending:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13)
					ui.Text(c, m.Text).FontSize(13).TextColor(pal.TextMuted)
				})
			case m.Error:
				ui.Text(c, m.Text).FontSize(13).TextColor(pal.DangerText).Selectable()
			default:
				markdown.View(c, m.Text, &a.md).FontSize(13.5)
			}
			run := sess.Run(m.RunID)
			if run == nil || m.Pending || len(run.Edits) == 0 {
				return
			}
			ui.Column(c).Gap(4).Padding(8, 10).Radius(8).Background(pal.AgentSection).Children(func() {
				for j, e := range run.Edits {
					row := ui.ButtonBase(c.Key(fmt.Sprintf("edit:%d", j))).Gap(6).AlignItems(ui.Center).TextColor(pal.AccentText)
					row.Children(func() {
						ui.Icon(c, theme.Icon("check")).FontSize(12)
						ui.Text(c, e.Summary).FontSize(12.5)
					})
					if row.Clicked() && len(e.BlockIDs) > 0 {
						a.page.editor.PlaceCaret(e.BlockIDs[0], 0)
					}
				}
				ui.Row(c).Gap(6).Margin(4, 0, 0).Children(func() {
					if run.Undone {
						if ghost(c, "redo", "redo-2", locales.PageRedo()).Clicked() {
							a.hub.RedoRun(a.page.id, run.ID)
						}
					} else if ghost(c, "undo", "undo-2", locales.CommonUndo()).Clicked() {
						a.hub.UndoRun(a.page.id, run.ID)
					}
				})
			})
		})
	})
}

// Comments --------------------------------------------------------------

type threadDraft struct {
	text    string
	sendNow bool // Enter was pressed in the reply box
	agents  agentPicker
}

var replies = map[string]*threadDraft{}

func (a *App) commentsPanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	showResolved := a.dialogs.showResolved
	ui.Scroll(c.Key("comments:" + ps.id)).Grow(1).Padding(12).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			if nc := a.dialogs.newComment; nc != nil {
				a.newCommentCard(c, nc)
			}
			open, resolved := 0, 0
			for _, t := range ps.threads {
				if t.Root.Resolved {
					resolved++
				} else {
					open++
				}
			}
			if open == 0 && a.dialogs.newComment == nil {
				// As the home view's empty state is: an icon over centered text.
				ui.Column(c).FillWidth().Padding(48, 16).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.Icon("message-square")).FontSize(26).TextColor(pal.ChartMuted).Margin(0, 0, 4, 0)
					ui.Text(c, locales.PanelCommentsEmpty()).FontSize(14).FontWeight(600).TextColor(pal.Text)
					ui.Text(c, locales.PanelCommentsEmptyHint()).
						FontSize(13).TextColor(pal.TextMuted).TextAlign(ui.Center).MaxWidth(260)
				})
			}
			for _, t := range ps.threads {
				if !t.Root.Resolved || showResolved {
					a.threadCard(c, t)
				}
			}
			if resolved > 0 {
				label := locales.PanelShowResolved(resolved)
				if showResolved {
					label = locales.PanelHideResolved()
				}
				if ghost(c, "toggle-resolved", "", label).Clicked() {
					a.dialogs.showResolved = !showResolved
				}
			}
		})
	})
}

func (a *App) newCommentCard(c *ui.Context, nc *newComment) {
	ps := a.page
	card(c, "new-comment", func() {
		ui.Column(c).Padding(12).Gap(10).Children(func() {
			if nc.quote != "" {
				quoteChip(c, nc.quote)
			}
			in := ui.TextArea(c.Key("text"), &nc.text).Placeholder(locales.PanelCommentPlaceholder()).MinHeight(64).AutoFocus().
				OnTextKey(nc.agents.key)
			a.agentMenu(c, in, &nc.agents, &nc.text)
			post := func() {
				body := strings.TrimSpace(nc.text)
				if body == "" {
					return
				}
				root, err := a.st.AddComment(ps.id, nc.blockID, body, "", nc.quote, "", "")
				if err != nil {
					a.toast(err.Error())
					return
				}
				a.dialogs.newComment = nil
				a.agentInComment(root, body)
			}
			if in.Shortcut(ui.Cmd, ui.KeyEnter) {
				post()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					a.dialogs.newComment = nil
				}
				if primary(c, "post", "", locales.CommonComment()).Disabled(strings.TrimSpace(nc.text) == "").Clicked() {
					post()
				}
			})
		})
	})
}

func quoteChip(c *ui.Context, quote string) {
	pal := theme.Of(c)
	ui.Text(c, locales.PanelQuote(short(quote, 120))).FontSize(12.5).Italic().TextColor(pal.TextMuted).Padding(4, 8).
		BorderWidth(0, 0, 0, 3).BorderColor(pal.WarnBorder).Background(pal.WarnBg).Radius(4)
}

// agentPicker is the @ menu of a comment box: typing @ lists the agents,
// as the page's @ menu does, and choosing one writes its name, which
// agentInComment finds to run it.
type agentPicker struct {
	open   bool
	at     int // where the @ is, in runes
	query  string
	n      int // the rows listed
	index  int
	chosen bool // Enter or Tab was pressed on the highlighted row
	closed int  // the @ whose menu was dismissed, +1; 0 for none
	caret  int  // where to put the caret once a name is written, +1
}

// key takes the keys that move through the open menu, choose a row or
// close it, from the comment box's OnTextKey.
func (p *agentPicker) key(k ui.TextKey) bool {
	if !p.open || k.Mods != 0 {
		return false
	}
	switch k.Key {
	case ui.KeyDown:
		p.index = min(p.index+1, p.n-1)
	case ui.KeyUp:
		p.index = max(p.index-1, 0)
	case ui.KeyEnter, ui.KeyTab:
		p.chosen = true
	case ui.KeyEscape:
		p.open, p.closed = false, p.at+1
	default:
		return false
	}
	return true
}

// mentionAt finds the @ being typed before the caret of in: at the start
// of text or after a space, so an email address opens no menu.
func mentionAt(in ui.Element, text string) (at int, query string, ok bool) {
	start, end := in.TextSelection()
	if start != end || !in.Focused() {
		return 0, "", false
	}
	r := []rune(text)
	caret := min(end, len(r))
	for i := caret - 1; i >= 0 && caret-i <= 25; i-- {
		if unicode.IsSpace(r[i]) {
			return 0, "", false
		}
		if r[i] == '@' {
			if i > 0 && !unicode.IsSpace(r[i-1]) {
				return 0, "", false
			}
			return i, string(r[i+1 : caret]), true
		}
	}
	return 0, "", false
}

// agentMenu shows a comment box's @ menu below it, while an @ is typed.
func (a *App) agentMenu(c *ui.Context, in ui.Element, p *agentPicker, text *string) {
	pal := theme.Of(c)
	if p.caret > 0 {
		in.SetTextSelection(p.caret-1, p.caret-1)
		p.caret = 0
	}
	at, query, ok := mentionAt(in, *text)
	if !ok || p.closed != at+1 {
		p.closed = 0
	}
	var options []agents.Option
	all := a.hub.Options()
	if ok {
		q := strings.ToLower(query)
		for _, o := range all {
			if strings.Contains(strings.ToLower(o.Name()), q) {
				options = append(options, o)
			}
		}
	}
	// With no agents yet, the menu offers to add one, as the page's does.
	connect := ok && len(all) == 0 && query == ""
	p.n = len(options)
	if connect {
		p.n = 1
	}
	p.open = ok && p.closed == 0 && p.n > 0
	if !p.open {
		p.chosen = false
		return
	}
	if at != p.at || query != p.query {
		p.at, p.query, p.index = at, query, 0
	}
	p.index = min(p.index, p.n-1)
	// write puts s in place of the @ and its query, the caret after it.
	write := func(s string) {
		r := []rune(*text)
		end := min(at+1+len([]rune(query)), len(r))
		*text = string(r[:at]) + s + string(r[end:])
		p.caret = at + len([]rune(s)) + 1
		p.open = false
		c.Invalidate()
	}
	choose := func(i int) {
		if connect {
			write("")
			a.openSettings("providers")
			return
		}
		write("@" + options[i].Name() + " ")
	}
	if p.chosen {
		p.chosen = false
		choose(p.index)
		return
	}
	ui.PopoverBase(c, in, &p.open, func(panel ui.Element) {
		panel.Width(max(in.Bounds().W, 240)).Padding(6).Gap(1).
			Radius(10).Background(pal.Surface).Border(1, pal.Border).Shadow(0, 10, 30, 0, pal.Shadow)
		panel.Children(func() {
			ui.Text(c, locales.CommonAgents()).FontSize(11).FontWeight(600).TextColor(pal.TextMuted).Padding(8, 10, 4)
			row := func(i int, key, icon, label, hint string) {
				b := ui.ButtonBase(c.Key(key)).Gap(10).Padding(6, 10).Radius(6).AlignItems(ui.Center).KeepFocus()
				if i == p.index {
					b.Background(pal.NavSelected)
				} else if b.Hovered() {
					b.Background(pal.Hover)
				}
				b.Children(func() {
					ui.Box(c).Size(26, 26).Center().Radius(6).Background(pal.Ground).Border(1, pal.Divider).Children(func() {
						ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.Icon)
					})
					ui.Column(c).Grow(1).Children(func() {
						ui.Text(c, label).TextColor(pal.Text).SingleLine()
						if hint != "" {
							ui.Text(c, hint).FontSize(12).TextColor(pal.TextMuted).SingleLine()
						}
					})
				})
				if b.Clicked() {
					choose(i)
				}
			}
			if connect {
				row(0, "connect", "sparkles", locales.HostAddAgentProvider(), locales.HostAddAgentProviderHint())
				return
			}
			for i, o := range options {
				row(i, "agent:"+o.Provider.ID, "sparkles", o.Name(), o.Model)
			}
		})
	})
	// Pressing outside the menu, or Escape, closes it until another @.
	if !p.open && p.caret == 0 {
		p.closed = at + 1
	}
}

func (a *App) threadCard(c *ui.Context, t store.Thread) {
	pal := theme.Of(c)
	ps := a.page
	draft := replies[t.Root.ID]
	if draft == nil {
		draft = &threadDraft{}
		replies[t.Root.ID] = draft
	}
	focused := a.dialogs.commentFocus == t.Root.BlockID
	k := card(c, "thread:"+t.Root.ID, func() {
		ui.Column(c).Padding(12).Gap(10).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				jump := ui.ButtonBase(c.Key("jump")).Grow(1).Justify(ui.Start)
				jump.Children(func() {
					if t.Root.Quote != "" {
						quoteChip(c, t.Root.Quote)
					} else {
						ui.Text(c, locales.PanelOnBlock()).FontSize(12).TextColor(pal.TextMuted)
					}
				})
				if jump.Clicked() {
					ps.editor.PlaceCaret(t.Root.BlockID, 0)
				}
				tools(c, func() {
					if t.Root.Resolved {
						if iconButton(c, "reopen", "rotate-ccw", locales.PanelReopen()).Clicked() {
							if err := a.st.SetResolved(t.Root.ID, false); err != nil {
								a.toast(locales.PanelCouldNotReopen(err.Error()))
							}
						}
					} else if iconButton(c, "resolve", "check", locales.PanelResolve()).Clicked() {
						if err := a.st.SetResolved(t.Root.ID, true); err != nil {
							a.toast(locales.PanelCouldNotResolve(err.Error()))
						}
					}
				})
			})
			for _, cm := range t.All() {
				a.commentRow(c, cm)
			}
			if !t.Root.Resolved {
				ui.Row(c).Gap(6).AlignItems(ui.End).Children(func() {
					in := ui.TextArea(c.Key("reply"), &draft.text).Placeholder(locales.PanelReplyPlaceholder()).Grow(1).MinHeight(34).
						OnTextKey(func(k ui.TextKey) bool {
							if draft.agents.key(k) {
								return true
							}
							if k.Key != ui.KeyEnter || (k.Mods != 0 && k.Mods != ui.Cmd) {
								return false
							}
							draft.sendNow = true
							return true
						})
					a.agentMenu(c, in, &draft.agents, &draft.text)
					send := func() {
						body := strings.TrimSpace(draft.text)
						if body == "" {
							return
						}
						if _, err := a.st.AddComment(ps.id, t.Root.BlockID, body, t.Root.ID, "", "", ""); err != nil {
							a.toast(err.Error())
							return
						}
						draft.text = ""
						a.agentInComment(t.Root, body)
					}
					if draft.sendNow {
						draft.sendNow = false
						send()
					}
					if strings.TrimSpace(draft.text) != "" {
						tools(c, func() {
							if iconButton(c, "send", "arrow-up", locales.PanelReply()).Clicked() {
								send()
							}
						})
					}
				})
			}
		})
	})
	if focused {
		k.Border(1.5, pal.WarnBorder).ScrollIntoView()
		a.dialogs.commentFocus = ""
	}
}

func (a *App) commentRow(c *ui.Context, cm store.Comment) {
	pal := theme.Of(c)
	ui.Column(c.Key(cm.ID)).Gap(4).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			avatar(c, cm.AuthorName, 20, cm.AuthorKind == "agent")
			ui.Text(c, cm.AuthorName).FontSize(12.5).FontWeight(600).TextColor(pal.Text)
			if asked := a.askedBy(cm); asked != "" {
				ui.Text(c, asked).FontSize(11.5).TextColor(pal.TextMuted)
			}
			if cm.AuthorKind == "guest" {
				ui.Text(c, locales.PanelViaPublicLink()).FontSize(11.5).TextColor(pal.TextMuted)
			}
			ui.Text(c, humanTime(cm.CreatedAt)).FontSize(11.5).TextColor(pal.TextMuted).Grow(1)
			mine := cm.AuthorKind == "user" && (cm.AuthorID == "" || cm.AuthorID == a.session.UserID)
			// A guest's comment came through the owner's public link: the
			// owner may delete it.
			guest := cm.AuthorKind == "guest" && a.page != nil && a.ownsPage(a.page.id)
			if (mine || guest) && cm.DeletedAt.IsZero() {
				tools(c, func() {
					more := iconButton(c, "more", "ellipsis", locales.PanelCommentActions()).Size(20, 20).Padding(2)
					more.Menu(func(m *ui.Menu) {
						if m.Item(locales.PanelDeleteComment()).Chosen() {
							if err := a.st.DeleteComment(cm.ID); err != nil {
								a.toast(locales.PanelCouldNotDelete(err.Error()))
							}
						}
					})
				})
			}
		})
		if !cm.DeletedAt.IsZero() {
			ui.Text(c, commentBody(cm)).FontSize(13.5).TextColor(pal.TextMuted).PaddingStart(28)
			return
		}
		a.commentText(c, cm.Body).FontSize(13.5).PaddingStart(28)
	})
}

// commentText shows a comment's Markdown as the agents' replies show, its
// mentions of agents marked as the editor marks mentions.
func (a *App) commentText(c *ui.Context, body string) ui.Element {
	pal := theme.Of(c)
	if a.commentMD.Theme == nil || a.commentMD.Theme.Mark != pal.AccentSoft {
		a.commentMD.Theme = &markdown.Theme{Mark: pal.AccentSoft}
	}
	names := []string{"agent"}
	for _, o := range a.hub.Options() {
		names = append(names, o.Name())
	}
	return markdown.View(c, markMentions(body, names), &a.commentMD)
}

// markMentions marks each @ of one of names in md, as an @ menu writes
// them, outside code, where they stay as written: fenced code blocks,
// indented ones, and code spans.
func markMentions(md string, names []string) string {
	return scanMentions(md, names, func(m string) string { return "<mark><b>" + m + "</b></mark>" })
}

// mentioned is which of names md mentions where markMentions marks them,
// in lower case.
func mentioned(md string, names []string) map[string]bool {
	out := map[string]bool{}
	scanMentions(md, names, func(m string) string {
		out[strings.ToLower(m[1:])] = true
		return m
	})
	return out
}

// scanMentions is md with each mention markMentions marks as mark has it.
func scanMentions(md string, names []string, mark func(mention string) string) string {
	names = slices.Clone(names)
	slices.SortFunc(names, func(x, y string) int { return len(y) - len(x) })
	var b, para strings.Builder
	// A paragraph's lines are marked together, for a code span may go
	// over more than one of them.
	flush := func() {
		b.WriteString(markInline(para.String(), names, mark))
		para.Reset()
	}
	fence := ""
	// inPara is set while a paragraph goes on: an indented line then
	// goes on with it, not starting a code block.
	inPara := false
	for _, line := range strings.SplitAfter(md, "\n") {
		text := strings.TrimRight(line, "\r\n")
		switch {
		case fence != "":
			b.WriteString(line)
			if closesFence(text, fence) {
				fence = ""
			}
			continue
		case opensFence(text) != "":
			flush()
			fence, inPara = opensFence(text), false
			b.WriteString(line)
			continue
		case strings.TrimSpace(text) == "":
			para.WriteString(line)
			flush()
			inPara = false
			continue
		case !inPara && (strings.HasPrefix(text, "    ") || strings.HasPrefix(text, "\t")):
			flush()
			b.WriteString(line)
			continue
		}
		para.WriteString(line)
		inPara = true
	}
	flush()
	return b.String()
}

// markInline marks the mentions in a paragraph, outside its code spans; a
// backslash keeps the character after it, a backtick too, from starting
// one.
func markInline(s string, names []string, mark func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i++
			continue
		case ch == '`':
			// A span opens with a run of backticks and closes with a run
			// as long; one that never closes is backticks as written.
			n := backticks(s[i:])
			end := i + n
			if j := closingRun(s[end:], n); j >= 0 {
				end += j + n
			}
			b.WriteString(s[i:end])
			i = end - 1
			continue
		case ch == '@' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\n' || s[i-1] == '\t'):
			if n := mentionLen(s[i+1:], names); n > 0 {
				b.WriteString(mark(s[i : i+1+n]))
				i += n
				continue
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// backticks is how many backticks s starts with.
func backticks(s string) int {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	return n
}

// closingRun is where in s a run of exactly n backticks starts, or -1.
func closingRun(s string, n int) int {
	for j := 0; j < len(s); {
		if m := backticks(s[j:]); m > 0 {
			if m == n {
				return j
			}
			j += m
			continue
		}
		j++
	}
	return -1
}

// opensFence is the fence a line opens a fenced code block with, as ```
// or ~~~, or "".
func opensFence(line string) string {
	line = trimIndent(line)
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(line) && line[n] == ch {
			n++
		}
		if n >= 3 {
			return line[:n]
		}
	}
	return ""
}

// closesFence reports whether a line closes the code block fence opened:
// a run of its character at least as long, alone on the line.
func closesFence(line, fence string) bool {
	line = trimIndent(line)
	n := 0
	for n < len(line) && line[n] == fence[0] {
		n++
	}
	return n >= len(fence) && strings.TrimSpace(line[n:]) == ""
}

// trimIndent drops the up to three spaces a fence may be indented by.
func trimIndent(line string) string {
	for i := 0; i < 3 && strings.HasPrefix(line, " "); i++ {
		line = line[1:]
	}
	return line
}

// mentionLen is how long the name of names that rest starts with is, a
// whole word, or 0.
func mentionLen(rest string, names []string) int {
	for _, name := range names {
		n := len(name)
		if n == 0 || len(rest) < n || !strings.EqualFold(rest[:n], name) {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(rest[n:]); !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return n
		}
	}
	return 0
}

// commentBody is a comment's text, or says it was deleted: a thread whose
// first comment was deleted keeps its replies.
func commentBody(cm store.Comment) string {
	if !cm.DeletedAt.IsZero() {
		return locales.PanelCommentDeleted()
	}
	return cm.Body
}

// askedBy names who asked for an agent's comment, when that was someone
// else: anyone who can comment can post one, so it says whose it is.
func (a *App) askedBy(cm store.Comment) string {
	if cm.AuthorKind != "agent" || cm.AuthorID == "" || cm.AuthorID == a.session.UserID {
		return ""
	}
	if a.page != nil {
		for _, m := range a.page.members {
			if m.UserID == cm.AuthorID && m.Name() != "" {
				return locales.PanelAskedBy(m.Name())
			}
		}
	}
	return locales.PanelAskedBySomeone()
}

// agentInComment runs an agent mentioned in a comment on the thread's
// block, where the comment shows it marked; its answer is posted in the
// thread.
func (a *App) agentInComment(root store.Comment, body string) {
	names := []string{"agent"}
	for _, o := range a.hub.Options() {
		names = append(names, o.Name())
	}
	hit := mentioned(body, names)
	for _, o := range a.hub.Options() {
		if hit[strings.ToLower(o.Name())] || hit["agent"] {
			ch := o.Choice()
			a.hub.Start(a.page.id, agents.RunOptions{
				Surface: "comment", Instruction: body, Focus: []string{root.BlockID}, Selection: root.Quote,
				CommentID: root.ID, Agent: &ch,
				Done: func(r *agents.Run) {
					if r.Error != "" {
						a.toast(r.AgentName + ": " + r.Error)
					}
				},
			})
			return
		}
	}
}
