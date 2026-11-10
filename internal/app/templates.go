package app

import (
	"errors"
	"log"
	"strings"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/templates"
	"leaflet/internal/theme"
	"leaflet/locales"
)

func (a *App) duplicatePage(p store.Page) {
	if !a.flush() {
		return
	}
	if templates.RecordingIn(a.st, a.rec, p.ID, a.isUnder) {
		a.toast(locales.TemplateWaitForRecording())
		return
	}
	failed := func(err error) { a.toast(locales.TemplateDuplicateFailed(err.Error())) }
	if !a.copyable(p.ID, failed, func() { a.again(p.ID, a.duplicatePage) }) {
		return
	}
	o := templates.Duplicate(a.st, p)
	var cp store.Page
	a.copyPage(failed, func() (err error) { cp, err = a.st.CopyPage(p.ID, o); return }, func() {
		reread(&a.pages, a.st.Pages)
		if cp.ParentID != "" {
			a.linkSubpage(cp.ParentID, cp.ID, p.ID)
		}
		a.openPage(cp.ID)
		a.focusTitle(true)
	})
}

func (a *App) again(id string, fn func(store.Page)) {
	if p, err := a.st.Page(id); err == nil && p.DeletedAt.IsZero() {
		fn(p)
	}
}

func (a *App) copyPage(failed func(error), work func() error, done func()) bool {
	if a.fetchingCopy {
		a.toast(locales.TemplateFetchingOther())
		return false
	}
	a.fetchingCopy = true
	var err error
	a.offMain(func() { err = work() }, func() {
		a.fetchingCopy = false
		a.dropFetchedAudio()
		if err != nil {
			a.copyFailed(err, failed)
			return
		}
		done()
	})
	return true
}

func (a *App) dropFetchedAudio() {
	templates.DropAudio(a.st, a.settings, a.fetchedAudio)
	a.fetchedAudio = nil
}

func (a *App) copyable(id string, failed func(error), again func()) bool {
	needs, err := templates.Needs(a.st, a.sync, id)
	if err != nil {
		a.copyFailed(err, failed)
		return false
	}
	if len(needs) == 0 {
		return true
	}
	if a.fetchingCopy {
		a.toast(locales.TemplateFetchingOther())
		return false
	}
	a.fetchingCopy = true
	a.toast(locales.TemplateFetchingFiles())
	go func() {
		audio, err := templates.Fetch(a.st, a.sync, needs)
		a.update(func() {
			a.fetchingCopy = false
			a.fetchedAudio = audio
			if err != nil {
				log.Printf("copy: %v", err)
				a.dropFetchedAudio()
				a.copyFailed(err, failed)
				return
			}
			again()
			if !a.fetchingCopy {
				a.dropFetchedAudio()
			}
		})
	}()
	return false
}

func (a *App) copyFailed(err error, failed func(error)) {
	if err = templates.CopyError(err); errors.Is(err, templates.ErrFilesUploading) {
		a.toast(err.Error())
		return
	}
	failed(err)
}

func (a *App) saveAsTemplate(p store.Page) {
	if !a.flush() {
		return
	}
	if templates.RecordingIn(a.st, a.rec, p.ID, a.isUnder) {
		a.toast(locales.TemplateWaitForRecording())
		return
	}
	failed := func(err error) { a.toast(locales.TemplateSaveFailed(err.Error())) }
	if !a.copyable(p.ID, failed, func() { a.again(p.ID, a.saveAsTemplate) }) {
		return
	}
	var t store.Page
	a.copyPage(failed, func() (err error) {
		t, err = a.st.CopyPage(p.ID, store.CopyOptions{Title: p.Title, Template: true})
		return
	}, func() {
		reread(&a.templates, a.st.Templates)
		a.toastAction(locales.TemplateSaved(t.DisplayTitle()), locales.CommonEdit(), func() { a.openPage(t.ID) })
	})
}

func (a *App) templateChoices() []templates.Choice {
	return templates.Choices(a.templates, a.deletedStarters())
}

