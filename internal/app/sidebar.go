package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
	"leaflet/internal/theme"
)

func (a *App) sidebar(c *ui.Context) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	// The window controls sit at the top left.
	ui.Row(c.Key("sidebar-top")).Height(max(bar.Height, 46)).Padding(0, 8, 0, bar.Left+8).Gap(2).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Spacer(c)
		if iconButton(c, "search", "search", "Search (⌘P)").Clicked() {
			a.openSearch()
		}
		if iconButton(c, "new-page", "square-pen", "New page (⌘N)").Clicked() {
			a.newPage("", "")
		}
		if iconButton(c, "hide-sidebar", "panel-left", "Hide sidebar (⌘\\)").Clicked() {
			a.toggleSidebar()
		}
	})
	ui.Scroll(c.Key("sidebar-scroll")).Grow(1).Padding(0, 8, 12).Children(func() {
		a.sectionHeader(c, "My space", "folder-plus", "New folder", func() { a.dialogs.openRenameFolder(nil) })
		ui.Column(c.Key("tree")).Gap(1).Children(func() {
			for _, f := range a.folders {
				if f.SpaceID == a.session.SpaceID {
					a.folderRow(c, f)
				}
			}
			for _, p := range a.children("", "", true) {
				a.pageRow(c, p, 0)
			}
			if len(a.pages) == 0 {
				ui.Text(c, "No pages yet").FontSize(13).TextColor(pal.TextMuted).Padding(6, 10)
			}
		})
		if shared := a.sharedRoots(); len(shared) > 0 {
			a.sectionHeader(c, "Shared with me", "", "", nil)
			ui.Column(c.Key("shared")).Gap(1).Children(func() {
				for _, p := range shared {
					a.pageRow(c, p, 0)
				}
			})
		}
	})
	a.sidebarFooter(c)
}

func (a *App) navRow(c *ui.Context, key, icon, label, hint string, active bool, fn func()) {
	pal := theme.Of(c)
	row := ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 8).Gap(9).Radius(7).AlignItems(ui.Center).TextColor(pal.TextSecondary)
	if active {
		row.Background(pal.NavSelected).TextColor(pal.Text)
	} else if row.Hovered() {
		row.Background(pal.Hover)
		row.TextColor(pal.Text)
	}
	row.Children(func() {
		ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.Icon)
		ui.Text(c, label).FontSize(13.5).Grow(1).SingleLine()
		if hint != "" && row.Hovered() {
			ui.Text(c, hint).FontSize(11.5).TextColor(pal.TextMuted)
		}
	})
	if row.Clicked() {
		fn()
	}
}

func (a *App) sectionHeader(c *ui.Context, title, icon, label string, action func()) {
	pal := theme.Of(c)
	row := ui.Row(c.Key("section:"+title)).Height(28).Padding(0, 4, 0, 10).Margin(10, 0, 2).AlignItems(ui.Center)
	hovered := row.Hovered()
	row.Children(func() {
		ui.Text(c, title).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).Grow(1).LetterSpacing(0.2)
		if action != nil {
			b := iconButton(c, "action", icon, label).Size(22, 22)
			if !hovered {
				b.Opacity(0)
			}
			if b.Clicked() {
				action()
			}
		}
	})
}

func (a *App) folderRow(c *ui.Context, f store.Folder) {
	pal := theme.Of(c)
	open := a.folderOpen[f.ID]
	pages := a.children("", f.ID, true)
	row := ui.ButtonBase(c.Key("folder:"+f.ID)).Height(30).Padding(0, 4, 0, 6).Gap(6).Radius(7).AlignItems(ui.Center).TextColor(pal.TextSecondary)
	hovered := row.Hovered()
	if hovered {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		ui.Icon(c, theme.Icon(chev)).FontSize(12).TextColor(pal.TextMuted)
		icon := "folder"
		if open {
			icon = "folder-open"
		}
		ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.Icon)
		ui.Text(c, f.Name).FontSize(13.5).Grow(1).SingleLine()
		more := iconButton(c, "more", "ellipsis", "Folder actions").Size(22, 22)
		if !hovered {
			more.Opacity(0)
		}
		more.Menu(func(m *ui.Menu) { a.folderMenu(m, f) })
	})
	row.ContextMenu(func(m *ui.Menu) { a.folderMenu(m, f) })
	if row.Clicked() {
		a.folderOpen[f.ID] = !open
	}
	if open {
		ui.Column(c.Key("folder-kids:" + f.ID)).Gap(1).PaddingStart(14).Children(func() {
			for _, p := range pages {
				a.pageRow(c, p, 0)
			}
			for _, file := range a.files {
				if file.FolderID == f.ID {
					a.fileRow(c, file)
				}
			}
			if len(pages) == 0 {
				ui.Text(c, "Empty folder").FontSize(12.5).TextColor(pal.TextMuted).Padding(4, 10)
			}
		})
	}
}

func (a *App) folderMenu(m *ui.Menu, f store.Folder) {
	if m.Item("New page in folder").Chosen() {
		a.folderOpen[f.ID] = true
		a.newPage("", f.ID)
	}
	if m.Item("Upload file…").Chosen() {
		a.uploadFile(f.ID)
	}
	if m.Item("Rename…").Chosen() {
		ff := f
		a.dialogs.openRenameFolder(&ff)
	}
	m.Separator()
	if m.Item("Delete folder").Chosen() {
		a.st.DeleteFolder(f)
		a.toast(fmt.Sprintf("Deleted %s. Its pages moved to the top level.", f.Name))
	}
}

