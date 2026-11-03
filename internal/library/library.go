// Package library arranges the library as the sidebar shows it, for the
// desktop and the iOS app alike: the folders of the user's space with
// their pages, the user's pages in no folder, the pages under each page,
// the pages others shared with the user, and the pages of each tag. Each
// app draws the tree it is given.
package library

import (
	"cmp"
	"slices"
	"strings"

	"leaflet/internal/store"
)

// Tree is the library as the sidebar arranges it, worked out once as the
// library is read: the sidebar looks pages up row by row, which a search
// of every page each time makes slow in a large library.
type Tree struct {
	// Folders are the folders of the user's space, in order.
	Folders []store.Folder
	// Top are the user's pages at the top level in no folder, in order. A
	// page of a folder that is gone, as another device deleted it while
	// this one put the page in it, is among them, not in no place at all.
	Top []store.Page
	// InFolder are the top-level pages of each folder, by its ID, in
	// order.
	InFolder map[string][]store.Page
	// Children are the pages under each page, by its ID, in order.
	Children map[string][]store.Page
	// Shared are the pages others shared with the user whose parent the
	// user has not, in order.
	Shared []store.Page
	// Tags are the tags of the pages, by name, with how many have each.
	Tags []store.TagCount

	folders map[string]bool
	// tagged are the pages with each tag, by the tag in lower case.
	tagged map[string][]store.Page
}

// Arrange arranges pages and folders for the user of space spaceID.
// Templates among the pages show apart from the tree, but their subpages
// are under them. Equal sort keys, from another client or before keys
// were made apart, go by ID, as every device sorts them alike.
func Arrange(spaceID string, pages []store.Page, folders []store.Folder) *Tree {
	t := &Tree{InFolder: map[string][]store.Page{}, Children: map[string][]store.Page{}, folders: map[string]bool{}}
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
		switch {
		case p.ParentID != "":
			t.Children[p.ParentID] = append(t.Children[p.ParentID], p)
		case p.Template || shared:
		case t.folders[p.FolderID]:
			t.InFolder[p.FolderID] = append(t.InFolder[p.FolderID], p)
		default:
			t.Top = append(t.Top, p)
		}
		if shared && !p.Template && (p.ParentID == "" || !have[p.ParentID]) {
			t.Shared = append(t.Shared, p)
		}
	}
	t.tag(pages)
	return t
}

// tag finds the tags of the pages but templates, and the pages with each,
// by title, in any case.
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
			// A page with a tag twice, in two cases, is under it once.
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

// Tagged returns the pages with tag, in any case, by title.
func (t *Tree) Tagged(tag string) []store.Page { return t.tagged[strings.ToLower(tag)] }

// HasFolder reports whether a folder of that ID is in the tree: one of
// the user's that is still there.
func (t *Tree) HasFolder(id string) bool { return t.folders[id] }

// Siblings returns the pages p is among: those under its parent, or the
// top-level pages of its folder, or the user's in none.
func (t *Tree) Siblings(p store.Page) []store.Page {
	switch {
	case p.ParentID != "":
		return t.Children[p.ParentID]
	case t.folders[p.FolderID]:
		return t.InFolder[p.FolderID]
	}
	return t.Top
}
