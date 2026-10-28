package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (a *App) sidebar(c *ui.Context) {
	pal := theme.Of(c)
	bar := c.TitleBar()
	// The window controls sit at the top left.
	ui.Row(c.Key("sidebar-top")).Height(max(bar.Height, headerHeight)).Padding(0, 8, 0, bar.Left+8).Gap(2).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Spacer(c)
		tools(c, func() {
			if iconButton(c, "search", "search", locales.SidebarSearch()).Clicked() {
				a.openSearch()
			}
			todos := iconButton(c, "todos", "square-check", locales.TodosTitle())
			if a.route.Kind == "todos" {
				todos.Background(pal.NavSelected).TextColor(pal.Text)
			}
			// It toggles: clicked while open, it goes back to where the
			// view was opened from.
			if todos.Clicked() {
				switch {
				case a.route.Kind != "todos":
					a.navigate(Route{Kind: "todos"})
				case a.router != nil && a.router.CanGoBack():
					a.back()
				default:
					a.navigate(Route{Kind: "home"})
				}
			}
			if iconButton(c, "new-page", "square-pen", locales.SidebarNewPage()).Clicked() {
				a.newPage("", "")
			}
			if iconButton(c, "hide-sidebar", "panel-left", locales.SidebarHide()).Clicked() {
				a.toggleSidebar()
			}
		})
	})
	a.tree(c)
	if len(a.pages) == 0 {
		ui.Text(c, locales.SidebarNoPages()).FontSize(13).TextColor(pal.TextMuted).Padding(0, 18, 12)
	}
	a.sidebarFooter(c)
}

func (a *App) sectionHeader(c *ui.Context, key, title, icon, label string, action func()) {
	pal := theme.Of(c)
	// In the tree, a header starts where the rows' arrows do: back over
	// the slot the Outline keeps for an arrow, and the gap after it.
	t := c.Theme()
	row := ui.Row(c.Key("section:"+key)).Height(28).Padding(0, 4, 0, 0).Margin(10, 0, 2).
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
	if m.Item(locales.SidebarNewPageInFolder()).Chosen() {
		a.treeOpen("folder", f.ID)
		a.newPage("", f.ID)
	}
	if m.Item(locales.SidebarUploadFile()).Chosen() {
		a.uploadFile(f.ID)
	}
	if m.Item(locales.SidebarRenameEllipsis()).Chosen() {
		ff := f
		a.dialogs.openRenameFolder(&ff)
	}
	m.Separator()
	if m.Item(locales.SidebarDeleteFolder()).Chosen() {
		if err := a.st.DeleteFolder(f); err != nil {
			a.toast(locales.SidebarCouldNotDelete(err.Error()))
			return
		}
		a.toast(locales.SidebarFolderDeleted(f.Name))
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
	a.treeRows = a.treeRows[:0]
	list := ui.Outline(c, s, roots, children, func(key string) { a.treeItem(c, key) }).Grow(1).Padding(0, 8, 12)
	a.treeDrop(c, list)
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
		switch id {
		case "mine":
			return locales.SidebarMySpace()
		case "shared":
			return locales.SidebarSharedWithMe()
		}
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
			a.sectionHeader(c, "mine", locales.SidebarMySpace(), "folder-plus", locales.SidebarNewFolder(), func() { a.dialogs.openRenameFolder(nil) })
		} else {
			a.sectionHeader(c, "shared", locales.SidebarSharedWithMe(), "", "", nil)
		}
		return
	}
	row := ui.Row(c.Key("item:" + key)).Grow(1).Height(26).Gap(6).AlignItems(ui.Center)
	if kind != "file" {
		a.treeRows = append(a.treeRows, treeRow{key, row.Bounds()})
	}
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
				button("more", "ellipsis", locales.SidebarFolderActions()).Menu(func(m *ui.Menu) { a.folderMenu(m, f) })
			})
		})
		row.ContextMenu(func(m *ui.Menu) { a.folderMenu(m, f) })
		a.pageDrop(c, row, key)
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
				if hovered && button("add", "plus", locales.PageAddSubpage()).Clicked() {
					a.newPage(p.ID, "")
				}
				button("more", "ellipsis", locales.PageActions()).Menu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
			})
		})
		row.ContextMenu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
		// A drag source takes the press from the list, so a click on it
		// opens the page itself.
		if a.canEdit(id) {
			row.Drag(pageDrag{id})
			if row.Dragging() {
				row.Opacity(0.4)
			}
			if row.Clicked() {
				a.openPage(id)
			}
		}
		a.pageDrop(c, row, key)
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
			if m.Item(locales.CommonOpen()).Chosen() && f.LocalPath != "" {
				a.openFile(f)
			}
			if m.Item(locales.SidebarDeleteFile()).Chosen() {
				if err := a.st.DeleteFile(f); err != nil {
					a.toast(locales.SidebarCouldNotDelete(err.Error()))
				}
			}
		})
	}
}

