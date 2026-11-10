package library

import (
	"fmt"
	"testing"

	"leaflet/internal/store"
)

func page(id, parent, folder, space, key string) store.Page {
	return store.Page{ID: id, ParentID: parent, FolderID: folder, SpaceID: space, Title: id, SortKey: key}
}

func ids(pages []store.Page) string {
	var out []string
	for _, p := range pages {
		out = append(out, p.ID)
	}
	return fmt.Sprint(out)
}

func TestArrange(t *testing.T) {
	tmpl := page("t", "", "", "me", "a")
	tmpl.Template = true
	tree := Arrange("me", []store.Page{
		page("b", "", "", "me", "b"), page("a", "", "", "me", "a"), page("a2", "a", "", "me", "b"),
		page("a1", "a", "", "me", "a"), page("f1", "", "f", "me", "a"), page("tie2", "", "", "me", "c"),
		page("tie1", "", "", "me", "c"), tmpl, page("t1", "t", "", "me", "a"),
		page("s", "", "", "them", "b"), page("s1", "s", "", "them", "a"), page("lone", "gone", "", "them", "a"),
	}, []store.Folder{
		{ID: "g", SpaceID: "me", Name: "Home", SortKey: "b"}, {ID: "f", SpaceID: "me", Name: "Work", SortKey: "a"},
		{ID: "x", SpaceID: "them", Name: "Theirs", SortKey: "a"},
	})
	for _, c := range []struct{ what, got, want string }{
		{"top", ids(tree.Top), "[a b tie1 tie2]"},
		{"under a", ids(tree.Children["a"]), "[a1 a2]"},
		{"under the template", ids(tree.Children["t"]), "[t1]"},
		{"in Work", ids(tree.InFolder["f"]), "[f1]"},
		{"shared", ids(tree.Shared), "[lone s]"},
		{"under s", ids(tree.Children["s"]), "[s1]"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %s, want %s", c.what, c.got, c.want)
		}
	}
	if len(tree.Folders) != 2 || tree.Folders[0].ID != "f" || tree.Folders[1].ID != "g" {
		t.Errorf("folders %+v, want the user's, Work first", tree.Folders)
	}
	if got := ids(tree.Siblings(page("a2", "a", "", "me", "b"))); got != "[a1 a2]" {
		t.Errorf("siblings of a2: %s", got)
	}
	if got := ids(tree.Siblings(page("f1", "", "f", "me", "a"))); got != "[f1]" {
		t.Errorf("siblings of f1: %s", got)
	}
}

func TestPagesOfAFolderGone(t *testing.T) {
	tree := Arrange("me", []store.Page{page("a", "", "", "me", "a"), page("left", "", "gone", "me", "b"), page("odd", "", "x", "me", "c")},
		[]store.Folder{{ID: "x", SpaceID: "them"}})
	if got := ids(tree.Top); got != "[a left odd]" {
		t.Errorf("top %s", got)
	}
	if tree.HasFolder("gone") || tree.HasFolder("x") {
		t.Error("a folder gone, or of another space, is in the tree")
	}
	if got := ids(tree.Siblings(page("left", "", "gone", "me", "b"))); got != "[a left odd]" {
		t.Errorf("siblings %s", got)
	}
}

func TestPagesOfAParentGone(t *testing.T) {
	tmpl := page("t1", "gone", "", "me", "c")
	tmpl.Template = true
	tree := Arrange("me", []store.Page{
		page("a", "", "", "me", "a"), page("left", "gone", "", "me", "b"), page("filed", "gone", "f", "me", "a"),
		page("lone", "gone", "", "them", "a"), tmpl,
	}, []store.Folder{{ID: "f", SpaceID: "me", Name: "Work"}})
	for _, c := range []struct{ what, got, want string }{
		{"top", ids(tree.Top), "[a left]"},
		{"in Work", ids(tree.InFolder["f"]), "[filed]"},
		{"shared", ids(tree.Shared), "[lone]"},
		{"under the parent gone", ids(tree.Children["gone"]), "[lone t1]"},
		{"siblings of left", ids(tree.Siblings(page("left", "gone", "", "me", "b"))), "[a left]"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %s, want %s", c.what, c.got, c.want)
		}
	}
}

func TestALoopOfParents(t *testing.T) {
	tree := Arrange("me", []store.Page{page("x", "y", "", "me", "a"), page("y", "x", "", "me", "a")}, nil)
	if len(tree.Top) != 0 || ids(tree.Children["x"]) != "[y]" || ids(tree.Children["y"]) != "[x]" {
		t.Errorf("top %s, under x %s, under y %s", ids(tree.Top), ids(tree.Children["x"]), ids(tree.Children["y"]))
	}
}

func TestTagged(t *testing.T) {
	tagged := func(id, title string, tags ...string) store.Page {
		p := page(id, "", "", "me", "a")
		p.Title, p.Tags = title, tags
		return p
	}
	tmpl := tagged("t", "A template", "home")
	tmpl.Template = true
	tree := Arrange("me", []store.Page{
		tagged("p1", "groceries", "Home"), tagged("p2", "Bills", "home", "work"), tmpl, tagged("p3", "Chores", "HOME"),
	}, nil)
	if got := ids(tree.Tagged("home")); got != "[p2 p3 p1]" {
		t.Errorf("home: %s", got)
	}
	if got := ids(tree.Tagged("Work")); got != "[p2]" {
		t.Errorf("work: %s", got)
	}
	if len(tree.Tagged("none")) != 0 {
		t.Error("pages of a tag no page has")
	}
	if fmt.Sprint(tree.Tags) != "[{Home 3} {work 1}]" {
		t.Errorf("tags %v", tree.Tags)
	}
}
