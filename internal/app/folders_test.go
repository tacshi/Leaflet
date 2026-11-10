package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
)

func folderNames(folders []store.Folder) string {
	var out []string
	for _, f := range folders {
		out = append(out, f.Name)
	}
	return fmt.Sprint(out)
}

func TestSidebarNestedFolders(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	work, _ := a.st.CreateFolder("Work")
	home, _ := a.st.CreateFolder("Home")
	plans, err := a.st.CreateFolderIn("Plans", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Memo", FolderID: work.ID}); err != nil {
		t.Fatal(err)
	}
	roadmap, _ := a.st.CreatePage(store.NewPageOptions{Title: "Roadmap", FolderID: plans.ID})
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Loose"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if tt.HasText("Plans") {
		t.Fatalf("a closed folder shows its folders: %v", tt.Texts())
	}
	a.openPage(roadmap.ID)
	tt.Frame()
	tt.Frame()
	above := func(upper, lower string) {
		t.Helper()
		u, ok1 := tt.Find(upper)
		l, ok2 := tt.Find(lower)
		if !ok1 || !ok2 || u.Y >= l.Y {
			t.Errorf("%s is not above %s: %v", upper, lower, tt.Texts())
		}
	}
	above("Plans", "Memo")
	above("Roadmap", "Memo")
	above("Home", "Loose")
	drag := func(from, onto string, dy float32) {
		t.Helper()
		f, ok1 := tt.Find(from)
		o, ok2 := tt.Find(onto)
		if !ok1 || !ok2 {
			t.Fatalf("no %s or %s in %v", from, onto, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		ty := o.Y + o.H/2 + dy
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x, y+(ty-y)*float32(i)/4)
		}
		tt.Release(x, ty)
		a.reloadAll()
		tt.Frame()
		tt.Frame()
	}
	drag("Home", "Plans", 0)
	if got := folderNames(a.library().SubFolders[plans.ID]); got != "[Home]" {
		t.Fatalf("in Plans: %s", got)
	}
	drag("Work", "Home", 0)
	if got := folderNames(a.library().RootFolders); got != "[Work]" {
		t.Errorf("a folder went into its own folder: %s", got)
	}
	drag("Home", "Work", -10)
	if got := folderNames(a.library().RootFolders); got != "[Home Work]" {
		t.Fatalf("Home before Work at the top: %s", got)
	}
	drag("Home", "Plans", 10)
	if got := folderNames(a.library().SubFolders[work.ID]); got != "[Plans Home]" {
		t.Fatalf("Home after Plans in Work: %s", got)
	}
	if got := fmt.Sprint(a.library().FolderPath(home.ID)[0].Name); got != "Work" {
		t.Errorf("Home is in %s", got)
	}
	drag("Work", "Roadmap", 0)
	if got := folderNames(a.library().RootFolders); got != "[Work]" {
		t.Errorf("a folder went among its own pages: %s", got)
	}
	drag("Home", "Roadmap", 0)
	if got := folderNames(a.library().SubFolders[plans.ID]); got != "[Home]" {
		t.Fatalf("Home dropped on a page of Plans: in Plans %s", got)
	}
	drag("Home", "Loose", 0)
	if got := folderNames(a.library().RootFolders); got != "[Work Home]" {
		t.Fatalf("Home dropped on a top-level page: top %s", got)
	}
}

func TestMoveDialogForAFolder(t *testing.T) {
	a := templatesApp(t)
	work, _ := a.st.CreateFolder("Work")
	plans, _ := a.st.CreateFolderIn("Plans", work.ID)
	if _, err := a.st.CreateFolder("Home"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Memo"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.dialogs.openMoveFolder(work.ID)
	tt := ui.NewTester(func(c *ui.Context) { a.moveDialog(c) }, 600, 600)
	tt.Frame()
	tt.Frame()
	if tt.HasText("Plans") || tt.HasText("Memo") || !tt.HasText("Home") {
		t.Fatalf("a folder moves only into folders not in it: %v", tt.Texts())
	}
	if err := tt.Click("Home"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.reloadAll()
	if p := a.library().FolderPath(plans.ID); folderNames(p) != "[Home Work Plans]" {
		t.Errorf("path of Plans %s", folderNames(p))
	}
}
