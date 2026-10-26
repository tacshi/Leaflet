package app

import (
	"fmt"
	"strings"

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
		tools(c, func() {
			if iconButton(c, "search", "search", "Search (⌘P)").Clicked() {
				a.openSearch()
			}
			todos := iconButton(c, "todos", "square-check", "To-dos")
			if a.route.Kind == "todos" {
				todos.Background(pal.NavSelected).TextColor(pal.Text)
			}
			if todos.Clicked() {
				a.navigate(Route{Kind: "todos"})
			}
			if iconButton(c, "new-page", "square-pen", "New page (⌘N)").Clicked() {
				a.newPage("", "")
			}
			if iconButton(c, "hide-sidebar", "panel-left", "Hide sidebar (⌘\\)").Clicked() {
				a.toggleSidebar()
			}
		})
	})
	a.tree(c)
	if len(a.pages) == 0 {
		ui.Text(c, "No pages yet").FontSize(13).TextColor(pal.TextMuted).Padding(0, 18, 12)
	}
	a.sidebarFooter(c)
}

func (a *App) sectionHeader(c *ui.Context, title, icon, label string, action func()) {
	pal := theme.Of(c)
	// In the tree, a header starts where the rows' arrows do: back over
	// the slot the Outline keeps for an arrow, and the gap after it.
	t := c.Theme()
	row := ui.Row(c.Key("section:"+title)).Height(28).Padding(0, 4, 0, 0).Margin(10, 0, 2).
		MarginStart(-(t.Space(4) + t.Space(1))).Grow(1).AlignItems(ui.Center)
	hovered := row.Hovered()
	row.Children(func() {
		ui.Text(c, title).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).Grow(1).LetterSpacing(0.2)
		if action != nil {
			tools(c, func() {
				b := iconButton(c, "action", icon, label).Size(22, 22).Padding(3)
				if !hovered {
					b.Opacity(0)
				}
				if b.Clicked() {
					action()
				}
			})
		}
	})
}

func (a *App) folderMenu(m *ui.Menu, f store.Folder) {
	if m.Item("New page in folder").Chosen() {
		a.treeOpen("folder", f.ID)
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
		if err := a.st.DeleteFolder(f); err != nil {
			a.toast("Could not delete: " + err.Error())
			return
		}
		a.toast(fmt.Sprintf("Deleted %s. Its pages moved to the top level.", f.Name))
	}
}

// The sidebar's tree is MyGo's Outline. Its items are keys: "section:" a
// section's header, "folder:", "page:" or "file:" and the ID.

// treeOpen opens an item of the tree, as a page's ancestors to show it.
func (a *App) treeOpen(kind, id string) { a.treeState.Open.Add(kind + ":" + id) }

// tree builds the sidebar's tree: My space's folders, with their pages
// and files, and its pages, then the pages shared with the user, pages
// with their subpages. The open page is its row chosen; choosing a page
// opens it, a file opens the file, and a folder opens or closes.
func (a *App) tree(c *ui.Context) {
	roots := []string{"section:mine"}
	for _, f := range a.folders {
		if f.SpaceID == a.session.SpaceID {
			roots = append(roots, "folder:"+f.ID)
		}
	}
	for _, p := range a.children("", "", true) {
		roots = append(roots, "page:"+p.ID)
	}
	if shared := a.sharedRoots(); len(shared) > 0 {
		roots = append(roots, "section:shared")
		for _, p := range shared {
			roots = append(roots, "page:"+p.ID)
		}
	}
	children := func(key string) []string {
		kind, id, _ := strings.Cut(key, ":")
		switch kind {
		case "folder":
			out := []string{} // a folder opens, empty or not
			for _, p := range a.children("", id, true) {
				out = append(out, "page:"+p.ID)
			}
			for _, f := range a.files {
				if f.FolderID == id {
					out = append(out, "file:"+f.ID)
				}
			}
			return out
		case "page":
			var out []string
			for _, p := range a.children(id, "", false) {
				out = append(out, "page:"+p.ID)
			}
			return out
		}
		return nil
	}
	s := &a.treeState
	a.treeRow = -1
	for i := 0; i < s.Rows(); i++ {
		if a.route.Kind == "page" && s.Item(i) == "page:"+a.route.ID {
			a.treeRow = i
		}
	}
	s.List.Selected = &a.treeRow
	s.List.Header = func(i int) bool { return strings.HasPrefix(s.Item(i), "section:") }
	s.List.Label = func(i int) string { return a.treeLabel(s.Item(i)) }
	list := ui.Outline(c, s, roots, children, func(key string) { a.treeItem(c, key) }).Grow(1).Padding(0, 8, 12)
	if list.Changed() && a.treeRow >= 0 && a.treeRow < s.Rows() {
		key := s.Item(a.treeRow)
		kind, id, _ := strings.Cut(key, ":")
		switch kind {
		case "page":
			a.openPage(id)
		case "folder":
			if s.Open.Has(key) {
				s.Open.Remove(key)
			} else {
				s.Open.Add(key)
			}
		case "file":
			for _, f := range a.files {
				if f.ID == id {
					a.openFile(f)
				}
			}
		}
	}
}

// treeLabel is the text of an item of the tree.
func (a *App) treeLabel(key string) string {
	kind, id, _ := strings.Cut(key, ":")
	switch kind {
	case "section":
		return map[string]string{"mine": "My space", "shared": "Shared with me"}[id]
	case "folder":
		for _, f := range a.folders {
			if f.ID == id {
				return f.Name
			}
		}
	case "page":
		if p := a.pageByID(id); p != nil {
			return p.DisplayTitle()
		}
	case "file":
		for _, f := range a.files {
			if f.ID == id {
				return f.Name
			}
		}
	}
	return ""
}

