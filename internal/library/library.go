package library

import (
	"cmp"
	"slices"
	"strings"

	"leaflet/internal/store"
)

type Tree struct {
	Folders     []store.Folder
	RootFolders []store.Folder
	SubFolders  map[string][]store.Folder
	Top         []store.Page
	InFolder    map[string][]store.Page
	Children    map[string][]store.Page
	Shared      []store.Page
	Tags        []store.TagCount

	folders    map[string]bool
	folderByID map[string]store.Folder
	folderUp   map[string]string
	parentGone map[string]bool
	tagged     map[string][]store.Page
}

func Arrange(spaceID string, pages []store.Page, folders []store.Folder) *Tree {
	t := &Tree{InFolder: map[string][]store.Page{}, Children: map[string][]store.Page{}, folders: map[string]bool{},
		parentGone: map[string]bool{}, SubFolders: map[string][]store.Folder{}, folderByID: map[string]store.Folder{},
		folderUp: map[string]string{}}
	for _, f := range folders {
		if f.SpaceID == spaceID {
			t.Folders = append(t.Folders, f)
			t.folders[f.ID] = true
		}
	}
	slices.SortStableFunc(t.Folders, func(a, b store.Folder) int {
		return cmp.Or(strings.Compare(a.SortKey, b.SortKey), strings.Compare(a.ID, b.ID))
	})
	t.nestFolders()
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

func (t *Tree) nestFolders() {
	for _, f := range t.Folders {
		t.folderByID[f.ID] = f
	}
	for _, f := range t.Folders {
		if parent := f.ParentFolderID; t.folders[parent] && !t.loops(f.ID) {
			t.folderUp[f.ID] = parent
			t.SubFolders[parent] = append(t.SubFolders[parent], f)
		} else {
			t.RootFolders = append(t.RootFolders, f)
		}
	}
}

// loops reports whether walking up from id comes back to id, which edits on two devices can make.
func (t *Tree) loops(id string) bool {
	seen := map[string]bool{}
	for cursor := t.folderByID[id].ParentFolderID; t.folders[cursor]; cursor = t.folderByID[cursor].ParentFolderID {
		if cursor == id {
			return true
		}
		if seen[cursor] {
			return false
		}
		seen[cursor] = true
	}
	return false
}

func (t *Tree) HasFolder(id string) bool { return t.folders[id] }

func (t *Tree) Folder(id string) (store.Folder, bool) {
	f, ok := t.folderByID[id]
	return f, ok
}

// FolderParent is the folder id is shown in, or "" at the top level.
func (t *Tree) FolderParent(id string) string { return t.folderUp[id] }

// FolderPath is the folder and the folders it is in, outermost first.
func (t *Tree) FolderPath(id string) []store.Folder {
	var path []store.Folder
	for cursor := id; t.folders[cursor]; cursor = t.folderUp[cursor] {
		path = append(path, t.folderByID[cursor])
	}
	slices.Reverse(path)
	return path
}

// FolderUnder reports whether candidate is ancestor or a folder inside it.
func (t *Tree) FolderUnder(candidate, ancestor string) bool {
	for cursor := candidate; t.folders[cursor]; cursor = t.folderUp[cursor] {
		if cursor == ancestor {
			return true
		}
	}
	return false
}

func (t *Tree) FolderSiblings(id string) []store.Folder {
	if parent := t.folderUp[id]; parent != "" {
		return t.SubFolders[parent]
	}
	return t.RootFolders
}

// FolderOrder is every folder in sidebar order, each with how deep it is.
func (t *Tree) FolderOrder() (folders []store.Folder, depths []int) {
	var walk func(list []store.Folder, depth int)
	walk = func(list []store.Folder, depth int) {
		for _, f := range list {
			folders, depths = append(folders, f), append(depths, depth)
			walk(t.SubFolders[f.ID], depth+1)
		}
	}
	walk(t.RootFolders, 0)
	return folders, depths
}

func (t *Tree) Siblings(p store.Page) []store.Page {
	switch {
	case p.ParentID != "" && !t.parentGone[p.ID]:
		return t.Children[p.ParentID]
	case t.folders[p.FolderID]:
		return t.InFolder[p.FolderID]
	}
	return t.Top
}