func (a *App) deletedStarters() map[string]bool {
	if a.goneStarters == nil {
		a.goneStarters = templates.DeletedStarters(a.st)
	}
	return a.goneStarters
}

func (a *App) reloadTemplates() {
	reread(&a.templates, a.st.Templates)
	a.goneStarters = nil
}

func (a *App) deleteTemplate(t templates.Choice) {
	if t.Page != nil {
		a.deletePage(t.Page.ID)
		return
	}
	key := t.Starter.Key
	err := templates.SetStarterDeleted(a.st, key, true)
	a.reloadTemplates()
	if err != nil {
		a.toast(locales.AppErrorDelete(err.Error()))
		return
	}
	a.toastAction(locales.AppDeleted(t.Title()), locales.CommonUndo(), func() {
		err := templates.SetStarterDeleted(a.st, key, false)
		a.reloadTemplates()
		if err != nil {
			a.toast(locales.AppErrorRestore(err.Error()))
		}
	})
}

func (a *App) templateByKey(key string) (templates.Choice, bool) {
	return templates.Find(a.templateChoices(), key)
}

func (a *App) editTemplate(t templates.Choice) {
	p, err := templates.Own(a.st, t)
	a.reloadTemplates()
	if err != nil {
		a.toast(locales.TemplateSaveFailed(err.Error()))
		return
	}
	a.openPage(p.ID)
}

func (a *App) templateBlocks(t templates.Choice, id string, again func(), use func([]*doc.Node)) bool {
	if t.Starter != nil {
		use(t.Starter.Nodes())
		return true
	}
	if templates.RecordingIn(a.st, a.rec, t.Page.ID, a.isUnder) {
		a.toast(locales.TemplateWaitForRecording())
		return false
	}
	open := func() bool { return a.page != nil && a.page.id == id }
	failed := func(err error) { a.toast(locales.TemplateUseFailed(err.Error())) }
	if !a.copyable(t.Page.ID, failed, func() {
		if open() {
			again()
		}
	}) {
		return false
	}
	var blocks []*doc.Node
	return a.copyPage(failed, func() (err error) { blocks, err = templates.Blocks(a.st, t, id); return }, func() {
		reread(&a.pages, a.st.Pages)
		if len(a.children(id)) > 0 {
			a.treeOpen("page", id)
		}
		if open() {
			use(blocks)
			return
		}
		if len(blocks) == 0 {
			return
		}
		if err := a.st.UpdateContent(id, func(c *doc.Content, _ bool) (bool, error) {
			if c.IsEmpty() {
				c.Children = blocks
			} else {
				c.Children = append(c.Children, blocks...)
			}
			return true, nil
		}); err != nil {
			failed(err)
		}
	})
}

func (a *App) applyTemplate(t templates.Choice) bool {
	ps := a.page
	if ps == nil || ps.editor.ReadOnly || !ps.editor.Content.IsEmpty() {
		return false
	}
	return a.templateBlocks(t, ps.id, func() { a.applyTemplate(t) }, func(blocks []*doc.Node) {
		ps := a.page
		switch {
		case len(blocks) == 0:
		case ps.editor.Content.IsEmpty():
			ps.editor.Edit(func(cur *doc.Content) { cur.Children = blocks })
		default:
			ps.editor.InsertBlocks(blocks...)
		}
		if len(blocks) > 0 {
			a.savePage()
		}
		took := t.Retitles(ps.title)
		if took {
			ps.title = t.Name()
			a.saveTitle()
		}
		a.focusTitle(took)
	})
}

func (a *App) insertTemplate(t templates.Choice) {
	ps := a.page
	if ps == nil {
		return
	}
	if ps.editor.Content.IsEmpty() {
		a.applyTemplate(t)
		return
	}
	a.templateBlocks(t, ps.id, func() { a.insertTemplate(t) }, func(blocks []*doc.Node) {
		a.page.editor.InsertBlocks(blocks...)
		a.savePage()
	})
}

func (a *App) startFrom(t *templates.Choice) {
	if t != nil && a.applyTemplate(*t) {
		return
	}
	a.focusTitle(false)
}

