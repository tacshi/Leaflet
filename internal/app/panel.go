package app

import (
	"fmt"
	"strings"

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
				b := primary(c, "send", "arrow-up", "").Size(28, 28).Padding(0).Label(locales.PanelSend())
				b.Disabled(strings.TrimSpace(cs.draft) == "" || sess.Running)
				if b.Clicked() {
					send()
				}
			})
		})
	})
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
				if m.Item(o.Name() + " · " + model).Checked(on).Chosen() {
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
}

var replies = map[string]*threadDraft{}

func (a *App) commentsPanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	showResolved := a.dialogs.commentFocus == "resolved"
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
					if showResolved {
						a.dialogs.commentFocus = ""
					} else {
						a.dialogs.commentFocus = "resolved"
					}
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
			in := ui.TextArea(c.Key("text"), &nc.text).Placeholder(locales.PanelCommentPlaceholder()).MinHeight(64).AutoFocus()
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
					ui.TextArea(c.Key("reply"), &draft.text).Placeholder(locales.PanelReplyPlaceholder()).Grow(1).MinHeight(34).
						OnTextKey(func(k ui.TextKey) bool {
							if k.Key != ui.KeyEnter || (k.Mods != 0 && k.Mods != ui.Cmd) {
								return false
							}
							draft.sendNow = true
							return true
						})
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
			ui.Text(c, humanTime(cm.CreatedAt)).FontSize(11.5).TextColor(pal.TextMuted).Grow(1)
			mine := cm.AuthorKind == "user" && (cm.AuthorID == "" || cm.AuthorID == a.session.UserID)
			if mine {
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
		ui.Text(c, cm.Body).FontSize(13.5).TextColor(pal.Text).PaddingStart(28).Selectable()
	})
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
// block; its answer is posted in the thread.
func (a *App) agentInComment(root store.Comment, body string) {
	lower := strings.ToLower(body)
	for _, o := range a.hub.Options() {
		name := "@" + strings.ToLower(o.Name())
		if strings.Contains(lower, name) || strings.Contains(lower, "@agent") {
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
