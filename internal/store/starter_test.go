package store

import (
	"os"
	"path/filepath"
	"testing"

	"leaflet/internal/doc"
)

func starter(t *testing.T, s *Store) (guide, sub, tmpl, mine Page, pic File) {
	t.Helper()
	var err error
	if guide, err = s.CreatePage(NewPageOptions{Title: "Welcome", Tags: []string{"guide"}}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "dawn.jpg")
	if err := os.WriteFile(src, []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pic, err = s.AddFile(src, "", guide.ID, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if sub, err = s.CreatePage(NewPageOptions{ParentID: guide.ID, Title: "Weekly team sync"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveContent(guide.ID, doc.FromBlocks([]*doc.Node{{ID: "b1", Type: "paragraph"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(guide.ID, "b1", "Try a comment", "", "", "agent", "Leaflet"); err != nil {
		t.Fatal(err)
	}
	if tmpl, err = s.CreatePage(NewPageOptions{Title: "Weekly team sync", Template: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue("welcome.page", guide.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStarterPages([]string{guide.ID, sub.ID, tmpl.ID}); err != nil {
		t.Fatal(err)
	}
	if mine, err = s.CreatePage(NewPageOptions{Title: "Mine"}); err != nil {
		t.Fatal(err)
	}
	return guide, sub, tmpl, mine, pic
}

func TestStarterPagesGoWhenJoiningALibrary(t *testing.T) {
	s := open(t)
	guide, sub, tmpl, mine, pic := starter(t, s)
	if err := s.SettleStarterPages(true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Page{guide, sub, tmpl} {
		if _, err := s.Page(p.ID); !IsNotFound(err) {
			t.Errorf("starter page %q is still here: %v", p.Title, err)
		}
		if s.HasPending("pages", p.ID) || s.HasPending("page_content", p.ID) {
			t.Errorf("starter page %q still waits to go up", p.Title)
		}
	}
	if threads, _ := s.Threads(guide.ID); len(threads) != 0 {
		t.Errorf("the guide's comment stayed: %+v", threads)
	}
	if _, err := s.File(pic.ID); err == nil || s.HasPending("files", pic.ID) {
		t.Error("the guide's picture stayed")
	}
	if _, err := os.Stat(pic.LocalPath); !os.IsNotExist(err) {
		t.Errorf("the picture's copy stayed: %v", err)
	}
	if _, err := s.Page(mine.ID); err != nil || !s.HasPending("pages", mine.ID) {
		t.Errorf("the user's own page: %v, waiting %v", err, s.HasPending("pages", mine.ID))
	}
	for _, k := range []string{starterKey, "welcome.page"} {
		if v, ok := s.Value(k); ok {
			t.Errorf("%s is still %q", k, v)
		}
	}
}

func TestStarterPagesTakenUpStay(t *testing.T) {
	for name, take := range map[string]func(s *Store, guide, sub, tmpl Page) error{
		"renamed": func(s *Store, guide, _, _ Page) error { return s.RenamePage(guide.ID, "Mine now") },
		"edited": func(s *Store, _, sub, _ Page) error {
			_, _, err := s.SaveContent(sub.ID, doc.FromBlocks([]*doc.Node{{ID: "n", Type: "paragraph"}}))
			return err
		},
		"tagged": func(s *Store, _, _, tmpl Page) error { return s.SetPageTags(tmpl.ID, []string{"work"}) },
		"page inside": func(s *Store, guide, _, _ Page) error {
			_, err := s.CreatePage(NewPageOptions{ParentID: guide.ID, Title: "Notes"})
			return err
		},
		"commented": func(s *Store, guide, _, _ Page) error {
			_, err := s.AddComment(guide.ID, "b1", "Mine", "", "", "", "")
			return err
		},
		"template used": func(s *Store, _, _, tmpl Page) error {
			f, err := s.CreateFolder("Meetings")
			if err != nil {
				return err
			}
			return s.SetFolderDefaultTemplate(f, tmpl.ID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			guide, sub, tmpl, _, _ := starter(t, s)
			nextStamp()
			if err := take(s, guide, sub, tmpl); err != nil {
				t.Fatal(err)
			}
			if err := s.SettleStarterPages(true); err != nil {
				t.Fatal(err)
			}
			for _, p := range []Page{guide, sub, tmpl} {
				if _, err := s.Page(p.ID); err != nil {
					t.Errorf("starter page %q went: %v", p.Title, err)
				}
			}
			if _, ok := s.Value(starterKey); ok {
				t.Error("the starter pages are not settled")
			}
		})
	}
	t.Run("deleted", func(t *testing.T) {
		s := open(t)
		guide, _, _, _, _ := starter(t, s)
		nextStamp()
		if _, err := s.DeletePage(guide.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.SettleStarterPages(true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Page(guide.ID); !IsNotFound(err) {
			t.Errorf("the deleted guide stayed: %v", err)
		}
	})
}

func TestStarterPagesStayStartingALibrary(t *testing.T) {
	s := open(t)
	guide, _, _, _, _ := starter(t, s)
	if err := s.SettleStarterPages(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleStarterPages(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Page(guide.ID); err != nil {
		t.Errorf("the guide went: %v", err)
	}
}
