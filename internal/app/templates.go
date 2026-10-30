package app

import (
	"encoding/json"
	"maps"
	"slices"
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
	// A copy without the edits waiting would lose them; the save said why.
	if !a.flush() {
		return
	}
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
	// A copy without the edits waiting would lose them; the save said why.
	if !a.flush() {
		return
	}
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

// A starter is a template that comes with Leaflet, in the app's language
// when it is used. It shows until the user has a template of its title, as
// editing it makes.
type starter struct {
	key, icon   string
	title, hint func() string
	blocks      func() []map[string]any
}

// starters are the templates that come with Leaflet.
func starters() []starter {
	heading := func(s string) map[string]any { return map[string]any{"type": "heading", "level": 2.0, "text": s} }
	empty := func(typ string) map[string]any { return map[string]any{"type": typ, "text": ""} }
	return []starter{
		{"meeting", "calendar", locales.TemplateStarterMeeting, locales.TemplateStarterMeetingHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterAgenda()), empty("numbered_list"),
				heading(locales.TemplateStarterNotes()), empty("bulleted_list"),
				heading(locales.TemplateStarterActionItems()), empty("todo_list"),
			}
		}},
		{"todo", "square-check", locales.TemplateStarterTodo, locales.TemplateStarterTodoHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterToday()), empty("todo_list"),
				heading(locales.TemplateStarterThisWeek()), empty("todo_list"),
				heading(locales.TemplateStarterLater()), empty("todo_list"),
			}
		}},
		{"project", "lightbulb", locales.TemplateStarterProject, locales.TemplateStarterProjectHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterOverview()), empty("paragraph"),
				heading(locales.TemplateStarterGoals()), empty("bulleted_list"),
				heading(locales.TemplateStarterMilestones()),
				{"type": "table", "rows": []any{
					[]any{locales.TemplateStarterMilestone(), locales.TemplateStarterOwner(), locales.TemplateStarterDue()},
					[]any{"", "", ""},
				}},
				heading(locales.TemplateStarterQuestions()), empty("bulleted_list"),
			}
		}},
		{"journal", "square-pen", locales.TemplateStarterJournal, locales.TemplateStarterJournalHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterHighlights()), empty("bulleted_list"),
				heading(locales.TemplateStarterGrateful()), empty("numbered_list"),
				heading(locales.TemplateStarterTomorrow()), empty("todo_list"),
			}
		}},
	}
}