func (a *App) fileRow(c *ui.Context, f store.File) {
	pal := theme.Of(c)
	row := ui.ButtonBase(c.Key("file:"+f.ID)).Height(28).Padding(0, 6, 0, 26).Gap(8).Radius(7).AlignItems(ui.Center).TextColor(pal.TextSecondary)
	if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		ui.Icon(c, theme.Icon("file")).FontSize(14).TextColor(pal.Icon)
		ui.Text(c, f.Name).FontSize(13).Grow(1).SingleLine()
	})
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Open").Chosen() && f.LocalPath != "" {
			a.openFile(f)
		}
		if m.Item("Delete file").Chosen() {
			a.st.DeleteFile(f)
		}
	})
	if row.DoubleClicked() || row.Clicked() {
		a.openFile(f)
	}
}

func (a *App) pageRow(c *ui.Context, p store.Page, depth int) {
	pal := theme.Of(c)
	kids := a.children(p.ID, "", false)
	open := a.expanded[p.ID]
	active := a.route.Kind == "page" && a.route.ID == p.ID
	row := ui.ButtonBase(c.Key("page:"+p.ID)).Height(30).Padding(0, 4, 0, 6+float32(depth)*14).Gap(6).Radius(7).
		AlignItems(ui.Center).TextColor(pal.TextSecondary).Label(p.DisplayTitle())
	hovered := row.Hovered()
	switch {
	case active:
		row.Background(pal.NavSelected).TextColor(pal.Text)
	case hovered:
		row.Background(pal.Hover)
	}
	row.Children(func() {
		if len(kids) > 0 {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			t := ui.ButtonBase(c.Key("toggle")).Size(16, 16).Center().Radius(4).Label("Show subpages")
			if t.Hovered() {
				t.Background(pal.Hover)
			}
			t.Children(func() { ui.Icon(c, theme.Icon(chev)).FontSize(12).TextColor(pal.TextMuted) })
			if t.Clicked() {
				a.expanded[p.ID] = !open
			}
		} else {
			ui.Box(c).Size(16, 16).Center().Children(func() {
				ui.Icon(c, theme.Icon("file-text")).FontSize(13).TextColor(pal.Icon)
			})
		}
		title := ui.Text(c, p.DisplayTitle()).FontSize(13.5).Grow(1).SingleLine()
		if p.Title == "" {
			title.TextColor(pal.TextMuted)
		}
		if active && a.page != nil && a.page.dirty {
			ui.Box(c).Size(6, 6).Radius(3).Background(pal.ChartMuted)
		}
		add := iconButton(c, "add", "plus", "Add a subpage").Size(22, 22)
		more := iconButton(c, "more", "ellipsis", "Page actions").Size(22, 22)
		if !hovered {
			add.Opacity(0)
			more.Opacity(0)
		}
		if add.Clicked() {
			a.newPage(p.ID, "")
		}
		more.Menu(func(m *ui.Menu) { a.pageMenu(m, p) })
	})
	row.ContextMenu(func(m *ui.Menu) { a.pageMenu(m, p) })
	if row.Clicked() {
		a.openPage(p.ID)
	}
	if open {
		for _, k := range kids {
			a.pageRow(c, k, depth+1)
		}
	}
}

// pageMenu is a page's actions, in the sidebar and the page's header.
func (a *App) pageMenu(m *ui.Menu, p store.Page) {
	editable := a.canEdit(p.ID)
	if m.Item("Add subpage").Disabled(!editable).Chosen() {
		a.newPage(p.ID, "")
	}
	if m.Item("Rename").Disabled(!editable).Chosen() {
		a.openPage(p.ID)
		if a.page != nil {
			a.page.titleIn.Focus()
		}
	}
	if m.Item("Move to…").Disabled(!editable).Chosen() {
		a.dialogs.openMove(p.ID)
	}
	m.Separator()
	if m.Item("Page history").Chosen() {
		a.openHistory(p.ID)
	}
	if m.Item("Share…").Chosen() {
		a.openShare(p.ID)
	}
	if m.Item("Export as Markdown…").Chosen() {
		a.exportPage(p.ID)
	}
	if m.Item("Keep on this device").Checked(p.KeepOffline || a.settings.AllPagesOffline).Disabled(a.settings.AllPagesOffline).Chosen() {
		a.st.SetKeepOffline(p.ID, !p.KeepOffline)
	}
	m.Separator()
	if m.Item("Delete").Disabled(!editable).Chosen() {
		a.deletePage(p.ID)
	}
}

func (a *App) sidebarFooter(c *ui.Context) {
	pal := theme.Of(c)
	ui.Row(c.Key("sidebar-footer")).Padding(8, 8, 8, 12).Gap(10).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
		avatar(c, a.session.Initial(), 26, false)
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, a.session.DisplayName).FontSize(13).FontWeight(600).TextColor(pal.Text).SingleLine()
			if status := a.accountStatus(); status != "" {
				ui.Text(c, status).FontSize(11.5).TextColor(pal.TextMuted).SingleLine()
			}
		})
		if !a.session.SignedIn && a.backend.Configured() {
			if ghost(c, "sign-in", "", "Sign in").Clicked() {
				a.navigate(Route{Kind: "signin"})
			}
		}
		settings := iconButton(c, "settings", "settings", "Settings (⌘,)")
		if a.route.Kind == "settings" {
			settings.Background(pal.NavSelected).TextColor(pal.Text)
		}
		if settings.Clicked() {
			a.navigate(Route{Kind: "settings", Section: "account"})
		}
	})
}

// accountStatus is the line under the user's name: where pages are kept,
// or how syncing goes.
func (a *App) accountStatus() string {
	switch {
	case !a.session.SignedIn:
		return "On this device"
	case a.syncing:
		return "Syncing…"
	case !a.online || a.settings.WorkOffline:
		return "Offline"
	case a.pending > 0:
		return fmt.Sprintf("%d %s to sync", a.pending, plural(a.pending, "change", "changes"))
	}
	return "Synced"
}
