package app

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (a *App) tagCounts() []store.TagCount { return a.library().Tags }

func (a *App) tagNames() []string {
	var out []string
	for _, t := range a.tagCounts() {
		out = append(out, t.Name)
	}
	return out
}

func (a *App) showTag(tag string) {
	for _, t := range a.tagCounts() {
		if strings.EqualFold(t.Name, tag) {
			a.tagTree.Open.Add("tag:" + t.Name)
		}
	}
	a.showSidebar(tabTags)
}

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
		for _, p := range a.library().Tagged(tag) {
			out = append(out, "tagged:"+p.ID+"|"+tag)
		}
		return out
	}
	s := &a.tagTree
	if a.tagIn.follows(a.route.Kind + ":" + a.route.ID) {
		a.tagRow = -1
		for i := 0; i < s.Rows(); i++ {
			if id, _ := taggedPage(s.Item(i)); a.route.Kind == "page" && id == a.route.ID {
				a.tagRow = i
				break
			}
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
	pressed, clicked := a.tagIn.after(list)
	submitted := list.Submitted()
	if (list.Changed() || submitted) && s.Open.Len() == opened && a.tagRow >= 0 && a.tagRow < s.Rows() {
		key := s.Item(a.tagRow)
		if id, ok := taggedPage(key); ok {
			a.openPage(id)
		} else if !pressed && !submitted {
		} else if s.Open.Has(key) {
			s.Open.Remove(key)
		} else {
			s.Open.Add(key)
		}
	}
	if clicked && clickedOff(s, a.tagRow, func(key string) bool { _, ok := taggedPage(key); return ok }) {
		a.tagRow = openRow(s, roots, children, func(key string) bool {
			id, ok := taggedPage(key)
			return ok && a.route.Kind == "page" && id == a.route.ID
		})
	}
}

func taggedPage(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "tagged:")
	id, _, _ := strings.Cut(rest, "|")
	return id, ok
}

func (a *App) tagItem(c *ui.Context, key string, counts map[string]int) {
	pal := theme.Of(c)
	row := ui.Row(c.Key("item:" + key)).Grow(1).Height(26).Gap(6).AlignItems(ui.Center)
	hovered := row.Hovered()
	if tag, ok := strings.CutPrefix(key, "tag:"); ok {
		t := c.Theme()
		band := tagColor(tag).Alpha(0.1)
		lead, pad := t.Space(2)+t.Space(4)+t.Space(1), t.Space(1)
		row.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X - lead, Y: r.Y - pad + 1, W: r.W + lead + t.Space(2), H: r.H + 2*pad - 2}, band, t.Radius)
		})
		row.Children(func() {
			ui.Text(c, tag).FontSize(13.5).FontWeight(600).TextColor(tagColor(tag).Mix(pal.Text, 0.35)).SingleLine()
			ui.Spacer(c)
			tools(c, func() {
				b := iconButton(c, "more", "ellipsis", locales.TagActions()).Size(22, 22).Padding(3).IgnoreInput(!hovered)
				if !hovered {
					b.Opacity(0)
				}
				b.PopoverMenuButton(func(m *ui.Menu) { a.tagMenu(m, tag) })
			})
			ui.Text(c, strconv.Itoa(counts[tag])).FontSize(12).TextColor(pal.TextMuted).Padding(0, 4)
		})
		row.PopoverMenu(func(m *ui.Menu) { a.tagMenu(m, tag) })
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
	row.PopoverMenu(func(m *ui.Menu) { a.pageMenu(m, *p, false) })
}

func (a *App) tagMenu(m *ui.Menu, tag string) {
	if m.Item(locales.TagRenameEllipsis()).Chosen() {
		a.dialogs.openRenameTag(tag)
	}
	m.Separator()
	if m.Item(locales.TagDelete()).Danger().Chosen() {
		a.deleteTag(tag)
	}
}

func (a *App) deleteTag(tag string) {
	before, err := a.st.DeleteTag(tag)
	if err != nil {
		a.toast(locales.SidebarCouldNotSave(err.Error()))
		return
	}
	a.toastAction(locales.TagDeleted(tag), locales.CommonUndo(), func() {
		for id, tags := range before {
			if err := a.st.SetPageTags(id, tags); err != nil {
				a.toast(locales.SidebarCouldNotSave(err.Error()))
				return
			}
		}
	})
}

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
