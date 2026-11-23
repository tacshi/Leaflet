package store

import (
	"strconv"
	"testing"
	"time"
)

func TestServedLink(t *testing.T) {
	s := open(t)
	if s.ServesLink("p1") {
		t.Fatal("a link served with none recorded")
	}
	if err := s.KeepServedLink("p1"); err != nil {
		t.Fatal(err)
	}
	if !s.ServesLink("p1") || s.ServesLink("p2") || s.ServesLink("") {
		t.Error("the record of p1's link says another page's")
	}
	if err := s.EndServedLink("p2"); err != nil || !s.ServesLink("p1") {
		t.Errorf("ending p2's link took p1's off: %v", err)
	}
	if err := s.EndServedLink("p1"); err != nil || s.ServesLink("p1") {
		t.Errorf("p1's link still served after it ended: %v", err)
	}
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	for _, v := range []string{"p1|" + past, "p1", "p1|soon"} {
		if err := s.SetValue(servedLinkKey, v); err != nil {
			t.Fatal(err)
		}
		if s.ServesLink("p1") {
			t.Errorf("record %q serves the link", v)
		}
	}
}

func TestPublicLink(t *testing.T) {
	s := open(t)
	if linked, known := s.PublicLink("p1"); linked || !known {
		t.Errorf("pages on this device alone: linked %v, known %v", linked, known)
	}
	if err := s.KeepServedLink("p1"); err != nil {
		t.Fatal(err)
	}
	if linked, known := s.PublicLink("p1"); !linked || !known {
		t.Errorf("served here: linked %v, known %v", linked, known)
	}
	settings := s.Settings()
	settings.ICloudSync = true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if linked, known := s.PublicLink("p2"); linked || known {
		t.Errorf("syncing through iCloud Drive with nothing read: linked %v, known %v", linked, known)
	}
	soon, past := time.Now().Add(time.Hour), time.Now().Add(-time.Minute)
	if err := s.KeepPublicLinks(map[string]time.Time{"p2": soon, "p3": past}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"p1": true, "p2": true, "p3": false, "p4": false} {
		if linked, known := s.PublicLink(id); linked != want || !known {
			t.Errorf("%s: linked %v, known %v", id, linked, known)
		}
	}
	space := s.Session().SpaceID
	for name, v := range map[string]string{
		"read too long ago": `{"space":"` + space + `","checked":` + strconv.FormatInt(time.Now().Add(-PublicLinksFresh-time.Minute).UnixMilli(), 10) + `,"pages":{"p2":` + strconv.FormatInt(soon.UnixMilli(), 10) + `}}`,
		"read ahead":        `{"space":"` + space + `","checked":` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `}`,
		"another account":   `{"space":"elsewhere","checked":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `}`,
		"unreadable":        `{"checked":`,
	} {
		if err := s.SetValue(publicLinksKey, v); err != nil {
			t.Fatal(err)
		}
		if linked, known := s.PublicLink("p4"); linked || known {
			t.Errorf("%s: linked %v, known %v", name, linked, known)
		}
		if linked, known := s.PublicLink("p1"); !linked || !known {
			t.Errorf("%s, served here: linked %v, known %v", name, linked, known)
		}
	}
}
