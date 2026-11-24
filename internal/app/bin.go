package app

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// binState is the bin as the app shows it: what is in it, read as it is opened and again as pages and files change
// while it shows, whether it has been read yet, or why it could not be; the item chosen and its page as Markdown; and the alert asking before
// something goes for good, while it is open. The bin and the page chosen are read off the main thread, as the bin may
// hold many pages: a read that a later one has followed is not shown.
type binState struct {
	items      []store.BinItem
	read       bool
	readErr    error
	pick       store.BinRef
	shown      binShownPage
	purging    *store.BinItem
	emptying   bool
	listReads  int
	shownReads int
}

// binShownPage is a page of the bin as Markdown.
type binShownPage struct {
	of store.BinRef
	md string
}

// binName is the bin as the system names it: the Trash on a Mac, the Recycle Bin on Windows.
func binName() string {
	if runtime.GOOS == "windows" {
		return locales.BinNameWindows()
	}
	return locales.BinName()
}

func binKey(ref store.BinRef) string { return string(ref.Kind) + ":" + ref.ID }

func (a *App) openBin() { a.navigate(Route{Kind: "bin"}) }

// toggleBin opens the bin, or, while it shows, goes back to what showed before it, as Back does, or home when nothing
// did.
func (a *App) toggleBin() {
	switch {
	case a.route.Kind != "bin":
		a.openBin()
	case a.router != nil && a.router.CanGoBack():
		a.back()
	default:
		a.navigate(Route{Kind: "home"})
	}
}

// loadBin reads what is in the bin, and then the page chosen, if one still is.
func (a *App) loadBin() {
	a.bin.listReads++
	read, st := a.bin.listReads, a.st
	var items []store.BinItem
	var err error
	a.offMain(func() { items, err = st.Bin() }, func() {
		if read != a.bin.listReads {
			return
		}
		a.bin.items, a.bin.read, a.bin.readErr = items, true, err
		a.loadBinShown()
	})
}

func (a *App) pickBin(it store.BinItem) {
	a.bin.pick = it.BinRef
	a.loadBinShown()
}

// loadBinShown reads the page chosen as Markdown, its links to other pages by their titles as the sidebar has them.
func (a *App) loadBinShown() {
	a.bin.shownReads++
	it := a.binPicked()
	if it == nil || it.Kind != store.BinPage {
		a.bin.shown = binShownPage{}
		return
	}
	read, st, ref := a.bin.shownReads, a.st, it.BinRef
	titles := map[string]string{}
	for _, list := range [][]store.Page{a.pages, a.templates} {
		for _, p := range list {
			titles[p.ID] = p.DisplayTitle()
		}
	}
	opts := a.markdownOptions()
	opts.PageTitle = func(id string) string { return titles[id] }
	var md string
	a.offMain(func() {
		if content := st.Content(ref.ID); content != nil {
			md = doc.PageMarkdown("", content, opts)
		}
	}, func() {
		if read == a.bin.shownReads {
			a.bin.shown = binShownPage{of: ref, md: md}
		}
	})
}

func (a *App) binPicked() *store.BinItem {
	for i, it := range a.bin.items {
		if it.BinRef == a.bin.pick {
			return &a.bin.items[i]
		}
	}
	return nil
}

