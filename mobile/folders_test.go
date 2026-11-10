package mobile

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestMoveFolder(t *testing.T) {
	c, _ := open(t)
	folder := func(name, parent string) string {
		t.Helper()
		raw, err := c.CreateFolderIn(name, parent)
		if err != nil {
			t.Fatal(err)
		}
		var f folderRow
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatal(err)
		}
		if f.ParentID != parent {
			t.Errorf("%s in %q, want %q", name, f.ParentID, parent)
		}
		return f.ID
	}
	lib := func() library {
		t.Helper()
		raw, err := c.Library()
		if err != nil {
			t.Fatal(err)
		}
		var out library
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	work, home := folder("Work", ""), folder("Home", "")
	plans := folder("Plans", work)
	if got := lib(); fmt.Sprint(got.RootFolders) != fmt.Sprint([]string{work, home}) ||
		fmt.Sprint(got.SubFolders[work]) != fmt.Sprint([]string{plans}) {
		t.Fatalf("roots %v, in Work %v", got.RootFolders, got.SubFolders[work])
	}
	if err := c.MoveFolderBefore(home, work, plans); err != nil {
		t.Fatal(err)
	}
	if got := lib(); fmt.Sprint(got.RootFolders) != fmt.Sprint([]string{work}) ||
		fmt.Sprint(got.SubFolders[work]) != fmt.Sprint([]string{home, plans}) {
		t.Fatalf("roots %v, in Work %v", got.RootFolders, got.SubFolders[work])
	}
	if err := c.MoveFolder(work, plans, ""); err == nil {
		t.Error("a folder moved into a folder inside it")
	}
	if err := c.MoveFolder(plans, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := lib(); fmt.Sprint(got.RootFolders) != fmt.Sprint([]string{work, plans}) {
		t.Errorf("roots %v", got.RootFolders)
	}
}