// pageDrag is a page dragged in the sidebar's tree, to another place.
type pageDrag struct{ id string }

// Where a page dragged over a row goes: before it, after it, or inside.
const (
	dropBefore = iota
	dropAfter
	dropInside
)

// treeRow is a row of the sidebar's tree built this frame: its item, and
// its content's box, after its arrow, in the last frame.
type treeRow struct {
	key string
	box ui.Rect
}

// dropSpot is where page d dragged goes over the row of key, at (x, y)
// in its content's box of height h: a page's top and bottom quarter put
// it before and after the page, and its middle inside it, last, as a
// folder's row does. Below the last page among its siblings, x left of
// the content puts it after a page above instead, a level out for each
// indent; out says how many. A page cannot go inside itself, or into
// another space.
func (a *App) dropSpot(c *ui.Context, d pageDrag, key string, x, y, h float32) (at int, target string, out int, ok bool) {
	kind, id, _ := strings.Cut(key, ":")
	p := a.pageByID(d.id)
	if p == nil {
		return 0, "", 0, false
	}
	if kind == "folder" {
		return dropInside, key, 0, !a.isShared(*p)
	}
	t := a.pageByID(id)
	if t == nil || t.SpaceID != p.SpaceID || id != d.id && a.st.IsDescendant(id, d.id) {
		return 0, "", 0, false
	}
	switch {
	case y < h/4 && id != d.id:
		return dropBefore, key, 0, true
	case y > h*3/4:
		indent := a.treeIndent(c)
		for levels := int(-x/indent) + 1; x < 0 && levels > 0 && t.ParentID != "" && a.lastSibling(t, d.id); levels-- {
			if t, out = a.pageByID(t.ParentID), out+1; t == nil {
				return 0, "", 0, false
			}
		}
		return dropAfter, "page:" + t.ID, out, t.ID != d.id
	case id != d.id:
		return dropInside, key, 0, a.canEdit(id)
	}
	return 0, "", 0, false
}

// treeIndent is how far in each level of the tree is.
func (a *App) treeIndent(c *ui.Context) float32 {
	if a.treeState.Indent > 0 {
		return a.treeState.Indent
	}
	return c.Theme().Space(4)
}

// pageDrop makes the row of key take pages dragged onto it, where
// dropSpot says, and shows where they would go: a line before or after
// it, or the row tinted for inside.
func (a *App) pageDrop(c *ui.Context, row ui.Element, key string) {
	pal := theme.Of(c)
	spot := func(d pageDrag) (int, string, int, bool) {
		x, y, _ := row.PointerPosition()
		return a.dropSpot(c, d, key, x, y, row.Bounds().H)
	}
	if d, ok := ui.DragOver[pageDrag](row); ok {
		if at, _, _, ok := spot(d); ok {
			if at == dropInside {
				row.Background(pal.AccentSoft).Radius(6)
			} else {
				row.DrawOver(func(p *ui.Painter, r ui.Rect) {
					y := r.Y
					if at == dropAfter {
						y += r.H
					}
					dropLine(p, r.X, y, r.X+r.W, pal)
				})
			}
		}
	}
	if d, ok := ui.Drop[pageDrag](row); ok {
		if at, target, out, ok := spot(d); ok {
			a.dropPage(d.id, target, at, out > 0)
		}
	}
}

