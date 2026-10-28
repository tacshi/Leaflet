package app

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// The Tags tab is MyGo's Outline of the pages' tags, each opening to its
// pages. Its items are keys: "tag:" and a tag, "tagged:" a page's ID, |
// and the tag it is under, for a page is under each of its tags.

// tagCounts are the tags of the pages, by name.
func (a *App) tagCounts() []store.TagCount { return store.TagsOf(a.pages) }

// tagNames are the tags of the pages, by name, as suggestions.
func (a *App) tagNames() []string {
	var out []string
	for _, t := range a.tagCounts() {
		out = append(out, t.Name)
	}
	return out
}

// tagged are the pages with a tag, by title.
func (a *App) tagged(tag string) []store.Page {
	var out []store.Page
	for _, p := range a.pages {
		if p.HasTag(tag) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(x, y store.Page) int {
		return strings.Compare(strings.ToLower(x.DisplayTitle()), strings.ToLower(y.DisplayTitle()))
	})
	return out
}

// showTag shows a tag's pages in the Tags tab.
func (a *App) showTag(tag string) {
	a.sidebarTab(tabTags)
	for _, t := range a.tagCounts() {
		if strings.EqualFold(t.Name, tag) {
			a.tagTree.Open.Add("tag:" + t.Name)
		}
	}
	if !a.settings.SidebarOpen {
		a.toggleSidebar()
	}
}

// tagsTree builds the Tags tab: the tags, each with how many pages have
// it, opening to its pages; choosing a page opens it.
func (a *App) tagsTree(c *ui.Context) {
	pal := theme.Of(c)
	tags := a.tagCounts()
	if len(tags) == 0 {
		ui.Text(c, locales.SidebarNoTags()).FontSize(13).TextColor(pal.TextMuted).Padding(2, 18).Grow(1)
		return
	}
	roots := make([]string, 0, len(tags))
	counts := map[string]int{}
	for _, t := range tags {
		roots = append(roots, "tag:"+t.Name)
		counts[t.Name] = t.Count
	}
	children := func(key string) []string {
		tag, ok := strings.CutPrefix(key, "tag:")
		if !ok {
			return nil
		}
		var out []string
		for _, p := range a.tagged(tag) {
			out = append(out, "tagged:"+p.ID+"|"+tag)
		}
		return out
	}
	s := &a.tagTree
	a.tagRow = -1
	for i := 0; i < s.Rows(); i++ {
		if id, _ := taggedPage(s.Item(i)); a.route.Kind == "page" && id == a.route.ID {
			a.tagRow = i
			break
		}
	}
	s.List.Selected = &a.tagRow
	s.List.Label = func(i int) string {
		key := s.Item(i)
		if tag, ok := strings.CutPrefix(key, "tag:"); ok {
			return tag
		}
		id, _ := taggedPage(key)
		return a.pageTitleOf(id)
	}
	opened := s.Open.Len()
	list := ui.Outline(c, s, roots, children, func(key string) { a.tagItem(c, key, counts) }).Grow(1).Padding(0, 8, 12)
	if list.Changed() && s.Open.Len() == opened && a.tagRow >= 0 && a.tagRow < s.Rows() {
		key := s.Item(a.tagRow)
		if id, ok := taggedPage(key); ok {
			a.openPage(id)
		} else if s.Open.Has(key) {
			s.Open.Remove(key)
		} else {
			s.Open.Add(key)
		}
	}
}

// taggedPage is the page of a "tagged:" item of the Tags tab.
func taggedPage(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "tagged:")
	id, _, _ := strings.Cut(rest, "|")
	return id, ok
}

// tagItem builds an item of the Tags tab after its arrow: a tag's chip
// and count, with its menu, or a page's icon and title.
func (a *App) tagItem(c *ui.Context, key string, counts map[string]int) {
	pal := theme.Of(c)
	row := ui.Row(c.Key("item:" + key)).Grow(1).Height(26).Gap(6).AlignItems(ui.Center)
	hovered := row.Hovered()
	if tag, ok := strings.CutPrefix(key, "tag:"); ok {
		row.Children(func() {
			tagChip(c, tag)
			ui.Spacer(c)
			tools(c, func() {
				b := iconButton(c, "more", "ellipsis", locales.TagActions()).Size(22, 22).Padding(3)
				if !hovered {
					b.Opacity(0)
				}
				b.Menu(func(m *ui.Menu) { a.tagMenu(m, tag) })
			})
			ui.Text(c, strconv.Itoa(counts[tag])).FontSize(12).TextColor(pal.TextMuted).Padding(0, 4)
		})
		row.ContextMenu(func(m *ui.Menu) { a.tagMenu(m, tag) })
		return
	}
	id, _ := taggedPage(key)
	p := a.pageByID(id)
	if p == nil {
		return
	}
	row.Children(func() {
		ui.Icon(c, theme.Icon("file-text")).FontSize(15)
		title := ui.Text(c, p.DisplayTitle()).FontSize(13.5).Grow(1).SingleLine().Tooltip(p.DisplayTitle())
		if open := a.route.Kind == "page" && a.route.ID == id; p.Title == "" && !open {
			title.TextColor(pal.TextMuted)
		}
	})
	row.ContextMenu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
}

// tagMenu is a tag's actions: renaming it, and taking it off every page.
func (a *App) tagMenu(m *ui.Menu, tag string) {
	if m.Item(locales.TagRenameEllipsis()).Chosen() {
		a.dialogs.openRenameTag(tag)
	}
	m.Separator()
	if m.Item(locales.TagDelete()).Chosen() {
		a.deleteTag(tag)
	}
}

// deleteTag takes a tag off every page the user may change, and offers
// to put it back.
func (a *App) deleteTag(tag string) {
	before := map[string][]string{}
	for _, p := range a.pages {
		before[p.ID] = p.Tags
	}
	ids, err := a.st.DeleteTag(tag)
	if err != nil {
		a.toast(locales.SidebarCouldNotSave(err.Error()))
		return
	}
	a.toastAction(locales.TagDeleted(tag), locales.CommonUndo(), func() {
		for _, id := range ids {
			if err := a.st.SetPageTags(id, before[id]); err != nil {
				a.toast(locales.SidebarCouldNotSave(err.Error()))
				return
			}
		}
	})
}

// Rename tag ------------------------------------------------------------

func (d *dialogs) openRenameTag(tag string) {
	d.tagOpen, d.tagOld, d.tagName, d.tagSelect = true, tag, tag, true
}

func (a *App) tagDialog(c *ui.Context) {
	d := &a.dialogs
	dialog(c, &d.tagOpen, 380, func() {
		dialogHeader(c, locales.DialogTagRename(), locales.DialogTagRenameSubtitle(), &d.tagOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			in := field(c, "name", &d.tagName, locales.DialogTagName()).AutoFocus()
			if d.tagSelect && in.Focused() {
				d.tagSelect = false
				in.SetTextSelection(0, utf8.RuneCountInString(d.tagName))
			}
			save := func() {
				tags := store.NormalizeTags([]string{d.tagName})
				if len(tags) == 0 {
					return
				}
				if _, err := a.st.RenameTag(d.tagOld, tags[0]); err != nil {
					a.toast(locales.SidebarCouldNotSave(err.Error()))
				}
				a.tagTree.Open.Add("tag:" + tags[0])
				d.tagOpen = false
			}
			if in.Submitted() {
				save()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.tagOpen = false
				}
				if primary(c, "save", "", locales.CommonRename()).Clicked() {
					save()
				}
			})
		})
	})
}
