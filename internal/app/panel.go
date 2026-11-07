package app

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/agents"
	"leaflet/internal/core"
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
		// With no page open, its Agent tab alone: the workspace agent's.
		var tabs ui.Element
		if a.pageOpen() {
			tabs = ui.Tabs(c.Key("panel-tab"), &a.panelTab, locales.CommonAgent(), locales.PanelTabComments(), locales.PanelTabOutline())
		} else {
			agent := 0
			tabs = ui.Tabs(c.Key("panel-tab"), &agent, locales.CommonAgent())
		}
		tabs.FillHeight().AlignItems(ui.Stretch).Shrink(0)
		a.panelTabsW = tabs.Bounds().W
		ui.Spacer(c)
	})
	if !a.pageOpen() {
		a.workspacePanel(c)
		return
	}
	switch a.panelTab {
	case 0:
		a.agentTab(c)
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

// pageDrafts is what was typed on a page and not sent yet: in its chat,
// and in its threads' reply boxes, by thread.
type pageDrafts struct {
	chat    chatState
	replies map[string]*threadDraft
}

// draftsOf is what was typed on a page and not sent yet.
func (a *App) draftsOf(pageID string) *pageDrafts {
	d := a.drafts[pageID]
	if d == nil {
		if a.drafts == nil {
			a.drafts = map[string]*pageDrafts{}
		}
		d = &pageDrafts{replies: map[string]*threadDraft{}}
		a.drafts[pageID] = d
	}
	return d
}

// sends reports whether a key sends what a box holds, a chat's message, a
// comment or a reply: Enter, or Cmd+Enter; Shift+Enter breaks the line.
func sends(k ui.TextKey) bool {
	return k.Key == ui.KeyEnter && (k.Mods == 0 || k.Mods == ui.Cmd)
}

func (a *App) agentPanel(c *ui.Context) {
	ps := a.page
	a.chatView(c, a.hub.Session(ps.id), &a.draftsOf(ps.id).chat, chatOptions{
		empty: locales.PanelChatEmpty(), placeholder: locales.PanelAskPlaceholder(),
		suggestions: []string{locales.PanelSuggestSummarize(), locales.PanelSuggestStructure(), locales.PanelSuggestGaps(), locales.PanelSuggestChart()},
		send:        func(text string) { a.hub.Chat(ps.id, text, a.focusBlocks()) },
		focus:       &a.dialogs.focusChat,
		scope:       true,
	})
}

// chatOptions are what a chat view says and does: the page panel's chat
// and the workspace agent's.
type chatOptions struct {
	empty, placeholder string
	suggestions        []string
	// send asks the session's agent text.
	send func(text string)
	// focus, set, has the composer take the keyboard once.
	focus *bool
	// scope shows the choice of what the chat asks about beside a page:
	// the page, or all the user's pages (scopePicker).
	scope bool
}

// chatView is a session's chat: its messages, or what to ask while it has
// none, and the composer, with the agent to ask, a new conversation, the
// earlier ones, and Send or Stop. With no provider, it says to add one.
func (a *App) chatView(c *ui.Context, sess *agents.Session, cs *chatState, o chatOptions) {
	pal := theme.Of(c)
	options := a.hub.Options()
	if len(options) == 0 {
		ui.Column(c).Grow(1).Padding(24).Gap(12).Justify(ui.Center).Children(func() {
			avatar(c, "", 40, true)
			ui.Text(c, locales.PanelNoProviderTitle()).FontSize(16).FontWeight(650).TextColor(pal.Text)
			ui.Text(c, locales.PanelNoProviderBody()).
				FontSize(13).TextColor(pal.TextMuted).Pretty()
			if primary(c, "connect", "sparkles", locales.PanelOpenProviders()).Clicked() {
				a.openSettings("providers")
			}
		})
		return
	}
	if cs.end.Y >= cs.end.MaxY-4 {
		cs.end.Y = 1e9
	}
	// Each chat's own widgets, its composer's text and undo among them:
	// the page's and the workspace agent's take turns in one place.
	ui.Column(c.Key("chat-view:" + sess.PageID)).Grow(1).Children(func() { a.chatBody(c, sess, cs, o, options) })
}

// chatBody is chatView's messages and composer.
func (a *App) chatBody(c *ui.Context, sess *agents.Session, cs *chatState, o chatOptions, options []agents.Option) {
	pal := theme.Of(c)
	ui.Scroll(c.Key("chat:" + sess.PageID)).Grow(1).TrackScroll(&cs.end).Padding(14).Children(func() {
		ui.Column(c).Gap(14).Children(func() {
			if len(sess.Messages) == 0 {
				ui.Column(c).Gap(10).Padding(12, 2).Children(func() {
					ui.Text(c, o.empty).FontSize(13.5).TextColor(pal.TextMuted)
					for i, s := range o.suggestions {
						if ghost(c, fmt.Sprintf("suggest:%d", i), "sparkles", s).Border(1, pal.Divider).Justify(ui.Start).Height(32).Clicked() {
							o.send(s)
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
			ui.TextArea(c.Key("draft"), &cs.draft).Bind(&cs.input).Placeholder(o.placeholder).MinHeight(64).MaxHeight(180).
				OnTextKey(func(k ui.TextKey) bool {
					if !sends(k) {
						return false
					}
					cs.sendNow = true
					return true
				})
			if o.focus != nil && *o.focus {
				*o.focus = false
				cs.input.Focus()
			}
			send := func() {
				if strings.TrimSpace(cs.draft) == "" || sess.Running {
					return
				}
				o.send(cs.draft)
				cs.draft = ""
				cs.end.Y = 1e9
			}
			if cs.sendNow {
				cs.sendNow = false
				send()
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if o.scope {
					a.scopePicker(c)
				}
				a.agentPicker(c, sess, options)
				tools(c, func() {
					if iconButton(c, "new-chat", "message-square-plus", locales.PanelNewConversation()).Disabled(sess.Running).Clicked() {
						a.hub.NewConversation(sess.PageID)
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
						a.hub.Stop(sess.PageID, run.ID)
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

// focusBlocks are the blocks the editor's selection is on, as the user
// left it: the focus is in the composer as a chat is sent. A page whose
// caret the user hasn't placed has none.
func (a *App) focusBlocks() []string {
	if a.page == nil || !a.page.editor.Focused() && !a.page.caretSet {
		return nil
	}
	return []string{a.page.editor.FocusBlock()}
}

// agentPicker is the menu choosing the agent the chat asks, named alone:
// each runs the model Settings › Agent providers chose for it, which its
// item says.
func (a *App) agentPicker(c *ui.Context, sess *agents.Session, options []agents.Option) {
	current, err := a.hub.Resolve(sess.Selected)
	name := locales.PanelNoAgent()
	if err == nil {
		name = current.Name()
	}
	menuChip(c, "agent-picker", "", name, locales.CommonAgent(), func(m *ui.Menu) {
		for _, o := range options {
			if m.Item(o.Label()).Radio(err == nil && current.Provider.ID == o.Provider.ID).Chosen() {
				sess.Selected = &store.ModelChoice{ProviderID: o.Provider.ID}
			}
		}
		m.Separator()
		if m.Item(locales.PanelManageProviders()).Chosen() {
			a.openSettings("providers")
		}
	})
}

// scopePicker is the choice, beside a page, of what the Agent tab's chat
// asks about: this page, of its agent, or all the user's pages, of the
// workspace agent. The composer takes the keyboard again once chosen.
func (a *App) scopePicker(c *ui.Context) {
	icon, label := "file-text", locales.PanelScopePage()
	if a.agentScope == 1 {
		icon, label = "layout-grid", locales.PanelScopeAll()
	}
	menuChip(c, "agent-scope", icon, label, locales.PanelScope(), func(m *ui.Menu) {
		for i, choice := range []string{locales.PanelScopePage(), locales.PanelScopeAll()} {
			if m.Item(choice).Radio(a.agentScope == i).Chosen() {
				a.agentScope, a.dialogs.focusChat = i, true
			}
		}
	}).Shrink(0)
}

// menuChip is a small button of the composer's row opening a menu: an
// icon, if any, its label and an arrow. Unlike MyGo's MenuButton, it gives
// way where the row has no room, its label cut short.
func menuChip(c *ui.Context, key, icon, label, name string, build func(m *ui.Menu)) ui.Element {
	b := ui.Button(c.Key(key), "").Label(name).Tooltip(label).Padding(4, 7).Shrink(1).MinWidth(0)
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(func() {
			if icon != "" {
				ui.Icon(c, theme.Icon(icon)).FontSize(13).Shrink(0)
			}
			ui.Text(c, label).FontSize(12).SingleLine().Ellipsis("…").Shrink(1)
			ui.Icon(c, theme.Icon("chevron-down")).FontSize(11).Shrink(0)
		})
	})
	b.Menu(build)
	return b
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
			case m.Streamed:
				markdown.View(c, m.Text, &a.md).FontSize(13.5)
				ui.Spinner(c).Size(11, 11)
			case m.Pending:
				shimmer(c, m.Text, 13)
			case m.Error:
				ui.Text(c, m.Text).FontSize(13).TextColor(pal.DangerText).Selectable()
			default:
				markdown.View(c, m.Text, &a.md).FontSize(13.5)
			}
			run := sess.Run(m.RunID)
			if run == nil || m.Pending || len(run.Edits) == 0 {
				return
			}
			a.runEdits(c, sess.PageID, run)
		})
	})
}

// runEdits is the card of what a run changed, under its reply: an edit a
// line, which a click goes to, and beside the first Undo, or Redo once
// undone, as one step for them all. Undone edits show struck through.
func (a *App) runEdits(c *ui.Context, sessID string, run *agents.Run) {
	pal := theme.Of(c)
	ui.Row(c).FillWidth().Gap(4).Padding(4).Radius(8).Background(pal.AgentSection).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			for j, e := range run.Edits {
				row := ui.ButtonBase(c.Key(fmt.Sprintf("edit:%d", j))).FillWidth().Height(26).Padding(0, 8).Gap(6).Radius(6).
					Justify(ui.Start).AlignItems(ui.Center).TextColor(pal.AccentText).Label(e.Summary)
				// The blocks of an undone edit are gone, or as they were, and
				// the page it made with them. A page made that has not come
				// yet goes nowhere.
				opens := !run.Undone && e.PageID != "" && a.pageByID(e.PageID) != nil
				goes := opens || !run.Undone && len(e.BlockIDs) > 0 && a.page != nil
				switch {
				case run.Undone:
					row.TextColor(pal.TextMuted)
				case goes && row.Hovered():
					row.Background(pal.Accent.Alpha(0.08)).Cursor(ui.CursorPointer)
				}
				row.Children(func() {
					ui.Icon(c, theme.Icon("check")).FontSize(12)
					t := ui.Text(c, e.Summary).FontSize(12.5).SingleLine().Ellipsis("…").Shrink(1)
					if run.Undone {
						t.Strikethrough()
					}
				})
				switch {
				case opens && row.Clicked():
					if sessID == agents.WorkspaceID {
						a.fromWorkspace()
					}
					a.openPage(e.PageID)
				case goes && row.Clicked():
					a.page.editor.PlaceCaret(e.BlockIDs[0], 0)
				}
			}
		})
		label, icon := locales.CommonUndo(), "undo-2"
		if run.Undone {
			label, icon = locales.PageRedo(), "redo-2"
		}
		b := ui.ButtonBase(c.Key("undo")).Height(26).Padding(0, 8).Gap(5).Radius(6).TextColor(pal.TextSecondary).Label(label)
		if b.Hovered() {
			b.Background(pal.Accent.Alpha(0.08)).TextColor(pal.Text)
		}
		b.Children(func() {
			ui.Icon(c, theme.Icon(icon)).FontSize(13)
			ui.Text(c, label).FontSize(12.5).FontWeight(500)
		})
		if b.Clicked() {
			if run.Undone {
				a.redoRun(sessID, run)
			} else {
				a.undoRun(sessID, run)
			}
		}
	})
}

// Comments --------------------------------------------------------------

type threadDraft struct {
	text    string
	sendNow bool // Enter was pressed in the reply box
	agents  agentPicker
}

// commentLit is how long the threads of a block asked for stay lit in
// the comments panel, from when they show.
const commentLit = 1500 * time.Millisecond

// showComments shows a block's threads in the comments panel: lit for a
// moment, the first of them brought into view.
func (a *App) showComments(blockID string) {
	a.panelOpen, a.panelTab = true, 1
	d := &a.dialogs
	d.commentFocus, d.commentAt, d.commentShown = blockID, time.Time{}, false
}

// litComments is the block whose threads show lit, or "": the one asked
// for, for commentLit from the frame its threads first showed in.
func (a *App) litComments(c *ui.Context) string {
	d := &a.dialogs
	if d.commentFocus == "" {
		return ""
	}
	if d.commentAt.IsZero() {
		d.commentAt = c.Now()
	}
	left := commentLit - c.Now().Sub(d.commentAt)
	if left <= 0 {
		d.commentFocus = ""
		return ""
	}
	c.After(left)
	return d.commentFocus
}

func (a *App) commentsPanel(c *ui.Context) {
	pal := theme.Of(c)
	ps := a.page
	showResolved := a.dialogs.showResolved
	lit := a.litComments(c)
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
						FontSize(13).TextColor(pal.TextMuted).TextAlign(ui.Center).MaxWidth(260).Balance()
				})
			}
			for _, t := range ps.threads {
				if !t.Root.Resolved || showResolved {
					a.threadCard(c, t, lit != "" && t.Root.BlockID == lit)
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
			// Enter posts, as it sends a reply or a chat.
			in := ui.TextArea(c.Key("text"), &nc.text).Placeholder(locales.PanelCommentPlaceholder()).MinHeight(64).AutoFocus().
				OnTextKey(func(k ui.TextKey) bool {
					if nc.agents.key(k) {
						return true
					}
					if !sends(k) {
						return false
					}
					nc.postNow = true
					return true
				})
			a.agentMenu(c, in, &nc.agents, &nc.text)
			post := func() {
				body := strings.TrimSpace(nc.text)
				if body == "" {
					return
				}
				root, err := a.st.AddComment(ps.id, nc.blockID, body, "", nc.quote, "", "")
				if err != nil {
					a.toast(locales.PanelCouldNotPost(err.Error()))
					return
				}
				a.dialogs.newComment = nil
				a.agentInComment(root, body)
			}
			if nc.postNow {
				nc.postNow = false
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

// threadCard is a thread of the open page's, lit when asked for.
func (a *App) threadCard(c *ui.Context, t store.Thread, lit bool) {
	pal := theme.Of(c)
	ps := a.page
	replies := a.draftsOf(ps.id).replies
	draft := replies[t.Root.ID]
	if draft == nil {
		draft = &threadDraft{}
		replies[t.Root.ID] = draft
	}
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
				// Who may change the thread, as the server allows: its
				// author, or someone who may edit the page. Another's
				// resolve would never go up.
				root := t.Root
				mayResolve := a.canEdit(ps.id) || root.AuthorKind == "user" && (root.AuthorID == "" || root.AuthorID == a.session.UserID)
				tools(c, func() {
					if !mayResolve {
						return
					}
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
							if !sends(k) {
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
							a.toast(locales.PanelCouldNotPost(err.Error()))
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
	if lit {
		k.Border(1.5, pal.WarnBorder)
		// The first of the block's threads comes into view, once.
		if !a.dialogs.commentShown {
			a.dialogs.commentShown = true
			k.ScrollIntoView()
		}
	}
}

func (a *App) commentRow(c *ui.Context, cm store.Comment) {
	pal := theme.Of(c)
	ui.Column(c.Key(cm.ID)).Gap(4).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			avatar(c, cm.AuthorName, 20, cm.AuthorKind == "agent")
			// Short of room, the name gives way before the time, which keeps
			// its line.
			ui.Text(c, cm.AuthorName).FontSize(12.5).FontWeight(600).TextColor(pal.Text).SingleLine().MinWidth(0).Tooltip(cm.AuthorName)
			if asked := a.askedBy(cm); asked != "" {
				ui.Text(c, asked).FontSize(11.5).TextColor(pal.TextMuted).SingleLine().MinWidth(0)
			}
			if cm.AuthorKind == "guest" {
				ui.Text(c, locales.PanelViaPublicLink()).FontSize(11.5).TextColor(pal.TextMuted).SingleLine().MinWidth(0)
			}
			ui.Text(c, humanTime(cm.CreatedAt)).FontSize(11.5).TextColor(pal.TextMuted).SingleLine().Shrink(0)
			ui.Spacer(c)
			mine := cm.AuthorKind == "user" && (cm.AuthorID == "" || cm.AuthorID == a.session.UserID)
			// A guest's comment came through the owner's public link: the
			// owner may delete it.
			guest := cm.AuthorKind == "guest" && a.page != nil && a.ownsPage(a.page.id)
			if (mine || guest) && cm.DeletedAt.IsZero() {
				tools(c, func() {
					more := iconButton(c, "more", "ellipsis", locales.PanelCommentActions()).Size(20, 20).Padding(2)
					more.PopoverMenuButton(func(m *ui.Menu) {
						if m.Item(locales.PanelDeleteComment()).Danger().Chosen() {
							if err := a.st.DeleteComment(cm.ID); err != nil {
								a.toast(locales.PanelCouldNotDelete(err.Error()))
							}
						}
					}, endMenu)
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
	// Marked as bold in the accent, as the editor marks mentions.
	md := core.MarkMentions(body, names, func(m string) string { return "<mark><b>" + m + "</b></mark>" })
	return markdown.View(c, md, &a.commentMD)
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
// block, where the comment shows it marked: one named, or the default for
// @agent. Its answer is posted in the thread.
func (a *App) agentInComment(root store.Comment, body string) {
	agent, mentioned, err := core.CommentAgent(a.hub, body)
	if err != nil {
		a.toast(err.Error())
		return
	}
	if !mentioned {
		return
	}
	a.hub.Start(a.page.id, agents.RunOptions{
		Surface: "comment", Instruction: body, Focus: []string{root.BlockID}, Selection: root.Quote,
		CommentID: root.ID, Agent: &agent,
		Done: func(r *agents.Run) {
			if r.Error != "" {
				a.toast(locales.PanelAgentFailed(r.AgentName, r.Error))
			}
		},
	})
}