// treeDrop makes the tree take pages dragged beside its rows' content,
// over their arrows and indents, as dropSpot says for the row there, and
// below its rows, to the end of My space's top level.
func (a *App) treeDrop(c *ui.Context, list ui.Element) {
	pal := theme.Of(c)
	spot := func(d pageDrag) (at int, target string, out int, row ui.Rect, ok bool) {
		lb := list.Bounds()
		lx, ly, _ := list.PointerPosition()
		x, y := lb.X+lx, lb.Y+ly
		// Below the rows, the line starts where the top level's do.
		end, left := float32(0), lb.X+lb.W
		for _, r := range a.treeRows {
			end, left = max(end, r.box.Y+r.box.H), min(left, r.box.X)
			if y >= r.box.Y && y < r.box.Y+r.box.H {
				at, target, out, ok = a.dropSpot(c, d, r.key, x-r.box.X, y-r.box.Y, r.box.H)
				return at, target, out, r.box, ok
			}
		}
		if p := a.pageByID(d.id); y >= end && end > 0 && p != nil && !a.isShared(*p) {
			return dropAfter, "", 0, ui.Rect{X: left, Y: end, W: lb.X + lb.W - 8 - left}, true
		}
		return 0, "", 0, ui.Rect{}, false
	}
	if d, ok := ui.DragOver[pageDrag](list); ok {
		if at, target, out, row, ok := spot(d); ok {
			lb := list.Bounds()
			list.DrawOver(func(p *ui.Painter, r ui.Rect) {
				// The row's box, from the list's in the window to its own.
				x, y := r.X+row.X-lb.X, r.Y+row.Y-lb.Y
				switch {
				case target == "":
					dropLine(p, x, y, x+row.W, pal)
				case at == dropInside:
					p.Fill(ui.Rect{X: x, Y: y, W: row.W, H: row.H}, pal.AccentSoft, 6)
				default:
					if at == dropAfter {
						y += row.H
					}
					dropLine(p, x-float32(out)*a.treeIndent(c), y, x+row.W, pal)
				}
			})
		}
	}
	if d, ok := ui.Drop[pageDrag](list); ok {
		if at, target, out, _, ok := spot(d); ok {
			if target == "" {
				if err := a.movePage(d.id, "", func() error { return a.st.MovePage(d.id, "", "", "") }); err != nil {
					a.toast(locales.SidebarCouldNotMove(err.Error()))
				}
				return
			}
			a.dropPage(d.id, target, at, out > 0)
		}
	}
}

// dropLine is where a page dragged would go: a line from x to right at
// y, with a dot at its start.
func dropLine(p *ui.Painter, x, y, right float32, pal *theme.Palette) {
	p.Fill(ui.Rect{X: x + 4, Y: y - 1, W: right - x - 4, H: 2}, pal.Accent, 1)
	p.Fill(ui.Rect{X: x, Y: y - 4, W: 8, H: 8}, pal.Accent, 4)
}

// lastSibling reports whether page p is the last of its siblings, but
// page skip, unless that is p.
func (a *App) lastSibling(p *store.Page, skip string) bool {
	siblings := a.children(p.ParentID, p.FolderID, p.ParentID == "")
	for k := len(siblings) - 1; k >= 0; k-- {
		if siblings[k].ID != skip || skip == p.ID {
			return siblings[k].ID == p.ID
		}
	}
	return false
}