// treeItem builds an item of the tree after its arrow: its icon and
// name, with its menu, under the pointer its buttons, and a right-click
// for the menu too.
func (a *App) treeItem(c *ui.Context, key string) {
	pal := theme.Of(c)
	kind, id, _ := strings.Cut(key, ":")
	if kind == "section" {
		if id == "mine" {
			a.sectionHeader(c, "My space", "folder-plus", "New folder", func() { a.dialogs.openRenameFolder(nil) })
		} else {
			a.sectionHeader(c, "Shared with me", "", "", nil)
		}
		return
	}
	row := ui.Row(c.Key("item:" + key)).Grow(1).Height(26).Gap(6).AlignItems(ui.Center)
	hovered := row.Hovered()
	// On the chosen row, the accent's, buttons are in its text's color.
	color := pal.Icon
	if a.route.Kind == "page" && key == "page:"+a.route.ID {
		color = pal.OnAccent
	}
	button := func(name, icon, label string) ui.Element {
		b := iconButton(c, name, icon, label).Size(22, 22).Padding(3).TextColor(color)
		if !hovered {
			b.Opacity(0)
		}
		return b
	}
	switch kind {
	case "folder":
		var f store.Folder
		for _, x := range a.folders {
			if x.ID == id {
				f = x
			}
		}
		row.Children(func() {
			icon := "folder"
			if a.treeState.Open.Has(key) {
				icon = "folder-open"
			}
			ui.Icon(c, theme.Icon(icon)).FontSize(15)
			ui.Text(c, f.Name).FontSize(13.5).Grow(1).SingleLine()
			tools(c, func() {
				button("more", "ellipsis", "Folder actions").Menu(func(m *ui.Menu) { a.folderMenu(m, f) })
			})
		})
		row.ContextMenu(func(m *ui.Menu) { a.folderMenu(m, f) })
	case "page":
		p := a.pageByID(id)
		if p == nil {
			return
		}
		row.Children(func() {
			ui.Icon(c, theme.Icon("file-text")).FontSize(15)
			title := ui.Text(c, p.DisplayTitle()).FontSize(13.5).Grow(1).SingleLine()
			if open := a.route.Kind == "page" && a.route.ID == id; p.Title == "" && !open {
				title.TextColor(pal.TextMuted)
			}
			// + only while hovered, so a title has the room until then; the
			// menu button stays built for its menu to report the choice.
			tools(c, func() {
				if hovered && button("add", "plus", "Add a subpage").Clicked() {
					a.newPage(p.ID, "")
				}
				button("more", "ellipsis", "Page actions").Menu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
			})
		})
		row.ContextMenu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
	case "file":
		var f store.File
		for _, x := range a.files {
			if x.ID == id {
				f = x
			}
		}
		row.Children(func() {
			ui.Icon(c, theme.Icon("file")).FontSize(15)
			ui.Text(c, f.Name).FontSize(13.5).Grow(1).SingleLine()
		})
		row.ContextMenu(func(m *ui.Menu) {
			if m.Item("Open").Chosen() && f.LocalPath != "" {
				a.openFile(f)
			}
			if m.Item("Delete file").Chosen() {
				if err := a.st.DeleteFile(f); err != nil {
					a.toast("Could not delete: " + err.Error())
				}
			}
		})
	}
}

// pageMenu is a page's actions, in the sidebar and the page's header;
// the header's leaves out Share, whose button is beside it. Share and
// Keep on this device need a server to share and sync with.
func (a *App) pageMenu(m *ui.Menu, p store.Page, header bool) {
	editable := a.canEdit(p.ID)
	if m.Item("Add subpage").Disabled(!editable).Chosen() {
		a.newPage(p.ID, "")
	}
	if m.Item("Rename").Disabled(!editable).Chosen() {
		a.openPage(p.ID)
		a.focusTitle(true)
	}
	// A template stays out of the page list: it doesn't move among the
	// pages, and starts copies from a blank page rather than here.
	if !p.Template {
		if m.Item("Move to…").Disabled(!editable).Chosen() {
			a.dialogs.openMove(p.ID)
		}
		if m.Item("Duplicate").Chosen() {
			a.duplicatePage(p)
		}
		if m.Item("Save as template").Chosen() {
			a.saveAsTemplate(p)
		}
	}
	m.Separator()
	if m.Item("Page history").Chosen() {
		a.openHistory(p.ID)
	}
	server := a.backend.Configured()
	if !header && server && !p.Template && m.Item("Share…").Chosen() {
		a.openShare(p.ID)
	}
	if m.Item("Export as Markdown…").Chosen() {
		a.exportPage(p.ID)
	}
	if server && !p.Template && m.Item("Keep on this device").Checked(p.KeepOffline || a.settings.AllPagesOffline).Disabled(a.settings.AllPagesOffline).Chosen() {
		if err := a.st.SetKeepOffline(p.ID, !p.KeepOffline); err != nil {
			a.toast("Could not save: " + err.Error())
		}
	}
	m.Separator()
	if m.Item("Delete").Disabled(!editable).Chosen() {
		a.deletePage(p.ID)
	}
}

func (a *App) sidebarFooter(c *ui.Context) {
	pal := theme.Of(c)
	ui.Row(c.Key("sidebar-footer")).Padding(8, 8, 8, 12).Gap(10).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(pal.Separator).Children(func() {
		avatar(c, a.session.DisplayName, 26, false)
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
		tools(c, func() {
			if a.updateReady != "" {
				tip := "Restart to update to " + a.updateReady
				if iconButton(c, "update", "rotate-cw", tip).TextColor(pal.Accent).Clicked() {
					a.restartToUpdate()
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
