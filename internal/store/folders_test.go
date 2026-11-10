package store

import (
	"errors"
	"fmt"
	"testing"
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