// dropPage moves page id to where it dropped by the item key: before or
// after it among its siblings, or inside it, last. After a page showing
// its subpages is before the first of them, unless out, a level out of
// the row it dropped on.
func (a *App) dropPage(id, key string, at int, out bool) {
	kind, target, _ := strings.Cut(key, ":")
	var parent string
	var move func() error
	switch {
	case kind == "folder":
		move = func() error { return a.st.MovePage(id, "", target, "") }
		a.treeOpen("folder", target)
	case at == dropInside:
		parent, move = target, func() error { return a.st.MovePage(id, target, "", "") }
		a.treeOpen("page", target)
	default:
		t := a.pageByID(target)
		if t == nil {
			return
		}
		parent = t.ParentID
		switch {
		case at == dropBefore:
			move = func() error { return a.st.MovePageBefore(id, t.ParentID, t.FolderID, t.ID) }
		case !out && a.treeState.Open.Has(key) && len(a.children(t.ID, "", false)) > 0:
			parent, move = t.ID, func() error { return a.st.MovePageBefore(id, t.ID, "", "") }
		default:
			move = func() error { return a.st.MovePage(id, t.ParentID, t.FolderID, t.ID) }
		}
	}
	if err := a.movePage(id, parent, move); err != nil {
		a.toast(locales.SidebarCouldNotMove(err.Error()))
	}
}

// pageMenu is a page's actions, in the sidebar and the page's header;
// the header's leaves out Share, whose button is beside it. Share and
// Keep on this device need a server to share and sync with.
func (a *App) pageMenu(m *ui.Menu, p store.Page, header bool) {
	editable := a.canEdit(p.ID)
	if m.Item(locales.SidebarAddSubpage()).Disabled(!editable).Chosen() {
		a.newPage(p.ID, "")
	}
	if m.Item(locales.CommonRename()).Disabled(!editable).Chosen() {
		a.openPage(p.ID)
		a.focusTitle(true)
	}
	// A template stays out of the page list: it doesn't move among the
	// pages, and starts copies from a blank page rather than here.
	if !p.Template {
		if m.Item(locales.SidebarMoveTo()).Disabled(!editable).Chosen() {
			a.dialogs.openMove(p.ID)
		}
		if m.Item(locales.SidebarDuplicate()).Chosen() {
			a.duplicatePage(p)
		}
		if m.Item(locales.SidebarSaveAsTemplate()).Chosen() {
			a.saveAsTemplate(p)
		}
	}
	m.Separator()
	if m.Item(locales.SidebarPageHistory()).Chosen() {
		a.openHistory(p.ID)
	}
	server := a.backend.Configured()
	if !header && server && !p.Template && m.Item(locales.SidebarShareEllipsis()).Chosen() {
		a.openShare(p.ID)
	}
	if m.Item(locales.SidebarExportMarkdown()).Chosen() {
		a.exportPage(p.ID)
	}
	if (server || a.sync.ICloudOn()) && !p.Template && m.Item(locales.SidebarKeepOnDevice()).Checked(p.KeepOffline || a.settings.AllPagesOffline).Disabled(a.settings.AllPagesOffline).Chosen() {
		if err := a.st.SetKeepOffline(p.ID, !p.KeepOffline); err != nil {
			a.toast(locales.SidebarCouldNotSave(err.Error()))
		}
	}
	m.Separator()
	if m.Item(locales.CommonDelete()).Disabled(!editable).Chosen() {
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
		if !a.syncOn() && a.backend.Configured() {
			if ghost(c, "sign-in", "", locales.CommonSignIn()).Clicked() {
				a.navigate(Route{Kind: "signin"})
			}
		}
		tools(c, func() {
			if a.updateReady != "" {
				tip := locales.SidebarRestartToUpdate(a.updateReady)
				if iconButton(c, "update", "rotate-cw", tip).TextColor(pal.Accent).Clicked() {
					a.restartToUpdate()
				}
			}
			settings := iconButton(c, "settings", "settings", locales.SidebarSettings())
			if a.settingsOpen {
				settings.Background(pal.NavSelected).TextColor(pal.Text)
			}
			if settings.Clicked() {
				a.openSettings("account")
			}
		})
	})
}

// accountStatus is the line under the user's name: where pages are kept,
// or how syncing goes.
func (a *App) accountStatus() string {
	switch {
	case !a.syncOn():
		return locales.SidebarOnDevice()
	case a.syncing:
		return locales.SidebarSyncing()
	case a.offlineNow():
		return locales.PageSyncOffline()
	case a.pending > 0:
		return locales.SidebarToSync(a.pending)
	}
	return locales.SidebarSynced()
}
