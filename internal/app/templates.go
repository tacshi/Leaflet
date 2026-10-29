package app

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// duplicatePage copies a page and its subpages next to it, links the copy
// where the page is linked, and opens it. A page shared with the user
// that they can't add to the parent of is copied into their own space.
func (a *App) duplicatePage(p store.Page) {
	a.flush()
	if a.recordingIn(p.ID) {
		a.toast(locales.TemplateWaitForRecording())
		return
	}
	o := store.CopyOptions{ParentID: p.ParentID, FolderID: p.FolderID, AfterID: p.ID, Title: copyTitle(p.Title)}
	if a.isShared(p) && (p.ParentID == "" || !a.canEdit(p.ParentID)) {
		o = store.CopyOptions{Title: o.Title}
	}
	cp, err := a.st.CopyPage(p.ID, o)
	if err != nil {
		a.toast(locales.TemplateDuplicateFailed(err.Error()))
		return
	}
	a.pages, _ = a.st.Pages()
	if cp.ParentID != "" {
		a.linkSubpage(cp.ParentID, cp.ID, p.ID)
	}
	a.openPage(cp.ID)
	a.focusTitle(true)
}

// recordingIn reports whether a recording on a page or its subpages is
// still being made or transcribed here: a copy of it now would stay as it
// is, without what the recording gets once it finishes.
func (a *App) recordingIn(id string) bool {
	if a.rec.Active() && a.isUnder(a.rec.State.PageID, id) {
		return true
	}
	for rid := range a.rec.Transcribing {
		if r, err := a.st.Recording(rid); err == nil && a.isUnder(r.PageID, id) {
			return true
		}
	}
	return false
}

// copyTitle is the title of a copy, as Finder names one.
func copyTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return locales.TemplateCopyTitle(title)
}

// saveAsTemplate keeps a copy of a page, with its subpages, as a template
// to start other pages from.
func (a *App) saveAsTemplate(p store.Page) {
	a.flush()
	if a.recordingIn(p.ID) {
		a.toast(locales.TemplateWaitForRecording())
		return
	}
	t, err := a.st.CopyPage(p.ID, store.CopyOptions{Title: p.Title, Template: true})
	if err != nil {
		a.toast(locales.TemplateSaveFailed(err.Error()))
		return
	}
	a.templates, _ = a.st.Templates()
	a.toastAction(locales.TemplateSaved(t.DisplayTitle()), locales.CommonEdit(), func() { a.openPage(t.ID) })
}

// templateRoots are the templates themselves, without their subpages, by
// title.
func (a *App) templateRoots() []store.Page {
	var out []store.Page
	for _, t := range a.templates {
		if t.ParentID == "" {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].DisplayTitle()) < strings.ToLower(out[j].DisplayTitle())
	})
	return out
}

// templateKids are the subpages of a template.
func (a *App) templateKids(id string) []store.Page {
	var out []store.Page
	for _, t := range a.templates {
		if t.ParentID == id {
			out = append(out, t)
		}
	}
	return out
}

// applyTemplate fills the open, empty page from a template: its content,
// as one undo step, and copies of its subpages. An untitled page takes the
// template's title, selected to type over.
func (a *App) applyTemplate(t store.Page) {
	ps := a.page
	if ps == nil {
		return
	}
	c, err := a.st.CopyInto(t.ID, ps.id)
	if err != nil {
		a.toast(locales.TemplateUseFailed(err.Error()))
		return
	}
	ps.editor.Edit(func(cur *doc.Content) { cur.Children = c.Children })
	a.pages, _ = a.st.Pages()
	if len(a.children(ps.id, "", false)) > 0 {
		a.treeOpen("page", ps.id)
	}
	a.savePage()
	if strings.TrimSpace(ps.title) == "" && t.Title != "" {
		ps.title = t.Title
		a.saveTitle()
		a.focusTitle(true)
	}
}

// templatePill marks a template, where it shows among pages.
func templatePill(c *ui.Context) { accentBadge(c, locales.TemplatePill()) }

// templateActions list the templates under a blank page's other ways to
// start, each with a menu to edit or delete it.
func (a *App) templateActions(c *ui.Context, row func(key, icon, label, hint string, fn func(), menu func(m *ui.Menu))) {
	pal := theme.Of(c)
	roots := a.templateRoots()
	if len(roots) == 0 {
		return
	}
	ui.Text(c, locales.TemplateSectionTitle()).FontSize(12).FontWeight(600).TextColor(pal.TextMuted).Margin(10, 0, 4, 2)
	for _, t := range roots {
		row("template:"+t.ID, "layout-template", t.DisplayTitle(), a.templateHint(t), func() { a.applyTemplate(t) }, func(m *ui.Menu) {
			if m.Item(locales.TemplateEdit()).Chosen() {
				a.openPage(t.ID)
			}
			if m.Item(locales.TemplateDelete()).Chosen() {
				a.deletePage(t.ID)
			}
		})
	}
}

// templateHint is the first line of a template's text, to tell templates
// apart, or how many subpages it has when it has no text; kept until the
// template next changes or the app's language does.
func (a *App) templateHint(t store.Page) string {
	key := t.ID + "@" + t.UpdatedAt.String() + "@" + i18n.Locale()
	if h, ok := a.templateHints[key]; ok {
		return h
	}
	hint := locales.TemplateHintEmpty()
	if n := len(a.templateKids(t.ID)); n > 0 {
		hint = locales.TemplateHintSubpages(n)
	}
	if row, err := a.st.ContentRow(t.ID); err == nil {
		for _, l := range strings.Split(row.PlainText, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				hint = l
				break
			}
		}
	}
	if a.templateHints == nil {
		a.templateHints = map[string]string{}
	}
	a.templateHints[key] = hint
	return hint
}