func (a *App) binView(c *ui.Context) {
	pal := theme.Of(c)
	items := a.bin.items
	a.titleBar(c, func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Margin(0, 0, 0, 6).Shrink(1).ClipX().Children(func() {
			ui.Icon(c, theme.Icon("trash")).FontSize(14).TextColor(pal.TextMuted).Shrink(0)
			ui.Text(c, binName()).FontSize(13).TextColor(pal.Text).SingleLine().Shrink(1)
		})
		ui.Spacer(c)
		a.syncStatus(c)
		tools(c, func() {
			if len(items) > 0 && button(c, "empty-bin", "trash", locales.BinEmptyButton(binName())).Clicked() {
				a.bin.emptying = true
			}
			a.panelToggle(c)
		})
	})
	ui.Row(c.Key("bin")).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("bin-list")).Width(340).Shrink(0).BorderWidth(0, 1, 0, 0).BorderColor(pal.Divider).Children(func() {
			ui.Column(c).Padding(18, 18, 12).Gap(4).Children(func() {
				ui.Text(c, binName()).FontSize(20).FontWeight(700).TextColor(pal.Text)
				ui.Text(c, locales.BinHint(store.BinDays)).FontSize(12.5).TextColor(pal.TextMuted)
			})
			if err := a.bin.readErr; err != nil {
				ui.Text(c, locales.BinCouldNotRead(binName(), err.Error())).FontSize(12.5).TextColor(pal.Danger).Padding(0, 18, 12)
			}
			if len(items) == 0 && !a.bin.read {
				return
			}
			if len(items) == 0 {
				ui.Column(c).Grow(1).Center().Gap(10).Padding(24).Children(func() {
					ui.Icon(c, theme.Icon("trash")).FontSize(30).TextColor(pal.ChartMuted)
					ui.Text(c, locales.BinNothing(binName())).FontSize(14).TextColor(pal.TextMuted)
				})
				return
			}
			ui.Scroll(c.Key("bin-scroll")).Grow(1).Padding(0, 8, 12).Children(func() {
				for _, it := range items {
					a.binRow(c, it, pal)
				}
			})
		})
		ui.Column(c.Key("bin-shown")).Grow(1).Children(func() {
			if it := a.binPicked(); it != nil {
				a.binShown(c, *it, pal)
				return
			}
			if len(items) > 0 {
				ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
					ui.Text(c, locales.BinChoose()).TextColor(pal.TextMuted).Balance()
				})
			}
		})
	})
	a.binAlerts(c)
}

func binIcon(it store.BinItem) string {
	switch {
	case it.Kind == store.BinFile:
		return "file"
	case it.Template:
		return "layout-template"
	}
	return "file-text"
}

func (a *App) binRow(c *ui.Context, it store.BinItem, pal *theme.Palette) {
	row := ui.ButtonBase(c.Key("bin-row:"+binKey(it.BinRef))).Label(it.Title).Tooltip(it.Title).Padding(8, 10).Radius(7).Gap(10).
		AlignItems(ui.Center).Justify(ui.Start).FillWidth()
	switch {
	case a.bin.pick == it.BinRef:
		row.Background(pal.NavSelected)
	case row.Hovered():
		row.Background(pal.Hover)
	}
	row.Children(func() {
		if it.Icon != "" {
			ui.Text(c, it.Icon).FontSize(15).Shrink(0)
		} else {
			ui.Icon(c, theme.Icon(binIcon(it))).FontSize(15).TextColor(pal.TextMuted).Shrink(0)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, it.Title).FontSize(13.5).FontWeight(550).TextColor(pal.Text).SingleLine()
			ui.Text(c, core.BinWhen(it, time.Now())).FontSize(11.5).TextColor(pal.TextMuted).SingleLine()
		})
	})
	row.PopoverMenu(func(m *ui.Menu) { a.binMenu(m, it) })
	row.OnClick(func() { a.pickBin(it) })
}

func (a *App) binMenu(m *ui.Menu, it store.BinItem) {
	if m.Item(locales.BinRestore()).Chosen() {
		a.restoreFromBin(it)
	}
	if it.Kind == store.BinPage && m.Item(locales.BinHistory()).Chosen() {
		a.openHistory(it.ID)
	}
	m.Separator()
	if m.Item(locales.BinDeleteForever()).Danger().Chosen() {
		item := it
		a.bin.purging = &item
	}
}