func (a *App) defaultTemplate(parent, folder string) *templates.Choice {
	if t, ok := templates.Default(a.st, a.templateChoices(), parent, folder); ok {
		return &t
	}
	return nil
}

func (a *App) defaultTemplateMenu(m *ui.Menu, label, place, current string, disabled bool, set func(id string) error) {
	if _, ok := a.templateByKey("template:" + current); !ok {
		current = ""
	}
	choose := func(t *templates.Choice) {
		done, err := templates.SetDefault(a.st, t, place, set)
		a.reloadTemplates()
		if err != nil {
			a.toast(locales.TemplateDefaultFailed(err.Error()))
			return
		}
		reread(&a.pages, a.st.Pages)
		reread(&a.folders, a.st.Folders)
		a.toast(done)
	}
	m.Submenu(label, func(m *ui.Menu) {
		if m.Item(locales.TemplateDefaultBlank()).Radio(current == "").Chosen() {
			choose(nil)
		}
		m.Separator()
		for _, t := range a.templateChoices() {
			if m.Item(t.Title()).Radio(t.Page != nil && t.Page.ID == current).Chosen() {
				choose(&t)
			}
		}
	}).Disabled(disabled)
}

func templatePill(c *ui.Context) { accentBadge(c, locales.TemplatePill()) }

func (a *App) templateActions(c *ui.Context, row func(key, icon, label, hint string, fn func(), menu func(m *ui.Menu))) {
	pal := theme.Of(c)
	choices := a.templateChoices()
	if len(choices) == 0 {
		return
	}
	ui.Text(c, locales.TemplateSectionTitle()).FontSize(12).FontWeight(600).TextColor(pal.TextMuted).Margin(10, 0, 4, 2)
	for _, t := range choices {
		row(t.Key(), t.Icon(), t.Title(), t.Hint(a.templateHint), func() { a.applyTemplate(t) }, func(m *ui.Menu) {
			if m.Item(locales.TemplateEdit()).Chosen() {
				a.editTemplate(t)
			}
			if m.Item(locales.TemplateDelete()).Danger().Chosen() {
				a.deleteTemplate(t)
			}
		})
	}
}

func (a *App) templateHint(t store.Page) string {
	key := t.ID + "@" + t.UpdatedAt.String() + "@" + i18n.Locale()
	if h, ok := a.templateHints[key]; ok {
		return h
	}
	hint := templates.Hint(a.st, t, templates.Subpages(a.templates))
	if a.templateHints == nil {
		a.templateHints = map[string]string{}
	}
	a.templateHints[key] = hint
	return hint
}

func (a *App) searchHits(query string) []store.SearchHit {
	hits, _ := a.st.Search(query, 30)
	return a.withStarters(query, hits)
}

func (a *App) search(query string) {
	d := &a.dialogs
	d.searches++
	d.submitted = false
	n := d.searches
	var hits []store.SearchHit
	a.offMain(func() { hits, _ = a.st.Search(query, 30) }, func() {
		if n != d.searches {
			return
		}
		d.hits, d.hitIndex, d.searched = a.withStarters(query, hits), 0, n
		if d.submitted && d.searchOpen {
			d.submitted = false
			a.submitSearch()
		}
	})
}

func (a *App) withStarters(query string, hits []store.SearchHit) []store.SearchHit {
	filters, rest := store.SplitTagQuery(query)
	q := strings.ToLower(strings.TrimSpace(rest))
	if q == "" || len(filters) > 0 {
		return hits
	}
	for _, t := range a.templateChoices() {
		if t.Starter != nil && strings.Contains(strings.ToLower(t.Title()), q) {
			hits = append(hits, store.SearchHit{Kind: "template", Title: t.Title(), PageID: t.Key(), Snippet: t.Starter.Hint()})
		}
	}
	return hits
}

func (a *App) hitTemplate(h store.SearchHit) (templates.Choice, bool) {
	if h.Kind != "template" {
		return templates.Choice{}, false
	}
	if t, ok := a.templateByKey(h.PageID); ok {
		return t, true
	}
	return a.templateByKey("template:" + h.PageID)
}
