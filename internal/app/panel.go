package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

// panel is the side panel of a page: the agent chat and the comments.
func (a *App) panel(c *ui.Context) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	ui.Row(c.Key("panel-top")).Height(max(bar.Height, 46)).Padding(0, 10, 0, 12).Gap(6).AlignItems(ui.Center).DragWindow().
		BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
		segmented(c, "panel-tab", &a.panelTab, "Agent", "Comments")
		ui.Spacer(c)
		if a.panelTab == 0 {
			if iconButton(c, "new-chat", "square-pen", "New conversation").Clicked() {
				a.hub.Session(a.page.id).NewConversation()
			}
		}
		if iconButton(c, "hide-panel", "panel-right", "Hide panel (⌘J)").Clicked() {
			a.panelOpen = false
		}
	})
	if a.panelTab == 0 {
		a.agentPanel(c)
	} else {
		a.commentsPanel(c)
	}
}

type chatState struct {
	draft string
	input ui.Handle
	end   ui.ScrollState
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
			ui.Text(c, "Add an agent provider").FontSize(16).FontWeight(650).TextColor(pal.Text)
			ui.Text(c, "Agents run on a service you choose: a cloud one such as OpenAI or Claude, or a model server on your machine, such as Ollama.").
				FontSize(13).TextColor(pal.TextMuted)
			if primary(c, "connect", "sparkles", "Open Settings › Agent providers").Clicked() {
				a.navigate(Route{Kind: "settings", Section: "providers"})
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
					ui.Text(c, "Ask about this page, or have an agent write in it.").FontSize(13.5).TextColor(pal.TextMuted)
					for _, s := range []string{"Summarize this page", "Suggest a structure for this page", "Find gaps or open questions", "Turn the numbers here into a chart"} {
						if ghost(c, "suggest:"+s, "sparkles", s).Border(1, pal.Divider).Justify(ui.Start).Height(32).Clicked() {
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
		box := ui.Column(c).Padding(8, 10).Gap(6).Radius(10).Border(1, pal.Border).Background(pal.Surface)
		box.Children(func() {
			in := ui.TextAreaBase(c.Key("draft"), &cs.draft).Bind(&cs.input).Placeholder("Ask the agent…").FontSize(14).MinHeight(40).MaxHeight(160)
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
			if in.Shortcut(0, ui.KeyEnter) {
				send()
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				a.agentPicker(c, sess, options)
				ui.Spacer(c)
				b := primary(c, "send", "arrow-up", "").Size(28, 28).Padding(0).Label("Send")
				b.Disabled(strings.TrimSpace(cs.draft) == "" || sess.Running)
				if b.Clicked() {
					send()
				}
			})
		})
		if in := in(box); in {
			box.Border(1.5, pal.Accent)
		}
	})
}

func in(e ui.Element) bool { return e.FocusWithin() }

// focusBlocks are the blocks the editor's selection is on.
func (a *App) focusBlocks() []string {
	if a.page == nil || !a.page.editor.Focused() {
		return nil
	}
	return []string{a.page.editor.FocusBlock()}
}

func (a *App) agentPicker(c *ui.Context, sess *agents.Session, options []agents.Option) {
	pal := theme.Of(c)
	current, err := a.hub.Resolve(sess.Selected)
	name := "No agent"
	if err == nil {
		name = current.Label()
	}
	b := ui.ButtonBase(c.Key("agent-picker")).Height(26).Padding(0, 8).Gap(6).Radius(7).AlignItems(ui.Center).TextColor(pal.TextSecondary)
	if b.Hovered() {
		b.Background(pal.Hover)
	}
	b.Children(func() {
		ui.Icon(c, theme.Icon("sparkles")).FontSize(12).TextColor(pal.Accent)
		ui.Text(c, name).FontSize(12).SingleLine().MaxWidth(200)
		ui.Icon(c, theme.Icon("chevron-down")).FontSize(11)
	})
	b.Menu(func(m *ui.Menu) {
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
		if m.Item("Manage agent providers…").Chosen() {
			a.navigate(Route{Kind: "settings", Section: "providers"})
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
				name = "Agent"
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
						if ghost(c, "redo", "redo-2", "Redo").Clicked() {
							a.hub.RedoRun(a.page.id, run.ID)
						}
					} else if ghost(c, "undo", "undo-2", "Undo run").Clicked() {
						a.hub.UndoRun(a.page.id, run.ID)
					}
				})
			})
		})
	})
}