// binShown shows the item chosen: a page as it was, which can only be read, and its history.
func (a *App) binShown(c *ui.Context, it store.BinItem, pal *theme.Palette) {
	ui.Row(c.Key("bin-actions")).Padding(10, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.Divider).Children(func() {
		ui.Icon(c, theme.Icon("trash")).FontSize(13).TextColor(pal.TextMuted).Shrink(0)
		ui.Text(c, locales.BinInBin(binName())).FontSize(12.5).TextColor(pal.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if it.Kind == store.BinPage && button(c, "bin-history", "history", locales.BinHistory()).Clicked() {
			a.openHistory(it.ID)
		}
		if danger(c, "bin-purge", locales.BinDeleteForever()).Clicked() {
			item := it
			a.bin.purging = &item
		}
		if primary(c, "bin-restore", "rotate-ccw", locales.BinRestore()).Clicked() {
			a.restoreFromBin(it)
		}
	})
	size := float32(max(a.settings.TextSize, 12))
	ui.Scroll(c.Key("bin-shown-scroll:" + binKey(it.BinRef))).Grow(1).Children(func() {
		ui.Column(c).FillWidth().Padding(32, 40, 120).Gap(6).Children(func() {
			ui.Text(c, it.Title).Font(theme.PageFamily(it.Title)).FontSize(size * 2.15).FontWeight(700).TextColor(pal.Text)
			notes := []string{core.BinWhen(it, time.Now())}
			switch {
			case it.Kind == store.BinFile:
				notes = append(notes, locales.BinFile(), locales.CardSizeBytes(int(it.File.SizeBytes)))
			case it.Template:
				notes = append(notes, locales.BinTemplate())
			}
			if it.Pages > 1 {
				notes = append(notes, locales.BinPages(it.Pages))
			}
			for _, n := range notes {
				ui.Text(c, n).FontSize(13).TextColor(pal.TextMuted)
			}
			if it.Kind != store.BinPage {
				return
			}
			ui.Box(c).Height(10)
			if shown := a.bin.shown; shown.of == it.BinRef && shown.md != "" {
				markdownView(c, shown.md, &a.md).Font(theme.PageFamily(shown.md)).FontSize(size)
			}
		})
	})
}

func (a *App) binAlerts(c *ui.Context) {
	if it := a.bin.purging; it != nil {
		open := true
		if ui.AlertDialog(c, &open, locales.BinDeleteForeverTitle(it.Title), locales.BinDeleteForeverMessage(),
			locales.CommonCancel(), locales.BinDeleteForever()) == 1 {
			item := *it
			a.purgeFromBin(func(cr *core.Core) error { return cr.PurgeBin(item.BinRef) }, locales.BinDeletedForever(item.Title))
		}
		if !open {
			a.bin.purging = nil
		}
	}
	if a.bin.emptying {
		open := true
		if ui.AlertDialog(c, &open, locales.BinEmptyTitle(binName()), locales.BinEmptyMessage(),
			locales.CommonCancel(), locales.BinEmptyButton(binName())) == 1 {
			a.purgeFromBin((*core.Core).EmptyBin, locales.BinEmptied(binName()))
		}
		if !open {
			a.bin.emptying = false
		}
	}
}

// purgeFromBin deletes for good off the main thread, as the whole bin may hold many pages and files.
func (a *App) purgeFromBin(purge func(*core.Core) error, done string) {
	cr := a.core()
	var err error
	a.offMain(func() { err = purge(cr) }, func() {
		if err != nil {
			a.toast(locales.SidebarCouldNotDelete(err.Error()))
		} else {
			a.toast(done)
		}
		if a.route.Kind == "bin" {
			a.loadBin()
		}
	})
}

// restoreFromBin takes an item out of the bin on the main thread, as a page coming back is linked in its parent, which
// may be open in the editor.
func (a *App) restoreFromBin(it store.BinItem) {
	if err := a.pageCore().RestoreFromBin(it.BinRef); err != nil {
		a.toast(locales.AppErrorRestore(err.Error()))
		return
	}
	reread(&a.pages, a.st.Pages)
	reread(&a.templates, a.st.Templates)
	reread(&a.files, a.st.Files)
	if a.bin.pick == it.BinRef {
		a.bin.pick = store.BinRef{}
	}
	a.loadBin()
	if it.Kind == store.BinFile || it.Template {
		a.toast(locales.BinRestored(it.Title))
		return
	}
	id := it.ID
	a.toastAction(locales.BinRestored(it.Title), locales.CommonOpen(), func() { a.openPage(id) })
}