// nodes are a starter's blocks.
func (s starter) nodes() []*doc.Node {
	var out []*doc.Node
	for _, spec := range s.blocks() {
		if n, err := doc.SpecToNode(spec, "", "Leaflet"); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// A templateChoice is a template to start a page from: one of the user's,
// or a starter.
type templateChoice struct {
	page    *store.Page
	starter *starter
}

// key tells choices apart in menus and lists.
func (t templateChoice) key() string {
	if t.page != nil {
		return "template:" + t.page.ID
	}
	return "starter:" + t.starter.key
}

// name is the title a page takes from the template, "" for none.
func (t templateChoice) name() string {
	if t.page != nil {
		return t.page.Title
	}
	return t.starter.title()
}

func (t templateChoice) title() string { return store.TitleOr(t.name()) }

func (t templateChoice) icon() string {
	if t.page != nil {
		return "layout-template"
	}
	return t.starter.icon
}

// choiceHint tells a template apart under its name.
func (a *App) choiceHint(t templateChoice) string {
	if t.page != nil {
		return a.templateHint(*t.page)
	}
	return t.starter.hint()
}

// templateChoices are the user's templates, by title, then the starters
// not deleted that they have no template of the same title for.
func (a *App) templateChoices() []templateChoice {
	roots := a.templateRoots()
	titles := map[string]bool{}
	var out []templateChoice
	for i := range roots {
		titles[strings.ToLower(strings.TrimSpace(roots[i].Title))] = true
		out = append(out, templateChoice{page: &roots[i]})
	}
	gone := a.deletedStarters()
	ss := starters()
	for i := range ss {
		if !gone[ss[i].key] && !titles[strings.ToLower(ss[i].title())] {
			out = append(out, templateChoice{starter: &ss[i]})
		}
	}
	return out
}

// deletedStartersKey keeps the keys of the starters deleted on this
// device, a JSON array.
const deletedStartersKey = "templates.deleted_starters"

// deletedStarters are the starters deleted on this device, by key, read
// once and kept as they change.
func (a *App) deletedStarters() map[string]bool {
	if a.goneStarters == nil {
		a.goneStarters = map[string]bool{}
		var keys []string
		if v, ok := a.st.Value(deletedStartersKey); ok {
			_ = json.Unmarshal([]byte(v), &keys)
		}
		for _, k := range keys {
			a.goneStarters[k] = true
		}
	}
	return a.goneStarters
}

// setStarterDeleted deletes a starter on this device, or brings it back.
func (a *App) setStarterDeleted(key string, deleted bool) error {
	gone := maps.Clone(a.deletedStarters())
	if deleted {
		gone[key] = true
	} else {
		delete(gone, key)
	}
	b, _ := json.Marshal(slices.Sorted(maps.Keys(gone)))
	if err := a.st.SetValue(deletedStartersKey, string(b)); err != nil {
		return err
	}
	a.goneStarters = gone
	return nil
}

// deleteTemplate deletes a template, with Undo: a starter it hides here.
func (a *App) deleteTemplate(t templateChoice) {
	if t.page != nil {
		a.deletePage(t.page.ID)
		return
	}
	key := t.starter.key
	if err := a.setStarterDeleted(key, true); err != nil {
		a.toast(locales.AppErrorDelete(err.Error()))
		return
	}
	a.toastAction(locales.AppDeleted(t.title()), locales.CommonUndo(), func() {
		if err := a.setStarterDeleted(key, false); err != nil {
			a.toast(locales.AppErrorRestore(err.Error()))
		}
	})
}

// templateByKey finds a template by its key.
func (a *App) templateByKey(key string) (templateChoice, bool) {
	for _, t := range a.templateChoices() {
		if t.key() == key {
			return t, true
		}
	}
	return templateChoice{}, false
}

// ownTemplate is the user's template for t: t itself, or for a starter a
// template made from it, which then takes the starter's place: deleting
// the template later doesn't bring the starter back.
func (a *App) ownTemplate(t templateChoice) (store.Page, error) {
	if t.page != nil {
		return *t.page, nil
	}
	p, err := a.st.CreatePage(store.NewPageOptions{Title: t.starter.title(), Template: true, Content: doc.FromBlocks(t.starter.nodes())})
	if err != nil {
		return store.Page{}, err
	}
	a.templates, _ = a.st.Templates()
	return p, a.setStarterDeleted(t.starter.key, true)
}

// editTemplate opens a template to change it; a starter becomes the
// user's own first.
func (a *App) editTemplate(t templateChoice) {
	p, err := a.ownTemplate(t)
	if err != nil {
		a.toast(locales.TemplateSaveFailed(err.Error()))
		return
	}
	a.openPage(p.ID)
}

// templateBlocks are the blocks page id takes from a template: a
// starter's, or a copy of a template's, whose subpages it copies under the
// page. When it has none to give it says why, and reports false.
func (a *App) templateBlocks(t templateChoice, id string) ([]*doc.Node, bool) {
	if t.starter != nil {
		return t.starter.nodes(), true
	}
	// A copy would keep a recording still being made in the template
	// unfinished, as duplicatePage's would.
	if a.recordingIn(t.page.ID) {
		a.toast(locales.TemplateWaitForRecording())
		return nil, false
	}
	c, err := a.st.CopyInto(t.page.ID, id)
	if err != nil {
		a.toast(locales.TemplateUseFailed(err.Error()))
		return nil, false
	}
	a.pages, _ = a.st.Pages()
	if len(a.children(id, "", false)) > 0 {
		a.treeOpen("page", id)
	}
	return c.Children, true
}

// applyTemplate fills the open, empty page from a template: its content,
// as one undo step, and copies of its subpages. An untitled page takes the
// template's title, selected to type over, and applyTemplate reports
// whether it did. A page with something in it already, or that can't be
// changed, is left as it is.
func (a *App) applyTemplate(t templateChoice) bool {
	ps := a.page
	if ps == nil || ps.editor.ReadOnly || !a.isEmpty() {
		return false
	}
	blocks, ok := a.templateBlocks(t, ps.id)
	if !ok {
		return false
	}
	// A template of no blocks leaves the page its empty paragraph.
	if len(blocks) > 0 {
		ps.editor.Edit(func(cur *doc.Content) { cur.Children = blocks })
		a.savePage()
	}
	if strings.TrimSpace(ps.title) != "" || t.name() == "" {
		return false
	}
	ps.title = t.name()
	a.saveTitle()
	a.focusTitle(true)
	return true
}

// insertTemplate puts a template's blocks at the caret, as / does, with
// copies of its subpages under the page. An empty page it fills as a blank
// page's row does.
func (a *App) insertTemplate(t templateChoice) {
	ps := a.page
	if ps == nil {
		return
	}
	if a.isEmpty() {
		a.applyTemplate(t)
		return
	}
	blocks, ok := a.templateBlocks(t, ps.id)
	if !ok {
		return
	}
	ps.editor.InsertBlocks(blocks...)
	a.savePage()
}

// startFrom fills the page just made and opened from a template, if there
// is one, and puts the caret in its title.
func (a *App) startFrom(t *templateChoice) {
	if t != nil && a.applyTemplate(*t) {
		return
	}
	a.focusTitle(false)
}

// defaultTemplate is the template new pages under parent, or at the top of
// folder, start from: the one the page or folder names, if the user has
// it; else nil.
func (a *App) defaultTemplate(parent, folder string) *templateChoice {
	id := ""
	switch {
	case parent != "":
		if p := a.pageByID(parent); p != nil {
			id = p.DefaultTemplateID
		}
	case folder != "":
		for _, f := range a.folders {
			if f.ID == folder {
				id = f.DefaultTemplateID
			}
		}
	}
	if id == "" {
		return nil
	}
	if t, ok := a.templateByKey("template:" + id); ok {
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
	choose := func(t *templateChoice) {
		id, done := "", locales.TemplateDefaultCleared(place)
		if t != nil {
			p, err := a.ownTemplate(*t)
			if err != nil {
				a.toast(locales.TemplateDefaultFailed(err.Error()))
				return
			}
			id, done = p.ID, locales.TemplateDefaultSet(place, t.title())
		}
		if err := set(id); err != nil {
			a.toast(locales.TemplateDefaultFailed(err.Error()))
			return
		}
		a.pages, _ = a.st.Pages()
		a.folders, _ = a.st.Folders()
		a.toast(done)
	}
	m.Submenu(label, func(m *ui.Menu) {
		if m.Item(locales.TemplateDefaultBlank()).Radio(current == "").Chosen() {
			choose(nil)
		}
		m.Separator()
		for _, t := range a.templateChoices() {
			if m.Item(t.title()).Radio(t.page != nil && t.page.ID == current).Chosen() {
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
		row(t.key(), t.icon(), t.title(), a.choiceHint(t), func() { a.applyTemplate(t) }, func(m *ui.Menu) {
			if m.Item(locales.TemplateEdit()).Chosen() {
				a.editTemplate(t)
			}
			if m.Item(locales.TemplateDelete()).Chosen() {
				a.deleteTemplate(t)
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

// searchHits are what search lists for a query: the store's hits, and the
// starters whose titles match it, whose PageID is their key.
func (a *App) searchHits(query string) []store.SearchHit {
	hits, _ := a.st.Search(query, 30)
	filters, rest := store.SplitTagQuery(query)
	q := strings.ToLower(strings.TrimSpace(rest))
	if q == "" || len(filters) > 0 {
		return hits
	}
	for _, t := range a.templateChoices() {
		if t.starter != nil && strings.Contains(strings.ToLower(t.title()), q) {
			hits = append(hits, store.SearchHit{Kind: "template", Title: t.title(), PageID: t.key(), Snippet: t.starter.hint()})
		}
	}
	return hits
}

// hitTemplate is the template a search hit starts a page from: a starter,
// or a template itself rather than one of its subpages.
func (a *App) hitTemplate(h store.SearchHit) (templateChoice, bool) {
	if h.Kind != "template" {
		return templateChoice{}, false
	}
	if t, ok := a.templateByKey(h.PageID); ok {
		return t, true
	}
	return a.templateByKey("template:" + h.PageID)
}
