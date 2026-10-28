package app

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	syncsvc "leaflet/internal/sync"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// Invites: pages shared with the user that reach them once they accept
// (docs/protocol.md §6). Sync reads them with each pass; Home lists them.

// inviteNotification starts the ID of an invite's notification, which a
// click on opens Home by.
const inviteNotification = "invite:"

// startInvites follows the invites sync reads, and shows Home when an
// invite's notification is clicked.
func (a *App) startInvites() {
	a.sync.OnInvites = func() {
		a.update(func() {
			a.invites = a.sync.Invites()
			a.notifyInvites()
		})
	}
	mygo.App.OnNotificationClick(func(id string) {
		if !strings.HasPrefix(id, inviteNotification) {
			return
		}
		a.update(func() {
			a.showWindow()
			a.navigate(Route{Kind: "home"})
		})
	})
}

// notifyInvites shows a desktop notification for each invite not shown
// one before.
func (a *App) notifyInvites() {
	if len(a.invites) == 0 || !mygo.NotificationsSupported() {
		return
	}
	for _, inv := range a.invites {
		key, at := "invite."+inv.PageID, inv.At.Format(time.RFC3339Nano)
		if v, ok := a.st.Value(key); ok && v == at {
			continue
		}
		n := mygo.NewNotification(mygo.NotificationOptions{ID: inviteNotification + inv.PageID,
			Title: inviteText(inv), Body: shareRoleName(inv.Role), Group: "invites"})
		if err := n.Show(); err == nil {
			_ = a.st.SetValue(key, at)
		}
	}
}

// inviteText says who shared which page.
func inviteText(inv syncsvc.Invite) string {
	title := inv.Title
	if strings.TrimSpace(title) == "" {
		title = locales.CommonUntitled()
	}
	return locales.ShellInviteFrom(inviterName(inv), title)
}

// inviterName is who shared an invite's page.
func inviterName(inv syncsvc.Invite) string {
	if inv.Inviter == "" {
		return locales.ShellInviteSomeone()
	}
	return inv.Inviter
}

// inviteList is Home's list of the invites, over what it shows.
func (a *App) inviteList(c *ui.Context) {
	if len(a.invites) == 0 {
		return
	}
	pal := theme.Of(c)
	ui.Column(c.Key("invites")).FillWidth().Padding(16, 20, 0).Gap(6).AlignItems(ui.Center).Children(func() {
		for _, inv := range a.invites {
			busy := a.inviteBusy[inv.PageID]
			ui.Row(c.Key(inv.PageID)).FillWidth().MaxWidth(560).Padding(8, 8, 8, 12).Gap(10).Radius(8).
				Border(1, pal.Divider).Background(pal.Surface).AlignItems(ui.Center).Children(func() {
				avatar(c, inviterName(inv), 26, false)
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, inviteText(inv)).FontSize(13.5).TextColor(pal.Text)
					ui.Text(c, shareRoleName(inv.Role)).FontSize(12).TextColor(pal.TextMuted)
				})
				if primary(c, "accept", "", locales.ShellInviteAccept()).Disabled(busy).Clicked() {
					a.answerInvite(inv.PageID, true)
				}
				decline := false
				tools(c, func() {
					decline = iconButton(c, "decline", "x", locales.ShellInviteDecline()).Disabled(busy).Clicked()
				})
				if decline {
					a.answerInvite(inv.PageID, false)
				}
			})
		}
	})
}

// answerInvite accepts or declines an invite off the main thread; an
// accepted page comes with the sync pass that follows.
func (a *App) answerInvite(pageID string, accept bool) {
	if a.inviteBusy[pageID] {
		return
	}
	if a.inviteBusy == nil {
		a.inviteBusy = map[string]bool{}
	}
	a.inviteBusy[pageID] = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		if accept {
			err = a.sync.AcceptInvite(ctx, pageID)
		} else {
			err = a.sync.DeclineInvite(ctx, pageID)
		}
		a.update(func() {
			delete(a.inviteBusy, pageID)
			if err != nil {
				a.toast(locales.ShellInviteFailed(err.Error()))
				return
			}
			a.invites = a.sync.Invites()
		})
	}()
}
