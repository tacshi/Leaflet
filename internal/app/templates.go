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

// duplicatePage copies a page and its subpages next to it, links the copy
// where the page is linked, and opens it. A page shared with the user
// that they can't add to the parent of is copied into their own space.
func (a *App) duplicatePage(p store.Page) {
	// A copy without the edits waiting would lose them; the save said why.
	if !a.flush() {
		return
	}
	if templates.RecordingIn(a.st, a.rec, p.ID, a.isUnder) {
		a.toast(locales.TemplateWaitForRecording())
		return
	}
	failed := func(err error) { a.toast(locales.TemplateDuplicateFailed(err.Error())) }
	// Again once what it needs has come: as the page is then, which may
	// have moved or gone meanwhile.
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

// again runs fn on page id as it is now, unless it is gone: not on this
// device, or deleted.
func (a *App) again(id string, fn func(store.Page)) {
	if p, err := a.st.Page(id); err == nil && p.DeletedAt.IsZero() {
		fn(p)
	}
}

// copyPage makes a copy of pages off the main thread, as work, which copies
// their files and recordings' audio, which a long recording takes seconds
// for. done runs once it has, on the main thread, or failed with why it
// did not. One copy is made at a time, as one downloads at a time: another
// is not begun, and copyPage reports whether this was. The audio fetched
// for the copy being made stays until it has its own.
func (a *App) copyPage(failed func(error), work func() error, done func()) bool {
	if a.fetchingCopy {
		a.toast(locales.TemplateFetchingOther())
		return false
	}
	a.fetchingCopy = true
	var err error
	a.offMain(func() { err = work() }, func() {
		a.fetchingCopy = false
		// The audio that came for the copy is in it now, or not needed.
		a.dropFetchedAudio()
		if err != nil {
			a.copyFailed(err, failed)
			return
		}
		done()
	})
	return true
}

// dropFetchedAudio removes the audio copyable downloaded for a copy, once
// the copy has its own or will not be made, as Keep the audio says.
func (a *App) dropFetchedAudio() {
	templates.DropAudio(a.st, a.settings, a.fetchedAudio)
	a.fetchedAudio = nil
}

// copyable reports whether page id, with its subpages, can be copied now,
// with what it needs on this device (templates.Needs). Those only stored
// download first, and again runs once they have, on the main thread; one
// still uploading from where it was added, or that can't download now,
// says so, and nothing is copied.
func (a *App) copyable(id string, failed func(error), again func()) bool {
	needs, err := templates.Needs(a.st, a.sync, id)
	if err != nil {
		a.copyFailed(err, failed)
		return false
	}
	if len(needs) == 0 {
		return true
	}
	// One copy's downloads at a time: the toast says they are coming.
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
			// What came goes once the copy has its own, or at once when it
			// will not be made.
			a.fetchedAudio = audio
			if err != nil {
				log.Printf("copy: %v", err)
				a.dropFetchedAudio()
				a.copyFailed(err, failed)
				return
			}
			again()
			if !a.fetchingCopy {
				a.dropFetchedAudio() // no copy began
			}
		})
	}()
	return false
}

// copyFailed says why a copy was not made (templates.CopyError): a file or
// a recording's audio not on this device, or what else failed.
func (a *App) copyFailed(err error, failed func(error)) {
	if err = templates.CopyError(err); errors.Is(err, templates.ErrFilesUploading) {
		a.toast(err.Error())
		return
	}
	failed(err)
}

