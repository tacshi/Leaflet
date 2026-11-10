package library

import (
	"cmp"
	"slices"
	"strings"

	"leaflet/internal/store"
)

type Tree struct {
	Folders  []store.Folder
	Top      []store.Page
	InFolder map[string][]store.Page
	Children map[string][]store.Page
	Shared   []store.Page
	Tags     []store.TagCount

	folders    map[string]bool
	parentGone map[string]bool
	tagged     map[string][]store.Page
}

func Arrange(spaceID string, pages []store.Page, folders []store.Folder) *Tree {
	t := &Tree{InFolder: map[string][]store.Page{}, Children: map[string][]store.Page{}, folders: map[string]bool{},
		parentGone: map[string]bool{}}
	for _, f := range folders {
		if f.SpaceID == spaceID {
			t.Folders = append(t.Folders, f)
			t.folders[f.ID] = true
		}
	}
	slices.SortStableFunc(t.Folders, func(a, b store.Folder) int {
		return cmp.Or(strings.Compare(a.SortKey, b.SortKey), strings.Compare(a.ID, b.ID))
	})
	have := make(map[string]bool, len(pages))
	for _, p := range pages {
		have[p.ID] = true
	}
	ordered := slices.Clone(pages)
	slices.SortStableFunc(ordered, func(a, b store.Page) int {
		return cmp.Or(strings.Compare(a.SortKey, b.SortKey), strings.Compare(a.ID, b.ID))
	})
	for _, p := range ordered {
		shared := p.SpaceID != spaceID
		parentGone := p.ParentID != "" && !have[p.ParentID] && !p.Template && !shared
		switch {
		case p.ParentID != "" && !parentGone:
			t.Children[p.ParentID] = append(t.Children[p.ParentID], p)
		case p.Template || shared:
		case t.folders[p.FolderID]:
			t.InFolder[p.FolderID] = append(t.InFolder[p.FolderID], p)
		default:
			t.Top = append(t.Top, p)
		}
		if parentGone {
			t.parentGone[p.ID] = true
		}
		if shared && !p.Template && (p.ParentID == "" || !have[p.ParentID]) {
			t.Shared = append(t.Shared, p)
		}
	}
	t.tag(pages)
	return t
}

func (t *Tree) tag(pages []store.Page) {
	var kept []store.Page
	t.tagged = map[string][]store.Page{}
	for _, p := range pages {
		if p.Template {
			continue
		}
		kept = append(kept, p)
		for _, tag := range p.Tags {
			k := strings.ToLower(tag)
			if have := t.tagged[k]; len(have) == 0 || have[len(have)-1].ID != p.ID {
				t.tagged[k] = append(have, p)
			}
		}
	}
	for _, have := range t.tagged {
		slices.SortStableFunc(have, func(a, b store.Page) int {
			return strings.Compare(strings.ToLower(a.DisplayTitle()), strings.ToLower(b.DisplayTitle()))
		})
	}
	t.Tags = store.TagsOf(kept)
}

func (t *Tree) Tagged(tag string) []store.Page { return t.tagged[strings.ToLower(tag)] }

func (t *Tree) HasFolder(id string) bool { return t.folders[id] }

func (t *Tree) Siblings(p store.Page) []store.Page {
	switch {
	case p.ParentID != "" && !t.parentGone[p.ID]:
		return t.Children[p.ParentID]
	case t.folders[p.FolderID]:
		return t.InFolder[p.FolderID]
	}
	return t.Top
}
