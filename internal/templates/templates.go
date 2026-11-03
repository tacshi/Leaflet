// Package templates is what starting pages from templates and copying
// pages take, for the desktop and the iOS app alike: the templates to
// start a page from, the user's own and the starters that come with
// Leaflet (internal/starters), and the copies of pages that duplicating a
// page, saving it as a template and using a template make. Each app asks
// for them and says what came of them in its own way.
package templates

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
	"leaflet/internal/starters"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A Choice is a template to start a page from: one of the user's, or a
// starter.
type Choice struct {
	Page    *store.Page
	Starter *starters.Starter
}

// Key tells choices apart in menus and lists: "template:" and the
// template's page ID, or "starter:" and the starter's key.
func (t Choice) Key() string {
	if t.Page != nil {
		return "template:" + t.Page.ID
	}
	return "starter:" + t.Starter.Key
}

// Name is the title a page takes from the template, "" for none.
func (t Choice) Name() string {
	if t.Page != nil {
		return t.Page.Title
	}
	return t.Starter.Title()
}

// Title is the template's name as lists show it.
func (t Choice) Title() string { return store.TitleOr(t.Name()) }

// Icon is a Lucide icon's name: the starter's own, or "layout-template"
// for the user's.
func (t Choice) Icon() string {
	if t.Page != nil {
		return "layout-template"
	}
	return t.Starter.Icon
}

// Hint tells the template apart under its name: a starter by its
// description, one of the user's by what page says of it (Hint).
func (t Choice) Hint(page func(store.Page) string) string {
	if t.Page != nil {
		return page(*t.Page)
	}
	return t.Starter.Hint()
}

// Retitles reports whether a page of title takes the template's name, as
// an untitled page does.
func (t Choice) Retitles(title string) bool {
	return strings.TrimSpace(title) == "" && t.Name() != ""
}

// Choices are the user's templates, by title, then the starters not gone
// (DeletedStarters) that they have no template of the same title for. all
// are the templates with their subpages, as store.Templates reads them.
func Choices(all []store.Page, gone map[string]bool) []Choice {
	var roots []store.Page
	for _, t := range all {
		if t.ParentID == "" {
			roots = append(roots, t)
		}
	}
	slices.SortStableFunc(roots, func(x, y store.Page) int {
		return cmp.Compare(strings.ToLower(x.DisplayTitle()), strings.ToLower(y.DisplayTitle()))
	})
	titles := map[string]bool{}
	var out []Choice
	for i := range roots {
		titles[strings.ToLower(strings.TrimSpace(roots[i].Title))] = true
		out = append(out, Choice{Page: &roots[i]})
	}
	ss := starters.All()
	for i := range ss {
		if !gone[ss[i].Key] && !titles[strings.ToLower(ss[i].Title())] {
			out = append(out, Choice{Starter: &ss[i]})
		}
	}
	return out
}

// Find is the choice of key among choices.
func Find(choices []Choice, key string) (Choice, bool) {
	i := slices.IndexFunc(choices, func(t Choice) bool { return t.Key() == key })
	if i < 0 {
		return Choice{}, false
	}
	return choices[i], true
}

// deletedStartersKey keeps the keys of the starters deleted on this
// device, a JSON array.
const deletedStartersKey = "templates.deleted_starters"

// DeletedStarters are the starters deleted on this device, by key.
func DeletedStarters(st *store.Store) map[string]bool {
	gone := map[string]bool{}
	var keys []string
	if v, ok := st.Value(deletedStartersKey); ok {
		_ = json.Unmarshal([]byte(v), &keys)
	}
	for _, k := range keys {
		gone[k] = true
	}
	return gone
}

// SetStarterDeleted deletes starter key on this device, or brings it
// back.
func SetStarterDeleted(st *store.Store, key string, deleted bool) error {
	gone := DeletedStarters(st)
	if deleted {
		gone[key] = true
	} else {
		delete(gone, key)
	}
	b, _ := json.Marshal(slices.Sorted(maps.Keys(gone)))
	return st.SetValue(deletedStartersKey, string(b))
}

// Own is the user's template for t: t itself, or for a starter a template
// made from it, which then takes the starter's place: deleting the
// template later doesn't bring the starter back.
func Own(st *store.Store, t Choice) (store.Page, error) {
	if t.Page != nil {
		return *t.Page, nil
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: t.Starter.Title(), Template: true, Content: doc.FromBlocks(t.Starter.Nodes())})
	if err != nil {
		return store.Page{}, err
	}
	return p, SetStarterDeleted(st, t.Starter.Key, true)
}

// Subpages counts the subpages of each of the templates all, by its ID.
func Subpages(all []store.Page) map[string]int {
	n := map[string]int{}
	for _, t := range all {
		if t.ParentID != "" {
			n[t.ParentID]++
		}
	}
	return n
}

// Hint is the first line of template t's text, to tell templates apart,
// or, when it has no text, how many subpages it has, as subpages counts
// them (Subpages).
func Hint(st *store.Store, t store.Page, subpages map[string]int) string {
	if row, err := st.ContentRow(t.ID); err == nil {
		for l := range strings.SplitSeq(row.PlainText, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
	}
	if n := subpages[t.ID]; n > 0 {
		return locales.TemplateHintSubpages(n)
	}
	return locales.TemplateHintEmpty()
}

// Items are the templates the / menu offers in page pageID, for
// menu.Slash: choices but the page's own, as a template doesn't go into
// itself. hint tells the user's apart (Hint).
func Items(choices []Choice, pageID string, hint func(store.Page) string) []menu.Item {
	var out []menu.Item
	for _, t := range choices {
		if t.Page != nil && t.Page.ID == pageID {
			continue
		}
		out = append(out, menu.Item{Key: t.Key(), Label: t.Title(), Hint: t.Hint(hint), Icon: t.Icon()})
	}
	return out
}

// Default is the template new pages under page parent, or at the top of
// folder, start from: the one the page or the folder names, if the user
// has it among choices.
func Default(st *store.Store, choices []Choice, parent, folder string) (Choice, bool) {
	id := ""
	switch {
	case parent != "":
		if p, err := st.Page(parent); err == nil {
			id = p.DefaultTemplateID
		}
	case folder != "":
		if f, err := st.Folder(folder); err == nil {
			id = f.DefaultTemplateID
		}
	}
	if id == "" {
		return Choice{}, false
	}
	return Find(choices, "template:"+id)
}

// SetDefault chooses with set the template new pages in a place start
// from: t, a starter made the user's own template first (Own), or blank
// pages for nil. It returns the message that says so, place naming where.
func SetDefault(st *store.Store, t *Choice, place string, set func(id string) error) (string, error) {
	if t == nil {
		return locales.TemplateDefaultCleared(place), set("")
	}
	p, err := Own(st, *t)
	if err != nil {
		return "", err
	}
	if err := set(p.ID); err != nil {
		return "", err
	}
	return locales.TemplateDefaultSet(place, t.Title()), nil
}

// Blocks are the blocks page id takes from template t: a starter's, or a
// copy's of the user's template, whose subpages it copies under the page
// (store.CopyInto). A copy copies files and recordings' audio, which a
// long recording takes seconds for.
func Blocks(st *store.Store, t Choice, id string) ([]*doc.Node, error) {
	if t.Starter != nil {
		return t.Starter.Nodes(), nil
	}
	c, err := st.CopyInto(t.Page.ID, id)
	if err != nil {
		return nil, err
	}
	return c.Children, nil
}
