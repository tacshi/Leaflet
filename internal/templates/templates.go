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

type Choice struct {
	Page    *store.Page
	Starter *starters.Starter
}

func (t Choice) Key() string {
	if t.Page != nil {
		return "template:" + t.Page.ID
	}
	return "starter:" + t.Starter.Key
}

func (t Choice) Name() string {
	if t.Page != nil {
		return t.Page.Title
	}
	return t.Starter.Title()
}

func (t Choice) Title() string { return store.TitleOr(t.Name()) }

func (t Choice) Icon() string {
	if t.Page != nil {
		return "layout-template"
	}
	return t.Starter.Icon
}

func (t Choice) Hint(page func(store.Page) string) string {
	if t.Page != nil {
		return page(*t.Page)
	}
	return t.Starter.Hint()
}

func (t Choice) Retitles(title string) bool {
	return strings.TrimSpace(title) == "" && t.Name() != ""
}

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

func Find(choices []Choice, key string) (Choice, bool) {
	i := slices.IndexFunc(choices, func(t Choice) bool { return t.Key() == key })
	if i < 0 {
		return Choice{}, false
	}
	return choices[i], true
}

const deletedStartersKey = "templates.deleted_starters"

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

func Subpages(all []store.Page) map[string]int {
	n := map[string]int{}
	for _, t := range all {
		if t.ParentID != "" {
			n[t.ParentID]++
		}
	}
	return n
}

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
