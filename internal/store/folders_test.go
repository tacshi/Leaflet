package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func childFolders(t *testing.T, s *Store, parent string) string {
	t.Helper()
	all, err := s.Folders()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range all {
		if f.ParentFolderID == parent {
			out = append(out, f.Name)
		}
	}
	return fmt.Sprint(out)
}

func TestNestedFoldersMove(t *testing.T) {
	s := open(t)
	a, _ := s.CreateFolder("A")
	b, _ := s.CreateFolder("B")
	c, err := s.CreateFolderIn("C", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := s.CreateFolderIn("D", a.ID)
	if got := childFolders(t, s, a.ID); got != "[C D]" {
		t.Errorf("in A: %s", got)
	}
	if got := childFolders(t, s, ""); got != "[A B]" {
		t.Errorf("top: %s", got)
	}
	if err := s.MoveFolderBefore(d.ID, a.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, a.ID); got != "[D C]" {
		t.Errorf("D before C: %s", got)
	}
	if err := s.MoveFolder(b.ID, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, c.ID); got != "[B]" {
		t.Errorf("in C: %s", got)
	}
	for _, into := range []string{a.ID, c.ID, b.ID} {
		if err := s.MoveFolder(a.ID, into, ""); !errors.Is(err, ErrFolderIntoSelf) {
			t.Errorf("A into its own %s: %v", into, err)
		}
	}
	if err := s.MoveFolder(c.ID, "", a.ID); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, ""); got != "[A C]" {
		t.Errorf("C after A at the top: %s", got)
	}
	if !s.FolderUnder(b.ID, c.ID) || s.FolderUnder(c.ID, b.ID) {
		t.Error("FolderUnder")
	}
}

func TestNestedFolderStaysInItsSpace(t *testing.T) {
	s := open(t)
	a, _ := s.CreateFolder("A")
	other := Folder{ID: "11111111-1111-4111-8111-111111111111", SpaceID: "elsewhere", Name: "Theirs", SortKey: "a"}
	if err := s.PutFolder(other); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveFolder(a.ID, other.ID, ""); !errors.Is(err, ErrFolderAcrossSpaces) {
		t.Errorf("into another space: %v", err)
	}
	if _, err := s.CreateFolderIn("B", other.ID); !errors.Is(err, ErrFolderAcrossSpaces) {
		t.Errorf("made in another space: %v", err)
	}
	gone, _ := s.CreateFolder("Gone")
	if err := s.DeleteFolder(gone); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveFolder(a.ID, gone.ID, ""); !errors.Is(err, ErrFolderIntoDeleted) {
		t.Errorf("into a deleted folder: %v", err)
	}
}

func TestDeleteFolderLiftsItsContentsOneLevel(t *testing.T) {
	s := open(t)
	top, _ := s.CreateFolder("Top")
	if _, err := s.CreateFolderIn("Before", top.ID); err != nil {
		t.Fatal(err)
	}
	mid, _ := s.CreateFolderIn("Mid", top.ID)
	if _, err := s.CreateFolderIn("After", top.ID); err != nil {
		t.Fatal(err)
	}
	x, _ := s.CreateFolderIn("X", mid.ID)
	if _, err := s.CreateFolderIn("Y", mid.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreatePage(NewPageOptions{Title: "In mid", FolderID: mid.ID})
	if err := s.DeleteFolder(mid); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, top.ID); got != "[Before X Y After]" {
		t.Errorf("in Top: %s", got)
	}
	if got, _ := s.Page(p.ID); got.FolderID != top.ID {
		t.Errorf("page went to %q, want Top", got.FolderID)
	}
	if err := s.DeleteFolder(top); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, ""); got != "[Before X Y After]" {
		t.Errorf("at the top: %s", got)
	}
	if got, _ := s.Page(p.ID); got.FolderID != "" {
		t.Errorf("page in %q, want the top level", got.FolderID)
	}
	if f, _ := s.Folder(x.ID); f.ParentFolderID != "" {
		t.Error("X kept a deleted parent")
	}
}