// saveAsTemplate keeps a copy of a page, with its subpages, as a template
// to start other pages from.
func (a *App) saveAsTemplate(p store.Page) {
	// A copy without the edits waiting would lose them; the save said why.
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

// templateChoices are the templates to start a page from: the user's, by
// title, then the starters (templates.Choices).
func (a *App) templateChoices() []templates.Choice {
	return templates.Choices(a.templates, a.deletedStarters())
}

// deletedStarters are the starters deleted on this device, by key, read
// once and again as they change.
func (a *App) deletedStarters() map[string]bool {
	if a.goneStarters == nil {
		a.goneStarters = templates.DeletedStarters(a.st)
	}
	return a.goneStarters
}

// reloadTemplates reads the templates and the starters deleted again, as
// a starter made the user's own changes both.
func (a *App) reloadTemplates() {
	reread(&a.templates, a.st.Templates)
	a.goneStarters = nil
}

// deleteTemplate deletes a template, with Undo: a starter it hides here.
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

// templateByKey finds a template by its key.
func (a *App) templateByKey(key string) (templates.Choice, bool) {
	return templates.Find(a.templateChoices(), key)
}

// editTemplate opens a template to change it; a starter becomes the
// user's own first.
func (a *App) editTemplate(t templates.Choice) {
	p, err := templates.Own(a.st, t)
	a.reloadTemplates()
	if err != nil {
		a.toast(locales.TemplateSaveFailed(err.Error()))
		return
	}
	a.openPage(p.ID)
}

// templateBlocks gives use the blocks page id takes from a template, on
// the main thread: a starter's at once, or a copy's of a template, whose
// subpages it copies under the page, once the copy is made off the main
// thread, if the page is still open; a page closed meanwhile takes them
// as stored. When it has none to give it says why, and reports false.
func (a *App) templateBlocks(t templates.Choice, id string, again func(), use func([]*doc.Node)) bool {
	if t.Starter != nil {
		use(t.Starter.Nodes())
		return true
	}
	// A copy would keep a recording still being made in the template
	// unfinished, as duplicatePage's would.
	if templates.RecordingIn(a.st, a.rec, t.Page.ID, a.isUnder) {
		a.toast(locales.TemplateWaitForRecording())
		return false
	}
	open := func() bool { return a.page != nil && a.page.id == id }
	failed := func(err error) { a.toast(locales.TemplateUseFailed(err.Error())) }
	// What the template's files and recordings need first comes, and the
	// page takes the template once it has, if it is still open.
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
		// The page closed while the copy was made, which put the
		// template's subpages under it already: it takes the blocks as
		// stored, so they are not left without them.
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

// applyTemplate fills the open, empty page from a template: its content,
// as one undo step, and copies of its subpages. An untitled page takes the
// template's title. applyTemplate reports whether it fills the page, now
// or once its copies are made, and then puts the caret in the title:
// selected to type over when it took the template's. A page with
// something in it already, or that can't be changed, is left as it is.
func (a *App) applyTemplate(t templates.Choice) bool {
	ps := a.page
	if ps == nil || ps.editor.ReadOnly || !ps.editor.Content.IsEmpty() {
		return false
	}
	return a.templateBlocks(t, ps.id, func() { a.applyTemplate(t) }, func(blocks []*doc.Node) {
		ps := a.page
		switch {
		case len(blocks) == 0:
			// A template of no blocks leaves the page its empty paragraph.
		case ps.editor.Content.IsEmpty():
			ps.editor.Edit(func(cur *doc.Content) { cur.Children = blocks })
		default:
			// What was typed while the copy was made stays.
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

// insertTemplate puts a template's blocks at the caret, as / does, with
// copies of its subpages under the page. An empty page it fills as a blank
// page's row does.
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

// startFrom fills the page just made and opened from a template, if there
// is one, and puts the caret in its title.
func (a *App) startFrom(t *templates.Choice) {
	if t != nil && a.applyTemplate(*t) {
		return
	}
	a.focusTitle(false)
}

// defaultTemplate is the template new pages under parent, or at the top of
// folder, start from (templates.Default); else nil.
func (a *App) defaultTemplate(parent, folder string) *templates.Choice {
	if t, ok := templates.Default(a.st, a.templateChoices(), parent, folder); ok {
		return &t
	}
	return nil
}

// defaultTemplateMenu is a submenu choosing the template new pages in a
// place start from: place is its name, current the template's ID, and set
// stores a choice, "" for blank pages. A starter chosen becomes the user's
// template first.
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

// templatePill marks a template, where it shows among pages.
func templatePill(c *ui.Context) { accentBadge(c, locales.TemplatePill()) }

// templateActions list the templates under a blank page's other ways to
// start, each with a menu to edit or delete it: the user's, and the
// starters, which editing makes the user's own.
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

// templateHint tells a template apart (templates.Hint), kept until the
// template next changes or the app's language does.
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

// searchHits are what search lists for a query: the store's hits, and the
// starters whose titles match it, whose PageID is their key.
func (a *App) searchHits(query string) []store.SearchHit {
	hits, _ := a.st.Search(query, 30)
	return a.withStarters(query, hits)
}

// search has the search dialog show what query finds, read off the main
// thread: it reads every page's text, which a large library takes a while
// for, at each key. The answer to a search asked for before the last is
// dropped.
func (a *App) search(query string) {
	d := &a.dialogs
	d.searches++
	n := d.searches
	var hits []store.SearchHit
	a.offMain(func() { hits, _ = a.st.Search(query, 30) }, func() {
		if n == d.searches {
			d.hits, d.hitIndex = a.withStarters(query, hits), 0
		}
	})
}

// withStarters is hits with the starter templates query finds, on the main
// thread, where the templates are.
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

// hitTemplate is the template a search hit starts a page from: a starter,
// or a template itself rather than one of its subpages.
func (a *App) hitTemplate(h store.SearchHit) (templates.Choice, bool) {
	if h.Kind != "template" {
		return templates.Choice{}, false
	}
	if t, ok := a.templateByKey(h.PageID); ok {
		return t, true
	}
	return a.templateByKey("template:" + h.PageID)
}
