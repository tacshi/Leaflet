package sync

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
)

func TestICloudJoinsAgainAfterAnAccountsSpace(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	pa := a.page("From A", para("a1", "one"))
	a.sync()
	pb := b.page("From B", para("b1", "two"))
	syncAll(a, b)
	folder := b.st.Session().SpaceID

	b.svc.SetICloud(false)
	if err := b.svc.moveInto(doc.NewRowID(), "My space", "user-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.st.DB().Exec(`DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.EnableICloud(); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if got := b.st.Session().SpaceID; got != folder {
		t.Fatalf("b's pages are in %s, the folder's space is %s", got, folder)
	}
	nextStamp()
	if err := a.st.RenamePage(pa.ID, "A renamed"); err != nil {
		t.Fatal(err)
	}
	if err := b.st.RenamePage(pb.ID, "B renamed"); err != nil {
		t.Fatal(err)
	}
	na := a.page("New on A", para("n1", "x"))
	syncAll(a, b)
	syncAll(a, b)
	if b.title(pa.ID) != "A renamed" || a.title(pb.ID) != "B renamed" || b.title(na.ID) != "New on A" {
		t.Errorf("b has %q and %q, a has %q", b.title(pa.ID), b.title(na.ID), a.title(pb.ID))
	}
}

func TestAPageFileWithoutContentEmptiesNoPage(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	pa := a.page("From A", para("a1", "one"), para("a2", "two"))
	syncAll(a, b)
	b.edit(pa.ID, "a1", "one!")
	syncAll(a, b)
	if a.text(pa.ID, "a1") != "one!" {
		t.Fatalf("a has %q", a.text(pa.ID, "a1"))
	}
	rel, _ := icloud.PagePath(b.st.DeviceID(), pa.ID)
	version := strconv.FormatInt(time.Now().UnixMilli()+1000, 10)
	file := `{"format":1,"page_id":"` + pa.ID + `","version":` + version + `,"merged":{}}`
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	a.svc.SetOpen(pa.ID, true)
	time.Sleep(300 * time.Millisecond)
	a.sync()
	if a.text(pa.ID, "a1") != "one!" || a.text(pa.ID, "a2") != "two" {
		t.Errorf("a has %q and %q", a.text(pa.ID, "a1"), a.text(pa.ID, "a2"))
	}
}