func TestMovingAFolderToWhereItIsChangesNothing(t *testing.T) {
	s := open(t)
	a, _ := s.CreateFolder("A")
	b, _ := s.CreateFolder("B")
	c, _ := s.CreateFolder("C")
	key := func() string {
		t.Helper()
		f, err := s.Folder(b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return f.SortKey
	}
	was := key()
	if err := s.MoveFolder(b.ID, "", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveFolderBefore(b.ID, "", c.ID); err != nil {
		t.Fatal(err)
	}
	if got := key(); got != was {
		t.Errorf("B, left between A and C, went from %q to %q", was, got)
	}
	if err := s.MoveFolder(b.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := childFolders(t, s, ""); got != "[A C B]" {
		t.Errorf("B last: %s", got)
	}
	was = key()
	if err := s.MoveFolder(b.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := key(); got != was {
		t.Errorf("B, left last, went from %q to %q", was, got)
	}
}

func loopFolders(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for i, id := range ids {
		f, err := s.Folder(id)
		if err != nil {
			t.Fatal(err)
		}
		f.ParentFolderID = ids[(i+1)%len(ids)]
		if err := s.PutFolder(f); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteFolderInALoopLiftsItsContentsToTheTop(t *testing.T) {
	s := open(t)
	x, _ := s.CreateFolder("X")
	y, _ := s.CreateFolder("Y")
	p, _ := s.CreatePage(NewPageOptions{Title: "In X", FolderID: x.ID})
	loopFolders(t, s, x.ID, y.ID)
	if err := s.DeleteFolder(x); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(y.ID); f.ParentFolderID != "" {
		t.Errorf("Y went into %q, want the top level", f.ParentFolderID)
	}
	if got, _ := s.Page(p.ID); got.FolderID != "" {
		t.Errorf("page went into %q, want the top level", got.FolderID)
	}
}

func TestSettleFolderLoopsLiftsTheSmallestID(t *testing.T) {
	s := open(t)
	x, _ := s.CreateFolder("X")
	y, _ := s.CreateFolder("Y")
	z, _ := s.CreateFolderIn("Z", y.ID)
	if err := s.SettleFolderLoops(); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(z.ID); f.ParentFolderID != y.ID {
		t.Error("a folder in no loop was moved")
	}
	loopFolders(t, s, x.ID, y.ID)
	for _, e := range mustPending(t, s) {
		if err := s.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	least, other := x, y
	if y.ID < x.ID {
		least, other = y, x
	}
	if err := s.Queue("folders", other.ID, FolderToRemote(other), "renamed folder", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleFolderLoops(); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(least.ID); f.ParentFolderID != other.ID {
		t.Error("a loop with a folder whose change has yet to sync was settled")
	}
	if err := s.Dequeue(mustPending(t, s)[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleFolderLoops(); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(least.ID); f.ParentFolderID != "" {
		t.Errorf("%s stayed in %q, want the top level", least.Name, f.ParentFolderID)
	}
	if f, _ := s.Folder(other.ID); f.ParentFolderID != least.ID {
		t.Errorf("%s is in %q, want %s", other.Name, f.ParentFolderID, least.Name)
	}
	if !s.HasPending("folders", least.ID) || s.HasPending("folders", other.ID) {
		t.Error("the lift, and nothing else, should be queued to sync")
	}
	if err := s.MoveFolder(z.ID, least.ID, ""); err != nil {
		t.Errorf("moving a folder once the loop is settled: %v", err)
	}

	self, _ := s.CreateFolder("Self")
	for _, e := range mustPending(t, s) {
		if err := s.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	loopFolders(t, s, self.ID)
	if err := s.SettleFolderLoops(); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(self.ID); f.ParentFolderID != "" {
		t.Error("a folder stayed its own parent")
	}
}

func TestMoveFolderIntoOneUnderADeletedFolder(t *testing.T) {
	s := open(t)
	p, _ := s.CreateFolder("P")
	x, _ := s.CreateFolderIn("X", p.ID)
	d, _ := s.CreateFolder("D")
	if err := s.MoveFolder(d.ID, x.ID, ""); err != nil {
		t.Fatal(err)
	}
	// X is deleted on another device, which does not know D is in it.
	gone, _ := s.Folder(x.ID)
	gone.DeletedAt = gone.UpdatedAt
	if err := s.PutFolder(gone); err != nil {
		t.Fatal(err)
	}
	if s.FolderUnder(d.ID, p.ID) {
		t.Error("D, shown at the top level, counts as under P")
	}
	if err := s.MoveFolder(p.ID, d.ID, ""); err != nil {
		t.Fatalf("P into D, which is shown at the top level: %v", err)
	}
	if err := s.RestoreFolder(x.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(x.ID); f.ParentFolderID != "" || !f.DeletedAt.IsZero() {
		t.Errorf("restored X is in %q (deleted %v), want the top level", f.ParentFolderID, !f.DeletedAt.IsZero())
	}
	if !s.FolderUnder(p.ID, x.ID) {
		t.Error("P should be under the restored X, through D")
	}
}

func TestRestoreFolderKeepsItsParent(t *testing.T) {
	s := open(t)
	p, _ := s.CreateFolder("P")
	x, _ := s.CreateFolderIn("X", p.ID)
	gone, _ := s.Folder(x.ID)
	gone.DeletedAt = gone.UpdatedAt
	if err := s.PutFolder(gone); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreFolder(x.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Folder(x.ID); f.ParentFolderID != p.ID {
		t.Errorf("restored X is in %q, want P", f.ParentFolderID)
	}
}

func TestPageGoesToTheTopLevelWhenItsFolderIsDeleted(t *testing.T) {
	s := open(t)
	gone, _ := s.CreateFolder("Gone")
	if err := s.DeleteFolder(gone); err != nil {
		t.Fatal(err)
	}
	kept, _ := s.CreateFolder("Kept")
	// A folder with no row here may be one that has yet to be pulled: the page keeps it.
	for folder, want := range map[string]string{gone.ID: "", "not-here": "not-here"} {
		p, err := s.CreatePage(NewPageOptions{Title: "Made", FolderID: folder})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Page(p.ID); got.FolderID != want || p.FolderID != want {
			t.Errorf("page made in folder %q is in %q, want %q", folder, got.FolderID, want)
		}
		if err := s.MovePage(p.ID, "", kept.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.MovePage(p.ID, "", folder, ""); err != nil {
			t.Fatalf("move into %q: %v", folder, err)
		}
		if got, _ := s.Page(p.ID); got.FolderID != want {
			t.Errorf("page moved into folder %q is in %q, want %q", folder, got.FolderID, want)
		}
		if err := s.MovePage(p.ID, "", kept.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.PlacePage(p.ID, "", folder, ""); err != nil {
			t.Fatalf("place into %q: %v", folder, err)
		}
		if got, _ := s.Page(p.ID); got.FolderID != want {
			t.Errorf("page placed in folder %q is in %q, want %q", folder, got.FolderID, want)
		}
	}
}

func TestReorderingASharedPageKeepsItsFolder(t *testing.T) {
	s := open(t)
	old := "2026-01-01T00:00:00Z"
	// The owner's folders are never on the device of someone a page is shared with.
	for id, key := range map[string]string{"theirs-1": "V", "theirs-2": "W"} {
		if err := s.PutRemotePage(map[string]any{"id": id, "space_id": "other", "folder_id": "their-folder", "title": id,
			"sort_key": key, "created_at": old, "updated_at": old}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MovePageBefore("theirs-2", "", "their-folder", "theirs-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Page("theirs-2")
	first, _ := s.Page("theirs-1")
	if got.FolderID != "their-folder" || got.SortKey >= first.SortKey {
		t.Errorf("reordered into folder %q at %q, want it kept in its folder before %q", got.FolderID, got.SortKey, first.SortKey)
	}
	var payload map[string]any
	for _, e := range mustPending(t, s) {
		if e.Entity == "pages" && e.EntityID == "theirs-2" {
			if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if payload["folder_id"] != "their-folder" || slices.Contains(payload[ChangedKey].([]any), any("folder_id")) {
		t.Errorf("the reorder goes up as %v", payload)
	}
}

func TestFolderNamesAreClamped(t *testing.T) {
	s := open(t)
	long := strings.Repeat("é", 100_000)
	f, err := s.CreateFolder(long)
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(f.Name); n != 2000 {
		t.Errorf("made with a name of %d characters, want 2000", n)
	}
	if got, _ := s.Folder(f.ID); got.Name != f.Name {
		t.Error("the stored name is not the clamped one")
	}
	if err := s.RenameFolder(f, "a"+long); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Folder(f.ID); utf8.RuneCountInString(got.Name) != 2000 || !strings.HasPrefix(got.Name, "aé") {
		t.Errorf("renamed to %d characters, want 2000", utf8.RuneCountInString(got.Name))
	}
}