// Comments --------------------------------------------------------------

type threadDraft struct{ text string }

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
				ui.Column(c).Padding(20, 6).Gap(8).Children(func() {
					ui.Text(c, "No open comments").FontSize(14).FontWeight(600).TextColor(pal.Text)
					ui.Text(c, "Select text in the page and choose Comment. Write @ and an agent's name in a reply to have it answer.").
						FontSize(13).TextColor(pal.TextMuted)
				})
			}
			for _, t := range ps.threads {
				if !t.Root.Resolved || showResolved {
					a.threadCard(c, t)
				}
			}
			if resolved > 0 {
				label := fmt.Sprintf("Show %d resolved", resolved)
				if showResolved {
					label = "Hide resolved"
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
	pal := theme.Of(c)
	ps := a.page
	card(c, "new-comment", func() {
		ui.Column(c).Padding(12).Gap(10).Children(func() {
			if nc.quote != "" {
				quoteChip(c, nc.quote)
			}
			in := ui.TextAreaBase(c.Key("text"), &nc.text).Placeholder("Add a comment… (@ an agent to ask it)").FontSize(13.5).MinHeight(56).AutoFocus()
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
				if button(c, "cancel", "", "Cancel").Clicked() {
					a.dialogs.newComment = nil
				}
				if primary(c, "post", "", "Comment").Disabled(strings.TrimSpace(nc.text) == "").Clicked() {
					post()
				}
			})
		})
	}).Border(1.5, pal.Accent)
}

func quoteChip(c *ui.Context, quote string) {
	pal := theme.Of(c)
	ui.Text(c, "“"+short(quote, 120)+"”").FontSize(12.5).Italic().TextColor(pal.TextMuted).Padding(4, 8).
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
						ui.Text(c, "On a block").FontSize(12).TextColor(pal.TextMuted)
					}
				})
				if jump.Clicked() {
					ps.editor.PlaceCaret(t.Root.BlockID, 0)
				}
				if t.Root.Resolved {
					if iconButton(c, "reopen", "rotate-ccw", "Reopen").Clicked() {
						a.st.SetResolved(t.Root.ID, false)
					}
				} else if iconButton(c, "resolve", "check", "Resolve").Clicked() {
					a.st.SetResolved(t.Root.ID, true)
				}
			})
			for _, cm := range t.All() {
				a.commentRow(c, cm)
			}
			if !t.Root.Resolved {
				ui.Row(c).Gap(6).AlignItems(ui.End).Children(func() {
					in := ui.TextAreaBase(c.Key("reply"), &draft.text).Placeholder("Reply…").FontSize(13).Grow(1).MinHeight(30).
						Padding(6, 8).Radius(7).Border(1, pal.Divider)
					send := func() {
						body := strings.TrimSpace(draft.text)
						if body == "" {
							return
						}
						a.st.AddComment(ps.id, t.Root.BlockID, body, t.Root.ID, "", "", "")
						draft.text = ""
						a.agentInComment(t.Root, body)
					}
					if in.Shortcut(ui.Cmd, ui.KeyEnter) || in.Shortcut(0, ui.KeyEnter) {
						send()
					}
					if strings.TrimSpace(draft.text) != "" && iconButton(c, "send", "arrow-up", "Reply").Clicked() {
						send()
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
			avatar(c, strings.ToUpper(firstRune(cm.AuthorName)), 20, cm.AuthorKind == "agent")
			ui.Text(c, cm.AuthorName).FontSize(12.5).FontWeight(600).TextColor(pal.Text)
			ui.Text(c, humanTime(cm.CreatedAt)).FontSize(11.5).TextColor(pal.TextMuted).Grow(1)
			mine := cm.AuthorKind == "user" && (cm.AuthorID == "" || cm.AuthorID == a.session.UserID)
			if mine {
				more := iconButton(c, "more", "ellipsis", "Comment actions").Size(20, 20)
				more.Menu(func(m *ui.Menu) {
					if m.Item("Delete comment").Chosen() {
						a.st.DeleteComment(cm.ID)
					}
				})
			}
		})
		ui.Text(c, cm.Body).FontSize(13.5).TextColor(pal.Text).PaddingStart(28).Selectable()
	})
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
