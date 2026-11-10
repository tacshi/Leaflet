package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
)

func TestActionToastsAlikeShowApart(t *testing.T) {
	a := &App{}
	var undone []int
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.toastView(c) })
	}, 600, 400)
	a.toastAction("Deleted “Untitled”", "Undo", func() { undone = append(undone, 1) })
	a.toastAction("Deleted “Untitled”", "Undo", func() { undone = append(undone, 2) })
	tt.Frame()
	if n := len(slices.DeleteFunc(tt.Texts(), func(s string) bool { return s != "Undo" })); n != 2 {
		t.Fatalf("%d Undo buttons, want 2: %q", n, tt.Texts())
	}
	for range 2 {
		if err := tt.Click("Undo"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	slices.Sort(undone)
	if !slices.Equal(undone, []int{1, 2}) {
		t.Errorf("undone %v, want both", undone)
	}
}

func TestCanEditIsReadAgainWhenMembersChange(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(&store.User{ID: "me"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePage(store.NewPageOptions{Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE pages SET space_id = 'theirs' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	a := &App{st: st}
	a.reload(store.Change{Table: "pages"})
	if a.canEdit(p.ID) {
		t.Fatal("a page of another space, shared with no one, can be edited")
	}
	if err := st.SetMembers(p.ID, []store.Member{{PageID: p.ID, UserID: "me", Role: "editor", Accepted: true}}); err != nil {
		t.Fatal(err)
	}
	if a.canEdit(p.ID) {
		t.Error("read again before the change was told")
	}
	a.reload(store.Change{Table: "page_members", PageID: p.ID})
	if !a.canEdit(p.ID) {
		t.Error("an editor cannot edit once the members changed")
	}
}
